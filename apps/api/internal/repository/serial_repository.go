package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
)

// serialLocking = SELECT ... FOR UPDATE.
var serialLocking = clause.Locking{Strength: clause.LockingStrengthUpdate}

// SerialRepository mengakses serial_units, unit_events, lifecycle_transitions,
// code_batches, dan batches. Semua method menerima tx dari service; query
// jalur panas memakai SQL berparameter (conditional update, recursive CTE).
type SerialRepository struct {
	db *gorm.DB
}

func NewSerialRepository(db *gorm.DB) *SerialRepository { return &SerialRepository{db: db} }

// SerialUUIDArray mengubah daftar UUID menjadi parameter array Postgres. Dipakai
// dengan CAST(@x AS uuid[]); tipe Valuer mencegah GORM memecah slice
// menjadi daftar parameter.
func SerialUUIDArray(ids []uuid.UUID) model.StringArray {
	out := make(model.StringArray, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// serialJSONParam meng-encode payload event; nil menjadi objek kosong.
func serialJSONParam(v map[string]any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode payload event: %w", err)
	}
	return string(b), nil
}

// LoadLifecycle membaca transisi serial unit dari lifecycle_transitions.
func (r *SerialRepository) LoadLifecycle(ctx context.Context) ([]domain.LifecycleTransition, error) {
	var rows []model.LifecycleTransition
	if err := r.db.WithContext(ctx).
		Where("entity_type = ?", model.EntitySerialUnit).
		Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("membaca lifecycle_transitions: %w", err)
	}
	out := make([]domain.LifecycleTransition, len(rows))
	for i, t := range rows {
		out[i] = domain.LifecycleTransition{To: t.ToCode, BizStep: t.BizStep}
		if t.FromCode != nil {
			out[i].From = *t.FromCode
		}
		if t.Permission != nil {
			out[i].Permission = *t.Permission
		}
	}
	return out, nil
}

// SerialStatuses mengembalikan semua kode status serial.
func (r *SerialRepository) SerialStatuses(ctx context.Context) ([]model.SerialUnitStatus, error) {
	var rows []model.SerialUnitStatus
	err := r.db.WithContext(ctx).Order("sort_order").Find(&rows).Error
	return rows, err
}

// LockByCodes mengunci (FOR UPDATE) serial dengan kode di codes, urut id
// supaya urutan lock konsisten antar transaksi (tanpa deadlock).
func (r *SerialRepository) LockByCodes(tx *gorm.DB, codes []string) ([]model.SerialUnit, error) {
	var rows []model.SerialUnit
	err := tx.Raw(`SELECT * FROM serial_units WHERE code = ANY(CAST(? AS text[])) ORDER BY id FOR UPDATE`,
		model.StringArray(codes)).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("mengunci serial: %w", err)
	}
	return rows, nil
}

// FindByCode membaca satu serial tanpa lock. found=false kalau tidak ada.
func (r *SerialRepository) FindByCode(tx *gorm.DB, code string) (model.SerialUnit, bool, error) {
	var su model.SerialUnit
	err := tx.Where("code = ?", code).Take(&su).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return su, false, nil
	}
	if err != nil {
		return su, false, fmt.Errorf("membaca serial: %w", err)
	}
	return su, true, nil
}

// SerialTreeNode adalah satu serial di pohon kemasan (akar + semua turunannya).
type SerialTreeNode struct {
	ID         uuid.UUID
	Code       string
	LevelCode  string
	Status     string
	ParentID   *uuid.UUID
	ProductID  *uuid.UUID
	BatchID    *uuid.UUID
	LocationID *uuid.UUID
}

