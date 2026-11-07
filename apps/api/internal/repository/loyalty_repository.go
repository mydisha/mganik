package repository

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
)

// Nilai ref_type ledger/event untuk klaim loyalty.
const RefLoyaltyClaim = "LOYALTY_CLAIM"

// LoyaltyRepository mengakses loyalty_claims, loyalty_ledger, dan kolom
// percobaan PIN di serial_units.
type LoyaltyRepository struct{}

func NewLoyaltyRepository() *LoyaltyRepository { return &LoyaltyRepository{} }

// ClaimUnit adalah serial yang dikunci untuk klaim beserta status batch dan
// poin produk (atribut loyalty_points, kalau ada).
type ClaimUnit struct {
	ID                  uuid.UUID
	Code                string
	Status              string
	ProductID           *uuid.UUID
	BatchID             *uuid.UUID
	ParentID            *uuid.UUID
	LocationID          *uuid.UUID
	CustodianPartyID    *uuid.UUID
	ClaimSecretHash     []byte
	ClaimFailedAttempts int
	ClaimLockedAt       *time.Time
	FirstVerifiedAt     *time.Time
	BatchStatus         *string
	ProductPoints       *int64
}

// LockUnitForClaim mengunci serial (FOR UPDATE) untuk klaim. Semua klaim dan
// percobaan PIN untuk unit yang sama diserialkan oleh lock ini.
func (r *LoyaltyRepository) LockUnitForClaim(tx *gorm.DB, code string) (ClaimUnit, bool, error) {
	var rows []ClaimUnit
	err := tx.Raw(`
SELECT s.id, s.code, s.status, s.product_id, s.batch_id,
       -- Karton terakhir dari riwayat packing kalau unit sudah dilepas (dijual).
       COALESCE(s.parent_id, (SELECT e.parent_id FROM unit_events e
                               WHERE e.serial_unit_id = s.id AND e.biz_step = 'packing' AND e.parent_id IS NOT NULL
                               ORDER BY e.occurred_at DESC, e.recorded_at DESC LIMIT 1)) AS parent_id,
       s.location_id, s.custodian_party_id,
       s.claim_secret_hash, s.claim_failed_attempts, s.claim_locked_at, s.first_verified_at,
       b.status AS batch_status,
       CASE WHEN jsonb_typeof(p.attributes -> 'loyalty_points') = 'number'
            THEN CAST(p.attributes ->> 'loyalty_points' AS bigint) END AS product_points
  FROM serial_units s
  LEFT JOIN batches b ON b.id = s.batch_id
  LEFT JOIN products p ON p.id = s.product_id
 WHERE s.code = ?
   FOR UPDATE OF s`, code).Scan(&rows).Error
	if err != nil {
		return ClaimUnit{}, false, fmt.Errorf("mengunci serial untuk klaim: %w", err)
	}
	if len(rows) == 0 {
		return ClaimUnit{}, false, nil
	}
	return rows[0], true, nil
}

// RecordFailedPIN menaikkan percobaan PIN gagal dan mengunci unit saat batas
// tercapai. Mengembalikan jumlah percobaan dan status terkunci setelahnya.
func (r *LoyaltyRepository) RecordFailedPIN(tx *gorm.DB, id uuid.UUID, maxAttempts int, at time.Time) (int, bool, error) {
	var rows []struct {
		Attempts int
		Locked   bool
	}
	err := tx.Raw(`
UPDATE serial_units
   SET claim_failed_attempts = claim_failed_attempts + 1,
       claim_locked_at = CASE WHEN claim_failed_attempts + 1 >= CAST(@max AS int) AND claim_locked_at IS NULL
                              THEN CAST(@at AS timestamptz) ELSE claim_locked_at END,
       updated_at = CAST(@at AS timestamptz)
 WHERE id = CAST(@id AS uuid)
RETURNING claim_failed_attempts AS attempts, claim_locked_at IS NOT NULL AS locked`,
		map[string]any{"id": id, "max": maxAttempts, "at": at}).Scan(&rows).Error
	if err != nil {
		return 0, false, fmt.Errorf("mencatat PIN salah: %w", err)
	}
	if len(rows) == 0 {
		return 0, false, errors.New("mencatat PIN salah: serial tidak ditemukan")
	}
	return rows[0].Attempts, rows[0].Locked, nil
}

