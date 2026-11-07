package repository

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// InventoryRepository mengakses saldo, ledger, dan tabel pendukung stok.
// Semua method menerima tx dari service; jalur panas memakai SQL berparameter.
type InventoryRepository struct{}

// MovementEffect adalah delta bucket hasil perhitungan movement_types.
type MovementEffect struct {
	PhysicalDelta   int64
	ReservedDelta   int64
	QuarantineDelta int64
	IsPromise       bool
}

type invBalanceWithEffect struct {
	model.InventoryBalance
	PhysicalDelta   int64
	ReservedDelta   int64
	QuarantineDelta int64
	IsPromise       bool
}

// ApplyBalanceEffect meng-update saldo dalam SATU statement: delta tiap bucket
// dihitung dari movement_types (sign × qty), dan untuk tipe janji (is_promise)
// update hanya terjadi kalau available setelah mutasi masih >= keepAvailable.
// Di READ COMMITTED, Postgres menunggu row lock lalu mengevaluasi ulang WHERE
// pada versi baris terbaru, jadi tidak ada lost update tanpa SERIALIZABLE.
//
// qty boleh negatif hanya untuk tipe allow_either_sign. updated=false berarti
// tidak ada baris yang cocok: tipe tidak dikenal, arah qty tidak sah, saldo
// belum ada, atau (tipe janji) stok tidak cukup. Fakta yang melanggar CHECK
// (no_oversell, *_nonneg) dikembalikan sebagai error Postgres.
func (InventoryRepository) ApplyBalanceEffect(tx *gorm.DB, locationID, productID uuid.UUID, movementType string,
	qty, keepAvailable int64, now time.Time) (model.InventoryBalance, MovementEffect, bool, error) {
	var rows []invBalanceWithEffect
	// Semua operand di-CAST ke bigint: tanpa itu Postgres menyimpulkan tipe
	// parameter dari kolom smallint *_sign, sehingga qty > 32767 overflow.
	err := tx.Raw(`
		UPDATE inventory_balances b
		   SET physical_qty   = b.physical_qty   + CAST(mt.physical_sign AS bigint)   * CAST(@qty AS bigint),
		       reserved_qty   = b.reserved_qty   + CAST(mt.reserved_sign AS bigint)   * CAST(@qty AS bigint),
		       quarantine_qty = b.quarantine_qty + CAST(mt.quarantine_sign AS bigint) * CAST(@qty AS bigint),
		       version        = b.version + 1,
		       updated_at     = @now
		  FROM movement_types mt
		 WHERE mt.code = @type
		   AND (mt.allow_either_sign OR CAST(@qty AS bigint) > 0)
		   AND b.location_id = @loc
		   AND b.product_id  = @prod
		   AND (NOT mt.is_promise
		        OR (b.physical_qty - b.reserved_qty - b.quarantine_qty)
		           + CAST(mt.physical_sign - mt.reserved_sign - mt.quarantine_sign AS bigint) * CAST(@qty AS bigint)
		           >= CAST(@keep AS bigint))
		RETURNING b.location_id, b.product_id, b.physical_qty, b.reserved_qty, b.quarantine_qty,
		          b.available_qty, b.version, b.created_at, b.updated_at,
		          CAST(mt.physical_sign AS bigint)   * CAST(@qty AS bigint) AS physical_delta,
		          CAST(mt.reserved_sign AS bigint)   * CAST(@qty AS bigint) AS reserved_delta,
		          CAST(mt.quarantine_sign AS bigint) * CAST(@qty AS bigint) AS quarantine_delta,
		          mt.is_promise`,
		map[string]any{"qty": qty, "keep": keepAvailable, "type": movementType, "loc": locationID, "prod": productID, "now": now},
	).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return model.InventoryBalance{}, MovementEffect{}, false, err
	}
	r := rows[0]
	return r.InventoryBalance, MovementEffect{
		PhysicalDelta: r.PhysicalDelta, ReservedDelta: r.ReservedDelta,
		QuarantineDelta: r.QuarantineDelta, IsPromise: r.IsPromise,
	}, true, nil
}