// LockTree mengunci akar dan seluruh turunannya (recursive CTE) dalam satu
// query, urut id.
func (r *SerialRepository) LockTree(tx *gorm.DB, roots []uuid.UUID) ([]SerialTreeNode, error) {
	var rows []SerialTreeNode
	err := tx.Raw(`
WITH RECURSIVE tree AS (
    SELECT id FROM serial_units WHERE id = ANY(CAST(? AS uuid[]))
  UNION
    SELECT c.id FROM serial_units c JOIN tree t ON c.parent_id = t.id
)
SELECT s.id, s.code, s.level_code, s.status, s.parent_id, s.product_id, s.batch_id, s.location_id
  FROM serial_units s
 WHERE s.id IN (SELECT id FROM tree)
 ORDER BY s.id
   FOR UPDATE`, SerialUUIDArray(roots)).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("mengunci pohon serial: %w", err)
	}
	return rows, nil
}

// UnitEventSpec mengisi baris unit_events yang ditulis bersama perubahan status.
type UnitEventSpec struct {
	BizStep   string
	ActorType string
	ActorID   *uuid.UUID
	DeviceID  *uuid.UUID
	RefType   string // kosong = NULL
	RefID     *uuid.UUID
	Payload   map[string]any
	At        time.Time
}

func (e UnitEventSpec) params(p map[string]any) error {
	payload, err := serialJSONParam(e.Payload)
	if err != nil {
		return err
	}
	var refType *string
	if e.RefType != "" {
		refType = &e.RefType
	}
	p["biz_step"] = e.BizStep
	p["actor_type"] = e.ActorType
	p["actor_id"] = e.ActorID
	p["device_id"] = e.DeviceID
	p["ref_type"] = refType
	p["ref_id"] = e.RefID
	p["payload"] = payload
	p["at"] = e.At
	return nil
}

// unitEventColumns + eventValues: kolom unit_events yang sama di semua INSERT ... SELECT.
const unitEventColumns = `id, serial_unit_id, biz_step, from_status, to_status, parent_id, location_id, party_id,
       actor_type, actor_id, device_id, ref_type, ref_id, payload, occurred_at`

const unitEventActorValues = `CAST(@actor_type AS text), CAST(@actor_id AS uuid), CAST(@device_id AS uuid),
       CAST(@ref_type AS text), CAST(@ref_id AS uuid), CAST(@payload AS jsonb), CAST(@at AS timestamptz)`

// SerialTransitionSpec adalah satu perubahan status massal.
type SerialTransitionSpec struct {
	// Roots: serial yang diubah. Kalau Recursive, seluruh turunannya ikut.
	Roots     []uuid.UUID
	Recursive bool
	// BatchID: alternatif Roots, semua serial di batch produksi ini (recall).
	BatchID *uuid.UUID

	AllowedFrom []string
	To          string
	// RequireLocation: hanya serial yang sedang berada di lokasi ini.
	RequireLocation *uuid.UUID

	SetLocation     *uuid.UUID
	SetCustodian    *uuid.UUID
	SetProduct      *uuid.UUID
	SetBatch        *uuid.UUID
	SetCommissioned bool
	DetachParent    bool

	Event UnitEventSpec
}