// ResetFailedPIN mengosongkan percobaan gagal setelah PIN benar.
func (r *LoyaltyRepository) ResetFailedPIN(tx *gorm.DB, id uuid.UUID) error {
	return tx.Model(&model.SerialUnit{}).Where("id = ? AND claim_failed_attempts > 0", id).
		Update("claim_failed_attempts", 0).Error
}

// ActiveClaim mengembalikan klaim PENDING/APPROVED unit (nil kalau tidak ada).
func (r *LoyaltyRepository) ActiveClaim(tx *gorm.DB, unitID uuid.UUID) (*model.LoyaltyClaim, error) {
	var rows []model.LoyaltyClaim
	err := tx.Where("serial_unit_id = ? AND status IN ?", unitID, []string{model.ClaimPending, model.ClaimApproved}).
		Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// ActivationOrigin mengembalikan status serial sebelum aktivasi konsumen
// TERAKHIR (SOLD atau AT_PARTNER), dari unit_events. Kosong kalau belum pernah.
func (r *LoyaltyRepository) ActivationOrigin(tx *gorm.DB, unitID uuid.UUID) (string, error) {
	var rows []string
	err := tx.Raw(`
SELECT from_status FROM unit_events
 WHERE serial_unit_id = ? AND biz_step = 'consumer_activating' AND from_status IS NOT NULL
 ORDER BY occurred_at DESC, recorded_at DESC
 LIMIT 1`, unitID).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rows[0], nil
}

// InsertClaim menyimpan klaim. Pelanggaran one_active_claim_per_unit
// dikembalikan apa adanya untuk dipetakan service.
func (r *LoyaltyRepository) InsertClaim(tx *gorm.DB, c *model.LoyaltyClaim) error {
	return tx.Create(c).Error
}

// InsertLedger menulis baris ledger; idempotency_key yang sama menjadi no-op.
func (r *LoyaltyRepository) InsertLedger(tx *gorm.DB, e model.LoyaltyLedgerEntry) (bool, error) {
	res := tx.Clauses(clause.OnConflict{OnConstraint: "loyalty_ledger_idempotent", DoNothing: true}).Create(&e)
	if res.Error != nil {
		return false, fmt.Errorf("menulis loyalty_ledger: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ---------------------------------------------------------------- penghitung risiko

// ClaimsByConsumerSince menghitung klaim akun sejak since.
func (r *LoyaltyRepository) ClaimsByConsumerSince(tx *gorm.DB, consumerID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := tx.Model(&model.LoyaltyClaim{}).Where("consumer_id = ? AND created_at >= ?", consumerID, since).Count(&n).Error
	return n, err
}

// SiblingClaimAccounts menghitung akun lain yang mengklaim unit satu induk
// (karton yang sama, sekarang atau menurut riwayat packing karena unit
// terjual dilepas dari kartonnya) sejak since.
func (r *LoyaltyRepository) SiblingClaimAccounts(tx *gorm.DB, parentID, consumerID uuid.UUID, since time.Time) (int64, error) {
	var n int64
	err := tx.Raw(`
SELECT count(DISTINCT c.consumer_id)
  FROM loyalty_claims c
 WHERE c.created_at >= CAST(@since AS timestamptz) AND c.consumer_id <> CAST(@consumer AS uuid)
   AND c.serial_unit_id IN (
         SELECT id FROM serial_units WHERE parent_id = CAST(@parent AS uuid)
       UNION
         SELECT serial_unit_id FROM unit_events WHERE biz_step = 'packing' AND parent_id = CAST(@parent AS uuid))`,
		map[string]any{"since": since, "consumer": consumerID, "parent": parentID}).Scan(&n).Error
	return n, err
}

// ClaimHistory merangkum klaim lama yang ditolak/dibalik untuk satu unit.
type ClaimHistory struct {
	// AnyRejected: unit pernah punya klaim REJECTED/REVERSED (siapa pun).
	AnyRejected bool
	// ConsumerRejected: klaim akun ini sendiri pada unit ini pernah ditolak.
	ConsumerRejected bool
}

// RejectedHistory membaca riwayat klaim REJECTED/REVERSED unit.
func (r *LoyaltyRepository) RejectedHistory(tx *gorm.DB, unitID, consumerID uuid.UUID) (ClaimHistory, error) {
	var h ClaimHistory
	err := tx.Raw(`
SELECT count(*) > 0 AS any_rejected, count(*) FILTER (WHERE consumer_id = CAST(@consumer AS uuid)) > 0 AS consumer_rejected
  FROM loyalty_claims
 WHERE serial_unit_id = CAST(@unit AS uuid) AND status IN ('REJECTED', 'REVERSED')`,
		map[string]any{"unit": unitID, "consumer": consumerID}).Scan(&h).Error
	return h, err
}

// LockSerialStatus mengunci baris serial (FOR UPDATE) dan mengembalikan statusnya.
func (r *LoyaltyRepository) LockSerialStatus(tx *gorm.DB, id uuid.UUID) (string, error) {
	var rows []string
	if err := tx.Raw(`SELECT status FROM serial_units WHERE id = ? FOR UPDATE`, id).Scan(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", errors.New("serial klaim tidak ditemukan")
	}
	return rows[0], nil
}

// FindClaim membaca klaim tanpa lock.
func (r *LoyaltyRepository) FindClaim(tx *gorm.DB, id uuid.UUID) (model.LoyaltyClaim, bool, error) {
	var c model.LoyaltyClaim
	err := tx.Where("id = ?", id).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c, false, nil
	}
	return c, err == nil, err
}

// FingerprintAccounts menghitung akun lain yang pernah mengklaim dari
// fingerprint perangkat yang sama.
func (r *LoyaltyRepository) FingerprintAccounts(tx *gorm.DB, fingerprint []byte, consumerID uuid.UUID) (int64, error) {
	var n int64
	err := tx.Raw(`
SELECT count(DISTINCT consumer_id) FROM scan_events
 WHERE actor_fingerprint = ? AND context = 'LOYALTY_CLAIM' AND consumer_id IS NOT NULL AND consumer_id <> ?`,
		fingerprint, consumerID).Scan(&n).Error
	return n, err
}

// NearOpenLocation melaporkan apakah titik berada dalam radiusM dari lokasi
// fisik (toko, gudang, mitra) dan, kalau duringOpeningHours, jam lokal lokasi
// itu 08:00–22:00.
func (r *LoyaltyRepository) NearOpenLocation(tx *gorm.DB, p domain.GeoPoint, radiusM float64, at time.Time, duringOpeningHours bool) (bool, error) {
	var ok bool
	err := tx.Raw(`
SELECT EXISTS (
    SELECT 1 FROM locations l JOIN location_types t ON t.code = l.type_code
     WHERE l.archived_at IS NULL AND NOT t.is_virtual AND l.lat IS NOT NULL AND l.lng IS NOT NULL
       AND 2 * 6371008.8 * asin(least(1, sqrt(
               power(sin(radians(l.lat - CAST(@lat AS float8)) / 2), 2)
             + cos(radians(CAST(@lat AS float8))) * cos(radians(l.lat))
             * power(sin(radians(l.lng - CAST(@lng AS float8)) / 2), 2)))) <= CAST(@radius AS float8)
       AND (NOT CAST(@hours AS boolean)
            OR EXTRACT(HOUR FROM (CAST(@at AS timestamptz) AT TIME ZONE l.timezone)) BETWEEN 8 AND 21)
)`, map[string]any{"lat": p.Lat, "lng": p.Lng, "radius": radiusM, "at": at, "hours": duringOpeningHours}).Scan(&ok).Error
	return ok, err
}

// ---------------------------------------------------------------- review & saldo

// LockClaim mengunci klaim FOR UPDATE.
func (r *LoyaltyRepository) LockClaim(tx *gorm.DB, id uuid.UUID) (model.LoyaltyClaim, bool, error) {
	var c model.LoyaltyClaim
	err := tx.Clauses(serialLocking).Where("id = ?", id).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c, false, nil
	}
	return c, err == nil, err
}

// Decide mengubah klaim dari status from ke to (conditional update).
func (r *LoyaltyRepository) Decide(tx *gorm.DB, id uuid.UUID, from, to string, by *uuid.UUID, at time.Time) (bool, error) {
	res := tx.Model(&model.LoyaltyClaim{}).Where("id = ? AND status = ?", id, from).
		Updates(map[string]any{"status": to, "decided_by": by, "decided_at": at, "updated_at": at})
	return res.RowsAffected == 1, res.Error
}

// ReleaseDue meng-approve klaim PENDING yang masa tunggunya habis dan tidak
// butuh review. Satu conditional UPDATE: aman dijalankan berkala dan dari dua
// instance sekaligus (baris yang sudah di-approve instance lain tidak lagi
// memenuhi WHERE status = 'PENDING' setelah lock dilepas, jadi tidak dobel).
func (r *LoyaltyRepository) ReleaseDue(tx *gorm.DB, at time.Time) (int64, error) {
	res := tx.Model(&model.LoyaltyClaim{}).
		Where("status = ? AND NOT review_required AND available_at <= ?", model.ClaimPending, at).
		Updates(map[string]any{"status": model.ClaimApproved, "decided_at": at, "updated_at": at})
	return res.RowsAffected, res.Error
}

// LoyaltyBalance adalah saldo poin konsumen.
type LoyaltyBalance struct {
	Available int64
	Pending   int64
}

// Balance menghitung saldo dari ledger (sumber kebenaran). Poin klaim baru
// tersedia kalau klaimnya APPROVED dan masa tunggunya lewat; klaim yang
// ditolak sudah netral karena EARN dan REVERSE saling meniadakan.
func (r *LoyaltyRepository) Balance(tx *gorm.DB, consumerID uuid.UUID, at time.Time) (LoyaltyBalance, error) {
	var b LoyaltyBalance
	err := tx.Raw(`
SELECT
  COALESCE(SUM(l.points) FILTER (WHERE c.id IS NULL OR (c.status = 'APPROVED' AND c.available_at <= CAST(@at AS timestamptz))), 0) AS available,
  COALESCE(SUM(l.points) FILTER (WHERE c.status = 'PENDING' OR (c.status = 'APPROVED' AND c.available_at > CAST(@at AS timestamptz))), 0) AS pending
  FROM loyalty_ledger l
  LEFT JOIN loyalty_claims c ON l.ref_type = 'LOYALTY_CLAIM' AND c.id = l.ref_id
 WHERE l.consumer_id = CAST(@consumer AS uuid)`, map[string]any{"consumer": consumerID, "at": at}).Scan(&b).Error
	return b, err
}

// ConsumerActive memeriksa konsumen ada dan berstatus ACTIVE.
func (r *LoyaltyRepository) ConsumerActive(tx *gorm.DB, id uuid.UUID) (bool, error) {
	var n int64
	err := tx.Model(&model.Consumer{}).Where("id = ? AND status = 'ACTIVE'", id).Count(&n).Error
	return n > 0, err
}
