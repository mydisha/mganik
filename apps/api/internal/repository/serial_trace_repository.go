package repository

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// TraceRepository membaca genealogi serial untuk investigasi (Kasus 1). Setiap
// bagian satu query dengan JOIN, tanpa N+1.
type TraceRepository struct{}

func NewTraceRepository() *TraceRepository { return &TraceRepository{} }

// TraceUnit adalah ringkasan serial beserta produk, batch, lokasi, custodian.
type TraceUnit struct {
	ID                      uuid.UUID
	Code                    string
	LevelCode               string
	Status                  string
	VerifyCount             int
	FirstVerifiedAt         *time.Time
	FirstVerifiedRegionName *string
	CommissionedAt          *time.Time
	ProductID               *uuid.UUID
	ProductSKU              *string
	ProductName             *string
	BatchID                 *uuid.UUID
	LotNo                   *string
	BatchStatus             *string
	ExpDate                 *time.Time
	LocationID              *uuid.UUID
	LocationCode            *string
	LocationName            *string
	CustodianPartyID        *uuid.UUID
	CustodianName           *string
	ParentID                *uuid.UUID
}

// Unit membaca ringkasan serial berdasarkan kode. found=false kalau tidak ada.
func (r *TraceRepository) Unit(tx *gorm.DB, code string) (TraceUnit, bool, error) {
	var rows []TraceUnit
	err := tx.Raw(`
SELECT s.id, s.code, s.level_code, s.status, s.verify_count, s.first_verified_at,
       fr.name AS first_verified_region_name, s.commissioned_at,
       p.id AS product_id, p.sku AS product_sku, p.name AS product_name,
       b.id AS batch_id, b.lot_no, b.status AS batch_status, b.exp_date,
       l.id AS location_id, l.code AS location_code, l.name AS location_name,
       pa.id AS custodian_party_id, pa.name AS custodian_name, s.parent_id
  FROM serial_units s
  LEFT JOIN products p ON p.id = s.product_id
  LEFT JOIN batches b ON b.id = s.batch_id
  LEFT JOIN locations l ON l.id = s.location_id
  LEFT JOIN parties pa ON pa.id = s.custodian_party_id
  LEFT JOIN regions fr ON fr.code = s.first_verified_region_code
 WHERE s.code = ?`, code).Scan(&rows).Error
	if err != nil {
		return TraceUnit{}, false, fmt.Errorf("membaca serial untuk trace: %w", err)
	}
	if len(rows) == 0 {
		return TraceUnit{}, false, nil
	}
	return rows[0], true, nil
}

// TraceNode adalah leluhur serial; Depth 1 = induk langsung.
type TraceNode struct {
	ID        uuid.UUID
	Code      string
	LevelCode string
	Status    string
	Depth     int
}

// Ancestors membaca semua leluhur (induk ke atas) dalam satu recursive CTE.
func (r *TraceRepository) Ancestors(tx *gorm.DB, id uuid.UUID) ([]TraceNode, error) {
	var rows []TraceNode
	err := tx.Raw(`
WITH RECURSIVE up (id, depth) AS (
    SELECT parent_id, 1 FROM serial_units WHERE id = ? AND parent_id IS NOT NULL
  UNION ALL
    SELECT s.parent_id, up.depth + 1 FROM serial_units s JOIN up ON s.id = up.id
     WHERE s.parent_id IS NOT NULL AND up.depth < 16
)
SELECT s.id, s.code, s.level_code, s.status, up.depth
  FROM up JOIN serial_units s ON s.id = up.id
 ORDER BY up.depth`, id).Scan(&rows).Error
	return rows, err
}

// DescendantCount adalah jumlah turunan per (level, status).
type DescendantCount struct {
	LevelCode string
	Status    string
	Count     int64
}

// Descendants merangkum seluruh turunan serial per level dan status.
func (r *TraceRepository) Descendants(tx *gorm.DB, id uuid.UUID) ([]DescendantCount, error) {
	var rows []DescendantCount
	err := tx.Raw(`
WITH RECURSIVE down AS (
    SELECT id FROM serial_units WHERE parent_id = ?
  UNION
    SELECT c.id FROM serial_units c JOIN down d ON c.parent_id = d.id
)
SELECT s.level_code, s.status, count(*) AS count
  FROM down JOIN serial_units s ON s.id = down.id
 GROUP BY s.level_code, s.status
 ORDER BY s.level_code, s.status`, id).Scan(&rows).Error
	return rows, err
}

// TraceEvent adalah satu baris unit_events dengan kode lokasi, nama party, dan
// kode induk yang sudah di-join.
type TraceEvent struct {
	ID           uuid.UUID
	BizStep      string
	FromStatus   *string
	ToStatus     *string
	ParentCode   *string
	LocationCode *string
	PartyName    *string
	ActorType    string
	RefType      *string
	RefID        *uuid.UUID
	OccurredAt   time.Time
}