// BulkTransition menjalankan perubahan status + kolom penyerta untuk semua
// serial terpilih DAN menulis satu unit_events per serial, dalam SATU
// statement (recursive CTE → UPDATE ... FROM → INSERT ... SELECT). Hanya baris
// yang statusnya ada di AllowedFrom yang berubah (conditional update); jumlah
// yang dikembalikan = baris yang berubah = event yang tertulis.
func (r *SerialRepository) BulkTransition(tx *gorm.DB, s SerialTransitionSpec) (int64, error) {
	if (len(s.Roots) == 0) == (s.BatchID == nil) {
		return 0, errors.New("BulkTransition: isi tepat salah satu dari Roots atau BatchID")
	}
	if len(s.AllowedFrom) == 0 || s.To == "" {
		return 0, errors.New("BulkTransition: AllowedFrom dan To wajib diisi")
	}
	p := map[string]any{
		"allowed": model.StringArray(s.AllowedFrom),
		"to":      s.To,
	}
	if err := s.Event.params(p); err != nil {
		return 0, err
	}

	var b strings.Builder
	b.WriteString("WITH RECURSIVE tree AS (\n")
	if s.BatchID != nil {
		p["batch"] = *s.BatchID
		b.WriteString("    SELECT id FROM serial_units WHERE batch_id = CAST(@batch AS uuid)\n")
	} else {
		p["roots"] = SerialUUIDArray(s.Roots)
		b.WriteString("    SELECT id FROM serial_units WHERE id = ANY(CAST(@roots AS uuid[]))\n")
		if s.Recursive {
			b.WriteString("  UNION\n    SELECT c.id FROM serial_units c JOIN tree t ON c.parent_id = t.id\n")
		}
	}
	b.WriteString(`), old AS (
    SELECT s.id, s.status, s.parent_id
      FROM serial_units s JOIN tree ON tree.id = s.id
     WHERE s.status = ANY(CAST(@allowed AS text[]))
`)
	if s.RequireLocation != nil {
		p["require_location"] = *s.RequireLocation
		b.WriteString("       AND s.location_id = CAST(@require_location AS uuid)\n")
	}
	b.WriteString(`       FOR UPDATE OF s
), upd AS (
    UPDATE serial_units s
       SET status = CAST(@to AS text), version = s.version + 1, updated_at = CAST(@at AS timestamptz)`)
	if s.SetLocation != nil {
		p["set_location"] = *s.SetLocation
		b.WriteString(",\n           location_id = CAST(@set_location AS uuid)")
	}
	if s.SetCustodian != nil {
		p["set_custodian"] = *s.SetCustodian
		b.WriteString(",\n           custodian_party_id = CAST(@set_custodian AS uuid)")
	}
	if s.SetProduct != nil {
		p["set_product"] = *s.SetProduct
		b.WriteString(",\n           product_id = CAST(@set_product AS uuid)")
	}
	if s.SetBatch != nil {
		p["set_batch"] = *s.SetBatch
		b.WriteString(",\n           batch_id = CAST(@set_batch AS uuid)")
	}
	if s.SetCommissioned {
		b.WriteString(",\n           commissioned_at = CAST(@at AS timestamptz)")
	}
	if s.DetachParent {
		b.WriteString(",\n           parent_id = NULL")
	}
	b.WriteString(`
      FROM old
     WHERE s.id = old.id
 RETURNING s.id, old.status AS from_status, old.parent_id AS old_parent_id, s.location_id, s.custodian_party_id
)
INSERT INTO unit_events (` + unitEventColumns + `)
SELECT gen_random_uuid(), upd.id, CAST(@biz_step AS text), upd.from_status, CAST(@to AS text),
       upd.old_parent_id, upd.location_id, upd.custodian_party_id,
       ` + unitEventActorValues + `
  FROM upd`)

	res := tx.Exec(b.String(), p)
	if res.Error != nil {
		return 0, fmt.Errorf("transisi massal serial ke %s: %w", s.To, res.Error)
	}
	return res.RowsAffected, nil
}

// MintRow adalah satu kode baru hasil mint.
type MintRow struct {
	ID   uuid.UUID
	Code string
	// HashHex: hex claim_secret_hash (kosong untuk level selain UNIT).
	HashHex string
}