// InsertMovement menulis baris ledger. inserted=false kalau idempotency_key
// sudah ada (transaksi lain yang memakai key sama sudah commit).
func (InventoryRepository) InsertMovement(tx *gorm.DB, m *model.InventoryMovement) (bool, error) {
	res := tx.Exec(`
		INSERT INTO inventory_movements (id, location_id, product_id, movement_type, physical_delta, reserved_delta,
		       quarantine_delta, ref_type, ref_id, idempotency_key, reason, actor_type, actor_id, occurred_at, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT ON CONSTRAINT movement_idempotent DO NOTHING`,
		m.ID, m.LocationID, m.ProductID, m.MovementType, m.PhysicalDelta, m.ReservedDelta, m.QuarantineDelta,
		m.RefType, m.RefID, m.IdempotencyKey, m.Reason, m.ActorType, m.ActorID, m.OccurredAt, m.RecordedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// MovementByKey mengambil baris ledger berdasarkan idempotency_key (nil kalau tidak ada).
func (InventoryRepository) MovementByKey(tx *gorm.DB, key string) (*model.InventoryMovement, error) {
	var m model.InventoryMovement
	err := tx.Where("idempotency_key = ?", key).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Balance membaca saldo tanpa lock (nil kalau belum ada).
func (InventoryRepository) Balance(tx *gorm.DB, locationID, productID uuid.UUID) (*model.InventoryBalance, error) {
	var b model.InventoryBalance
	err := tx.Where("location_id = ? AND product_id = ?", locationID, productID).Take(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// LockBalance mengunci baris saldo dengan FOR NO KEY UPDATE: lock yang sama
// dengan yang diambil UPDATE, sehingga mutasi lain menunggu, tetapi tidak
// memblokir pengecekan FK (KEY SHARE) dari insert ledger/reservasi. FOR UPDATE
// penuh akan memblokir FK check itu dan membuka peluang deadlock.
func (InventoryRepository) LockBalance(tx *gorm.DB, locationID, productID uuid.UUID) (*model.InventoryBalance, error) {
	var rows []model.InventoryBalance
	err := tx.Raw(`SELECT * FROM inventory_balances WHERE location_id = ? AND product_id = ? FOR NO KEY UPDATE`,
		locationID, productID).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// EnsureBalance membuat baris saldo nol kalau belum ada.
func (InventoryRepository) EnsureBalance(tx *gorm.DB, locationID, productID uuid.UUID, now time.Time) error {
	return tx.Exec(`
		INSERT INTO inventory_balances (location_id, product_id, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (location_id, product_id) DO NOTHING`, locationID, productID, now, now).Error
}

// MovementType membaca satu tipe mutasi (nil kalau kode tidak dikenal).
func (InventoryRepository) MovementType(tx *gorm.DB, code string) (*model.MovementType, error) {
	var mt model.MovementType
	err := tx.Where("code = ?", code).Take(&mt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &mt, nil
}

// CandidateRow adalah stok satu produk di satu lokasi yang boleh memenuhi order online.
type CandidateRow struct {
	LocationID      uuid.UUID
	LocationCode    string
	TypeCode        string
	SyncState       string
	ProductID       uuid.UUID
	AvailableQty    int64
	OnlineBufferQty int64
}

// FulfillmentCandidates mengembalikan saldo produk di lokasi yang aktif dan
// online_fulfillment_enabled, urut prioritas: gudang pusat, hub, lalu lokasi
// lain (toko), kemudian kode lokasi. Dibaca tanpa lock; reservasi tetap
// memakai conditional update.
func (InventoryRepository) FulfillmentCandidates(tx *gorm.DB, productIDs []uuid.UUID) ([]CandidateRow, error) {
	var rows []CandidateRow
	err := tx.Raw(`
		SELECT l.id AS location_id, l.code AS location_code, l.type_code, l.sync_state,
		       b.product_id, b.available_qty, COALESCE(sp.online_buffer_qty, 0) AS online_buffer_qty
		  FROM locations l
		  JOIN inventory_balances b ON b.location_id = l.id
		  LEFT JOIN stock_policies sp ON sp.location_id = b.location_id AND sp.product_id = b.product_id
		 WHERE l.archived_at IS NULL
		   AND l.online_fulfillment_enabled
		   AND b.product_id IN ?
		 ORDER BY CASE l.type_code WHEN 'CENTRAL_WH' THEN 0 WHEN 'HUB' THEN 1 ELSE 2 END, l.code, l.id`,
		productIDs).Scan(&rows).Error
	return rows, err
}

// OnlinePolicy membaca status sinkron lokasi dan buffer online satu produk.
func (InventoryRepository) OnlinePolicy(tx *gorm.DB, locationID, productID uuid.UUID) (syncState string, buffer int64, err error) {
	var row struct {
		SyncState       string
		OnlineBufferQty int64
	}
	err = tx.Raw(`
		SELECT l.sync_state, COALESCE(sp.online_buffer_qty, 0) AS online_buffer_qty
		  FROM locations l
		  LEFT JOIN stock_policies sp ON sp.location_id = l.id AND sp.product_id = ?
		 WHERE l.id = ?`, productID, locationID).Scan(&row).Error
	return row.SyncState, row.OnlineBufferQty, err
}

// BalanceFilter adalah filter dan cursor keyset (location_id, product_id).
type BalanceFilter struct {
	LocationID *uuid.UUID
	ProductID  *uuid.UUID
	// ScopeAll=false membatasi ke LocationIDs (kosong = tidak ada hasil).
	ScopeAll        bool
	LocationIDs     []uuid.UUID
	AfterLocationID *uuid.UUID
	AfterProductID  *uuid.UUID
	Limit           int
}

// BalanceRow adalah saldo beserta identitas lokasi/produk untuk API.
type BalanceRow struct {
	LocationID      uuid.UUID
	LocationCode    string
	LocationName    string
	LocationType    string
	SyncState       string
	ProductID       uuid.UUID
	SKU             string `gorm:"column:sku"`
	ProductName     string
	PhysicalQty     int64
	ReservedQty     int64
	QuarantineQty   int64
	AvailableQty    int64
	OnlineBufferQty int64
	UpdatedAt       time.Time
}

// ListBalances membaca saldo dengan pagination keyset (location_id, product_id)
// memakai primary key. Satu query dengan join (tanpa N+1).
func (InventoryRepository) ListBalances(tx *gorm.DB, f BalanceFilter) ([]BalanceRow, error) {
	q := tx.Table("inventory_balances AS b").
		Select(`b.location_id, l.code AS location_code, l.name AS location_name, l.type_code AS location_type,
		        l.sync_state, b.product_id, p.sku, p.name AS product_name, b.physical_qty, b.reserved_qty,
		        b.quarantine_qty, b.available_qty, COALESCE(sp.online_buffer_qty, 0) AS online_buffer_qty, b.updated_at`).
		Joins("JOIN locations l ON l.id = b.location_id").
		Joins("JOIN products p ON p.id = b.product_id").
		Joins("LEFT JOIN stock_policies sp ON sp.location_id = b.location_id AND sp.product_id = b.product_id")
	if !f.ScopeAll {
		if len(f.LocationIDs) == 0 {
			return nil, nil
		}
		q = q.Where("b.location_id IN ?", f.LocationIDs)
	}
	if f.LocationID != nil {
		q = q.Where("b.location_id = ?", *f.LocationID)
	}
	if f.ProductID != nil {
		q = q.Where("b.product_id = ?", *f.ProductID)
	}
	if f.AfterLocationID != nil && f.AfterProductID != nil {
		q = q.Where("(b.location_id, b.product_id) > (?, ?)", *f.AfterLocationID, *f.AfterProductID)
	}
	var rows []BalanceRow
	err := q.Order("b.location_id, b.product_id").Limit(f.Limit).Scan(&rows).Error
	return rows, err
}

// LocationExists dan ProductExists memvalidasi referensi dari request.
func (InventoryRepository) LocationExists(tx *gorm.DB, id uuid.UUID) (bool, error) {
	var n int64
	err := tx.Raw(`SELECT count(*) FROM locations WHERE id = ? AND archived_at IS NULL`, id).Scan(&n).Error
	return n > 0, err
}

func (InventoryRepository) ProductExists(tx *gorm.DB, id uuid.UUID) (bool, error) {
	var n int64
	err := tx.Raw(`SELECT count(*) FROM products WHERE id = ? AND archived_at IS NULL`, id).Scan(&n).Error
	return n > 0, err
}

// InsertAudit menulis audit_logs (append-only).
func (InventoryRepository) InsertAudit(tx *gorm.DB, a *model.AuditLog) error {
	return tx.Create(a).Error
}

// PreemptCandidate adalah reservasi ACTIVE yang bisa dikalahkan fakta penjualan.
type PreemptCandidate struct {
	model.StockReservation
	OrderStatus string
}

// LockActiveReservationsAt mengunci (MENUNGGU) semua reservasi ACTIVE di
// (lokasi, produk), urut id. Dipanggil SEBELUM saldo dikunci, sesuai urutan
// lock global reservasi → saldo: transaksi batal/kedaluwarsa/fulfill yang
// sedang memegang reservasi itu ditunggu sampai selesai.
func (InventoryRepository) LockActiveReservationsAt(tx *gorm.DB, locationID, productID uuid.UUID) error {
	var ids []uuid.UUID
	return tx.Raw(`
		SELECT id FROM stock_reservations
		 WHERE location_id = ? AND product_id = ? AND status = 'ACTIVE'
		 ORDER BY id
		   FOR NO KEY UPDATE`, locationID, productID).Scan(&ids).Error
}

// LockPreemptCandidates mengunci reservasi ACTIVE di (lokasi, produk) dengan
// urutan prioritas preempt: order PENDING_PAYMENT terbaru dulu, lalu PAID
// terbaru, lalu status lain. SKIP LOCKED: dipanggil setelah saldo terkunci,
// jadi tidak boleh menunggu reservasi (pemegangnya mungkin menunggu saldo).
func (InventoryRepository) LockPreemptCandidates(tx *gorm.DB, locationID, productID uuid.UUID) ([]PreemptCandidate, error) {
	const lock = "FOR NO KEY UPDATE OF r SKIP LOCKED"
	var rows []PreemptCandidate
	err := tx.Raw(`
		SELECT r.*, o.status AS order_status
		  FROM stock_reservations r
		  JOIN orders o ON o.id = r.order_id
		 WHERE r.location_id = ? AND r.product_id = ? AND r.status = 'ACTIVE'
		 ORDER BY CASE o.status WHEN 'PENDING_PAYMENT' THEN 0 WHEN 'PAID' THEN 1 ELSE 2 END,
		          r.created_at DESC, r.id DESC
		`+lock, locationID, productID).Scan(&rows).Error
	return rows, err
}

// InsertConflict mencatat konflik alokasi (Kasus 4).
func (InventoryRepository) InsertConflict(tx *gorm.DB, c *model.AllocationConflict) error {
	return tx.Create(c).Error
}

// ResolveReservationConflicts menutup konflik OPEN milik satu reservasi.
func (InventoryRepository) ResolveReservationConflicts(tx *gorm.DB, reservationID uuid.UUID, resolution string, now time.Time) error {
	return tx.Exec(`
		UPDATE allocation_conflicts
		   SET status = 'AUTO_RESOLVED', resolution = ?, resolved_at = ?, updated_at = ?
		 WHERE reservation_id = ? AND status = 'OPEN'`, resolution, now, now, reservationID).Error
}

// UpsertAnomaly membuat anomali dari aturan yang aktif, atau menaikkan
// hit_count kalau dedupe_key sudah ada. Aturan yang dimatikan tidak menulis apa pun.
func (InventoryRepository) UpsertAnomaly(tx *gorm.DB, ruleCode, subjectType string, subjectID uuid.UUID,
	dedupeKey string, evidence model.JSON, now time.Time) error {
	return tx.Exec(`
		INSERT INTO anomalies (id, rule_code, severity, status, subject_type, subject_id, dedupe_key, hit_count,
		       evidence, first_seen_at, last_seen_at, created_at, updated_at)
		SELECT ?, ar.code, ar.severity, 'OPEN', ?, ?, ?, 1, ?, ?, ?, ?, ?
		  FROM anomaly_rules ar
		 WHERE ar.code = ? AND ar.enabled
		ON CONFLICT ON CONSTRAINT anomaly_dedupe_once DO UPDATE
		   SET hit_count    = anomalies.hit_count + 1,
		       last_seen_at = GREATEST(anomalies.last_seen_at, EXCLUDED.last_seen_at),
		       evidence     = EXCLUDED.evidence,
		       updated_at   = EXCLUDED.updated_at`,
		uuid.Must(uuid.NewV7()), subjectType, subjectID, dedupeKey, evidence, now, now, now, now, ruleCode).Error
}