// Events membaca riwayat event serial (paling lama dulu), maksimal limit.
func (r *TraceRepository) Events(tx *gorm.DB, id uuid.UUID, limit int) ([]TraceEvent, error) {
	var rows []TraceEvent
	err := tx.Raw(`
SELECT e.id, e.biz_step, e.from_status, e.to_status, ps.code AS parent_code,
       l.code AS location_code, pa.name AS party_name, e.actor_type, e.ref_type, e.ref_id, e.occurred_at
  FROM unit_events e
  LEFT JOIN serial_units ps ON ps.id = e.parent_id
  LEFT JOIN locations l ON l.id = e.location_id
  LEFT JOIN parties pa ON pa.id = e.party_id
 WHERE e.serial_unit_id = ?
 ORDER BY e.occurred_at, e.recorded_at, e.id
 LIMIT ?`, id, limit).Scan(&rows).Error
	return rows, err
}

// TraceShipment adalah shipment yang membawa serial ini (langsung atau lewat
// karton/palet leluhurnya).
type TraceShipment struct {
	ID               uuid.UUID
	Code             string
	Status           string
	ProgramCode      *string
	FromLocationCode string
	ToLocationCode   string
	ToPartyName      *string
	ViaCode          string
	ShippedAt        *time.Time
	ReceivedAt       *time.Time
}

// Shipments membaca shipment dari RIWAYAT: event unit_events ber-ref_type
// SHIPMENT milik serial dan leluhur historisnya (karton/palet yang pernah
// membungkusnya, dirunut dari event packing). Bukti "unit X ← karton C ←
// palet P ← shipment SH" tetap ada setelah karton/palet dibongkar. via_code =
// serial yang tercatat di shipment_items (palet/karton/unit itu sendiri).
func (r *TraceRepository) Shipments(tx *gorm.DB, unitID uuid.UUID, unitCode string) ([]TraceShipment, error) {
	var rows []TraceShipment
	err := tx.Raw(`
WITH RECURSIVE anc (id) AS (
    SELECT CAST(@unit AS uuid)
  UNION
    SELECT e.parent_id FROM unit_events e JOIN anc ON e.serial_unit_id = anc.id
     WHERE e.biz_step = 'packing' AND e.parent_id IS NOT NULL
), ships AS (
    SELECT DISTINCT ue.ref_id AS id
      FROM unit_events ue JOIN anc ON ue.serial_unit_id = anc.id
     WHERE ue.ref_type = 'SHIPMENT' AND ue.ref_id IS NOT NULL
)
SELECT sh.id, sh.code, sh.status, sh.program_code, lf.code AS from_location_code,
       lt.code AS to_location_code, pa.name AS to_party_name,
       COALESCE((SELECT su.code FROM shipment_items si JOIN serial_units su ON su.id = si.serial_unit_id
                  WHERE si.shipment_id = sh.id AND si.serial_unit_id IN (SELECT id FROM anc)
                  ORDER BY su.code LIMIT 1), CAST(@code AS text)) AS via_code,
       sh.shipped_at, sh.received_at
  FROM ships
  JOIN shipments sh ON sh.id = ships.id
  JOIN locations lf ON lf.id = sh.from_location_id
  JOIN locations lt ON lt.id = sh.to_location_id
  LEFT JOIN parties pa ON pa.id = sh.to_party_id
 ORDER BY sh.shipped_at NULLS LAST, sh.id`, map[string]any{"unit": unitID, "code": unitCode}).Scan(&rows).Error
	return rows, err
}

// TraceScan adalah scan terakhir pada serial (tanpa fingerprint/IP).
type TraceScan struct {
	ID         uuid.UUID
	Context    string
	Result     string
	GeoSource  string
	RegionCode *string
	RegionName *string
	RuleHits   model.StringArray
	OccurredAt time.Time
}

// Scans membaca scan terbaru pada serial, maksimal limit.
func (r *TraceRepository) Scans(tx *gorm.DB, id uuid.UUID, limit int) ([]TraceScan, error) {
	var rows []TraceScan
	err := tx.Raw(`
SELECT se.id, se.context, se.result, se.geo_source, se.region_code, rg.name AS region_name,
       se.rule_hits, se.occurred_at
  FROM scan_events se
  LEFT JOIN regions rg ON rg.code = se.region_code
 WHERE se.serial_unit_id = ?
 ORDER BY se.occurred_at DESC
 LIMIT ?`, id, limit).Scan(&rows).Error
	return rows, err
}