// InsertMinted menyisipkan serial GENERATED + event encoding untuk setiap
// baris yang berhasil masuk. Kode yang bertabrakan (sangat jarang, 80-bit)
// dilewati lewat ON CONFLICT; id yang benar-benar masuk dikembalikan supaya
// service bisa membuat ulang sisanya.
func (r *SerialRepository) InsertMinted(tx *gorm.DB, codeBatchID uuid.UUID, level string, rows []MintRow, ev UnitEventSpec) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, len(rows))
	codes := make(model.StringArray, len(rows))
	hashes := make(model.StringArray, len(rows))
	for i, row := range rows {
		ids[i], codes[i], hashes[i] = row.ID, row.Code, row.HashHex
	}
	p := map[string]any{
		"ids": SerialUUIDArray(ids), "codes": codes, "hashes": hashes,
		"level": level, "code_batch": codeBatchID, "to": domain.SerialGenerated,
	}
	if err := ev.params(p); err != nil {
		return nil, err
	}
	var inserted []uuid.UUID
	err := tx.Raw(`
WITH ins AS (
    INSERT INTO serial_units (id, code, level_code, code_batch_id, status, claim_secret_hash, created_at, updated_at)
    SELECT u.id, u.code, CAST(@level AS text), CAST(@code_batch AS uuid), CAST(@to AS text),
           CASE WHEN u.hash = '' THEN NULL ELSE decode(u.hash, 'hex') END,
           CAST(@at AS timestamptz), CAST(@at AS timestamptz)
      FROM unnest(CAST(@ids AS uuid[]), CAST(@codes AS text[]), CAST(@hashes AS text[])) AS u (id, code, hash)
    ON CONFLICT ON CONSTRAINT serial_code_unique DO NOTHING
    RETURNING id
), ev AS (
    INSERT INTO unit_events (`+unitEventColumns+`)
    SELECT gen_random_uuid(), ins.id, CAST(@biz_step AS text), NULL, CAST(@to AS text), NULL, NULL, NULL,
           `+unitEventActorValues+`
      FROM ins
)
SELECT id FROM ins`, p).Scan(&inserted).Error
	if err != nil {
		return nil, fmt.Errorf("menyisipkan kode hasil mint: %w", err)
	}
	return inserted, nil
}

// SetParent memasukkan anak ke induk (hanya anak yang belum punya induk) dan
// menulis event packing per anak dalam satu statement.
func (r *SerialRepository) SetParent(tx *gorm.DB, parentID uuid.UUID, childIDs []uuid.UUID, ev UnitEventSpec) (int64, error) {
	p := map[string]any{"parent": parentID, "children": SerialUUIDArray(childIDs)}
	if err := ev.params(p); err != nil {
		return 0, err
	}
	res := tx.Exec(`
WITH upd AS (
    UPDATE serial_units s
       SET parent_id = CAST(@parent AS uuid), version = s.version + 1, updated_at = CAST(@at AS timestamptz)
     WHERE s.id = ANY(CAST(@children AS uuid[])) AND s.parent_id IS NULL
 RETURNING s.id, s.status, s.location_id, s.custodian_party_id
)
INSERT INTO unit_events (`+unitEventColumns+`)
SELECT gen_random_uuid(), upd.id, CAST(@biz_step AS text), NULL, NULL, CAST(@parent AS uuid),
       upd.location_id, upd.custodian_party_id,
       `+unitEventActorValues+`
  FROM upd`, p)
	if res.Error != nil {
		return 0, fmt.Errorf("memasukkan anak ke induk: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// DetachChildren mengeluarkan semua anak langsung dari induk dan menulis event
// unpacking per anak (parent_id event = induk lama).
func (r *SerialRepository) DetachChildren(tx *gorm.DB, parentID uuid.UUID, ev UnitEventSpec) (int64, error) {
	p := map[string]any{"parent": parentID}
	if err := ev.params(p); err != nil {
		return 0, err
	}
	res := tx.Exec(`
WITH upd AS (
    UPDATE serial_units s
       SET parent_id = NULL, version = s.version + 1, updated_at = CAST(@at AS timestamptz)
     WHERE s.parent_id = CAST(@parent AS uuid)
 RETURNING s.id, s.location_id, s.custodian_party_id
)
INSERT INTO unit_events (`+unitEventColumns+`)
SELECT gen_random_uuid(), upd.id, CAST(@biz_step AS text), NULL, NULL, CAST(@parent AS uuid),
       upd.location_id, upd.custodian_party_id,
       `+unitEventActorValues+`
  FROM upd`, p)
	if res.Error != nil {
		return 0, fmt.Errorf("membongkar induk: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// InsertEvent menulis event yang tidak mengubah status (misal klaim ulang).
func (r *SerialRepository) InsertEvent(tx *gorm.DB, ev model.UnitEvent) error {
	if err := tx.Create(&ev).Error; err != nil {
		return fmt.Errorf("menulis unit_events: %w", err)
	}
	return nil
}

// ChildCount menghitung anak langsung induk.
func (r *SerialRepository) ChildCount(tx *gorm.DB, parentID uuid.UUID) (int64, error) {
	var n int64
	err := tx.Model(&model.SerialUnit{}).Where("parent_id = ?", parentID).Count(&n).Error
	return n, err
}

// ChildBatches mengembalikan batch_id berbeda dari anak langsung induk.
func (r *SerialRepository) ChildBatches(tx *gorm.DB, parentID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := tx.Model(&model.SerialUnit{}).Where("parent_id = ? AND batch_id IS NOT NULL", parentID).
		Distinct().Pluck("batch_id", &ids).Error
	return ids, err
}

// AncestorIDs mengembalikan semua leluhur serial (induk, induk dari induk, ...).
func (r *SerialRepository) AncestorIDs(tx *gorm.DB, id uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := tx.Raw(`
WITH RECURSIVE up AS (
    SELECT parent_id AS id FROM serial_units WHERE id = ? AND parent_id IS NOT NULL
  UNION
    SELECT s.parent_id FROM serial_units s JOIN up ON s.id = up.id WHERE s.parent_id IS NOT NULL
)
SELECT id FROM up`, id).Scan(&ids).Error
	return ids, err
}

// LevelRanks membaca rank tiap level kemasan.
func (r *SerialRepository) LevelRanks(tx *gorm.DB) (map[string]int, error) {
	var rows []model.PackagingLevel
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("membaca packaging_levels: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, l := range rows {
		out[l.Code] = int(l.Rank)
	}
	return out, nil
}

// PackagingSpec membaca spesifikasi kemasan produk untuk level anak.
func (r *SerialRepository) PackagingSpec(tx *gorm.DB, productID uuid.UUID, childLevel string) (model.PackagingSpec, bool, error) {
	var spec model.PackagingSpec
	err := tx.Where("product_id = ? AND child_level = ?", productID, childLevel).Take(&spec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return spec, false, nil
	}
	return spec, err == nil, err
}

// ---------------------------------------------------------------- master data

// CreateCodeBatch menyimpan job cetak.
func (r *SerialRepository) CreateCodeBatch(tx *gorm.DB, cb *model.CodeBatch) error {
	return tx.Create(cb).Error
}

// FindCodeBatches membaca code batch berdasarkan id.
func (r *SerialRepository) FindCodeBatches(tx *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]model.CodeBatch, error) {
	var rows []model.CodeBatch
	if len(ids) == 0 {
		return map[uuid.UUID]model.CodeBatch{}, nil
	}
	if err := tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]model.CodeBatch, len(rows))
	for _, cb := range rows {
		out[cb.ID] = cb
	}
	return out, nil
}

// FindBatch membaca batch produksi; lock=true menguncinya FOR UPDATE.
func (r *SerialRepository) FindBatch(tx *gorm.DB, id uuid.UUID, lock bool) (model.Batch, bool, error) {
	var b model.Batch
	q := tx
	if lock {
		q = q.Clauses(serialLocking)
	}
	err := q.Where("id = ?", id).Take(&b).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return b, false, nil
	}
	return b, err == nil, err
}

// MarkBatchRecalled mengubah batch ke RECALLED (conditional, idempotent).
func (r *SerialRepository) MarkBatchRecalled(tx *gorm.DB, id uuid.UUID, reason string, at time.Time) (bool, error) {
	res := tx.Model(&model.Batch{}).
		Where("id = ? AND status <> 'RECALLED'", id).
		Updates(map[string]any{
			"status": "RECALLED", "recalled_at": at, "recall_reason": reason,
			"version": gorm.Expr("version + 1"), "updated_at": at,
		})
	return res.RowsAffected == 1, res.Error
}

// ProductExists memeriksa produk aktif.
func (r *SerialRepository) ProductExists(tx *gorm.DB, id uuid.UUID) (bool, error) {
	var n int64
	err := tx.Model(&model.Product{}).Where("id = ? AND archived_at IS NULL", id).Count(&n).Error
	return n > 0, err
}

// LevelExists memeriksa kode level kemasan.
func (r *SerialRepository) LevelExists(tx *gorm.DB, code string) (bool, error) {
	var n int64
	err := tx.Model(&model.PackagingLevel{}).Where("code = ?", code).Count(&n).Error
	return n > 0, err
}

// SerialLocation adalah lokasi beserta atribut tipenya.
type SerialLocation struct {
	ID        uuid.UUID
	Code      string
	Name      string
	TypeCode  string
	PartyID   *uuid.UUID
	IsOwn     bool
	IsVirtual bool
}

// FindLocation membaca lokasi aktif beserta atribut tipenya.
func (r *SerialRepository) FindLocation(tx *gorm.DB, id uuid.UUID) (SerialLocation, bool, error) {
	var rows []SerialLocation
	err := tx.Raw(`
SELECT l.id, l.code, l.name, l.type_code, l.party_id, t.is_own, t.is_virtual
  FROM locations l JOIN location_types t ON t.code = l.type_code
 WHERE l.id = ? AND l.archived_at IS NULL`, id).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return SerialLocation{}, false, err
	}
	return rows[0], true, nil
}

// AnyLocationOfType melaporkan apakah salah satu lokasi ids bertipe typeCode.
func (r *SerialRepository) AnyLocationOfType(tx *gorm.DB, ids []uuid.UUID, typeCode string) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	var n int64
	err := tx.Model(&model.Location{}).
		Where("id IN ? AND type_code = ? AND archived_at IS NULL", ids, typeCode).Count(&n).Error
	return n > 0, err
}

// DetachForSale melepas unit dari kemasan induknya (parent_id NULL) dan
// menulis event unpacking per unit (parent_id event = induk lama). Dipakai
// SellSerials di transaksi yang sama dengan transisi SOLD.
func (r *SerialRepository) DetachForSale(tx *gorm.DB, ids []uuid.UUID, ev UnitEventSpec) (int64, error) {
	p := map[string]any{"ids": SerialUUIDArray(ids)}
	if err := ev.params(p); err != nil {
		return 0, err
	}
	res := tx.Exec(`
WITH old AS (
    SELECT id, parent_id FROM serial_units
     WHERE id = ANY(CAST(@ids AS uuid[])) AND parent_id IS NOT NULL
       FOR UPDATE
), upd AS (
    UPDATE serial_units s
       SET parent_id = NULL, version = s.version + 1, updated_at = CAST(@at AS timestamptz)
      FROM old
     WHERE s.id = old.id
 RETURNING s.id, old.parent_id AS old_parent_id, s.location_id, s.custodian_party_id
)
INSERT INTO unit_events (`+unitEventColumns+`)
SELECT gen_random_uuid(), upd.id, CAST(@biz_step AS text), NULL, NULL, upd.old_parent_id,
       upd.location_id, upd.custodian_party_id,
       `+unitEventActorValues+`
  FROM upd`, p)
	if res.Error != nil {
		return 0, fmt.Errorf("melepas unit dari kemasan: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// PartyExists memeriksa party aktif.
func (r *SerialRepository) PartyExists(tx *gorm.DB, id uuid.UUID) (bool, error) {
	var n int64
	err := tx.Model(&model.Party{}).Where("id = ? AND archived_at IS NULL", id).Count(&n).Error
	return n > 0, err
}

// InsertAudit menulis audit log aksi manusia yang sensitif.
func (r *SerialRepository) InsertAudit(tx *gorm.DB, a model.AuditLog) error {
	if err := tx.Create(&a).Error; err != nil {
		return fmt.Errorf("menulis audit_logs: %w", err)
	}
	return nil
}
