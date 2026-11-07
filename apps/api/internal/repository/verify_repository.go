package repository

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
)

// VerifyRepository mengakses scan_events, anomaly_rules, anomalies, regions,
// dan wilayah distributor untuk verifikasi publik dan radar.
type VerifyRepository struct{}

func NewVerifyRepository() *VerifyRepository { return &VerifyRepository{} }

// VerifyUnit adalah serial beserta atribut yang dibutuhkan aturan radar.
type VerifyUnit struct {
	ID               uuid.UUID
	Code             string
	LevelCode        string
	Status           string
	ProductName      *string
	BatchID          *uuid.UUID
	BatchStatus      *string
	LotNo            *string
	ExpDate          *time.Time
	LocationTypeCode *string
	CustodianPartyID *uuid.UUID
	CodeBatchID      *uuid.UUID
}

// FindForVerify membaca serial berdasarkan kode beserta produk, batch, dan
// tipe lokasi dalam satu query, lalu mengunci baris serial (FOR UPDATE OF s)
// supaya scan bersamaan untuk serial yang sama dievaluasi berurutan (scan
// kedua melihat scan pertama untuk impossible travel/velocity). found=false
// kalau kode tidak terdaftar.
func (r *VerifyRepository) FindForVerify(tx *gorm.DB, code string) (VerifyUnit, bool, error) {
	var rows []VerifyUnit
	err := tx.Raw(`
SELECT s.id, s.code, s.level_code, s.status, p.name AS product_name,
       s.batch_id, b.status AS batch_status, b.lot_no, b.exp_date,
       l.type_code AS location_type_code, s.custodian_party_id, s.code_batch_id
  FROM serial_units s
  LEFT JOIN products p ON p.id = s.product_id
  LEFT JOIN batches b ON b.id = s.batch_id
  LEFT JOIN locations l ON l.id = s.location_id
 WHERE s.code = ?
   FOR UPDATE OF s`, code).Scan(&rows).Error
	if err != nil {
		return VerifyUnit{}, false, fmt.Errorf("membaca serial untuk verifikasi: %w", err)
	}
	if len(rows) == 0 {
		return VerifyUnit{}, false, nil
	}
	return rows[0], true, nil
}

// Rules membaca aturan aktif satu kategori, dikunci per kode.
func (r *VerifyRepository) Rules(tx *gorm.DB, category string) (map[string]model.AnomalyRule, error) {
	var rows []model.AnomalyRule
	if err := tx.Where("category = ? AND enabled", category).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("membaca anomaly_rules: %w", err)
	}
	out := make(map[string]model.AnomalyRule, len(rows))
	for _, rule := range rows {
		out[rule.Code] = rule
	}
	return out, nil
}

// LastGeoScan mengembalikan scan terakhir serial yang punya koordinat.
func (r *VerifyRepository) LastGeoScan(tx *gorm.DB, serialID uuid.UUID) (*model.ScanEvent, error) {
	var rows []model.ScanEvent
	err := tx.Where("serial_unit_id = ? AND geo_source <> ?", serialID, domain.GeoNone).
		Order("occurred_at DESC").Limit(1).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("membaca scan terakhir: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// DistinctFingerprints menghitung fingerprint berbeda yang memindai serial
// sejak since, termasuk fingerprint scan yang sedang diproses (current).
// Catatan: parameter []byte tidak boleh tepat setelah "(" (GORM memecah slice
// non-Valuer di posisi itu menjadi daftar parameter), jadi pakai ?::bytea.
func (r *VerifyRepository) DistinctFingerprints(tx *gorm.DB, serialID uuid.UUID, since time.Time, current []byte) (int, error) {
	var n int
	err := tx.Raw(`
SELECT count(*) FROM (
    SELECT actor_fingerprint FROM scan_events
     WHERE serial_unit_id = ? AND occurred_at >= ? AND actor_fingerprint IS NOT NULL
  UNION
    SELECT ?::bytea
) f`, serialID, since, current).Scan(&n).Error
	return n, err
}

// DistinctIPHashes menghitung hash prefix IP berbeda yang memindai serial
// sejak since, termasuk scan yang sedang diproses (current boleh nil).
func (r *VerifyRepository) DistinctIPHashes(tx *gorm.DB, serialID uuid.UUID, since time.Time, current []byte) (int, error) {
	var n int
	err := tx.Raw(`
SELECT count(*) FROM (
    SELECT ip_hash FROM scan_events
     WHERE serial_unit_id = ? AND occurred_at >= ? AND ip_hash IS NOT NULL
  UNION
    SELECT h FROM (SELECT ?::bytea AS h) c WHERE h IS NOT NULL
) f`, serialID, since, current).Scan(&n).Error
	return n, err
}

// EverShipped melaporkan apakah serial pernah ikut shipment (event shipping).
func (r *VerifyRepository) EverShipped(tx *gorm.DB, serialID uuid.UUID) (bool, error) {
	var ok bool
	err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM unit_events WHERE serial_unit_id = ? AND biz_step = 'shipping')`,
		serialID).Scan(&ok).Error
	return ok, err
}

// RegionHit adalah wilayah hasil resolusi titik ke centroid terdekat.
type RegionHit struct {
	Code       string
	Name       string
	ParentCode *string
	DistKm     float64
}

// ResolveRegion mencari wilayah kota/kabupaten (bukan provinsi) dengan
// centroid terdekat dalam maxKm. nil kalau tidak ada.
func (r *VerifyRepository) ResolveRegion(tx *gorm.DB, p domain.GeoPoint, maxKm float64) (*RegionHit, error) {
	var rows []RegionHit
	err := tx.Raw(`
SELECT code, name, parent_code, dist_km FROM (
    SELECT code, name, parent_code,
           2 * 6371.0088 * asin(least(1, sqrt(
               power(sin(radians(centroid_lat - CAST(@lat AS float8)) / 2), 2)
             + cos(radians(CAST(@lat AS float8))) * cos(radians(centroid_lat))
             * power(sin(radians(centroid_lng - CAST(@lng AS float8)) / 2), 2)))) AS dist_km
      FROM regions
     WHERE centroid_lat IS NOT NULL AND centroid_lng IS NOT NULL AND level <> 'PROVINCE'
) r
 WHERE dist_km <= CAST(@max AS float8)
 ORDER BY dist_km
 LIMIT 1`, map[string]any{"lat": p.Lat, "lng": p.Lng, "max": maxKm}).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("resolusi wilayah: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// TerritoryCoverage adalah cakupan wilayah sebuah party pada satu waktu.
type TerritoryCoverage struct {
	// Known=false: party tidak punya baris partner_territories yang berlaku;
	// cakupan tidak diketahui, aturan luar wilayah tidak dijalankan.
	Known bool
	// Covered: wilayah scan termasuk cakupan (NATIONAL, atau SELECTED dan
	// wilayah/induknya ada di territory_regions).
	Covered bool
}

// CoverageFor membaca cakupan party untuk region (dan induknya) pada waktu at.
// Mode dibaca dari kolom coverage_mode, tidak pernah disimpulkan dari
// territory_regions yang kosong.
func (r *VerifyRepository) CoverageFor(tx *gorm.DB, partyID uuid.UUID, region string, parent *string, at time.Time) (TerritoryCoverage, error) {
	var rows []struct {
		CoverageMode string
		InTerritory  bool
	}
	err := tx.Raw(`
SELECT pt.coverage_mode,
       (pt.coverage_mode = 'SELECTED' AND EXISTS (
            SELECT 1 FROM territory_regions tr
             WHERE tr.territory_id = pt.territory_id
               AND (tr.region_code = CAST(@region AS text) OR tr.region_code = CAST(@parent AS text)))) AS in_territory
  FROM partner_territories pt
 WHERE pt.party_id = CAST(@party AS uuid)
   AND pt.valid_from <= CAST(@at AS timestamptz)
   AND (pt.valid_to IS NULL OR pt.valid_to > CAST(@at AS timestamptz))`,
		map[string]any{"party": partyID, "region": region, "parent": parent, "at": at}).Scan(&rows).Error
	if err != nil {
		return TerritoryCoverage{}, fmt.Errorf("membaca cakupan wilayah: %w", err)
	}
	cov := TerritoryCoverage{Known: len(rows) > 0}
	for _, row := range rows {
		if row.CoverageMode == model.CoverageNational || row.InTerritory {
			cov.Covered = true
		}
	}
	return cov, nil
}

// InsertScan menulis scan_events (append-only).
func (r *VerifyRepository) InsertScan(tx *gorm.DB, s *model.ScanEvent) error {
	if err := tx.Create(s).Error; err != nil {
		return fmt.Errorf("menulis scan_events: %w", err)
	}
	return nil
}

// RadarAnomalyHit adalah satu kejadian anomali yang di-upsert lewat dedupe_key.
type RadarAnomalyHit struct {
	RuleCode    string
	Severity    string
	SubjectType string
	SubjectID   *uuid.UUID
	PartyID     *uuid.UUID
	RegionCode  *string
	DedupeKey   string
	Evidence    map[string]any
	At          time.Time
}

// UpsertAnomaly membuat anomali baru atau, kalau dedupe_key sudah ada,
// menaikkan hit_count dan last_seen_at. Bukti pertama dipertahankan; bukti
// terbaru disimpan di evidence.last.
func (r *VerifyRepository) UpsertAnomaly(tx *gorm.DB, h RadarAnomalyHit) error {
	ev, err := json.Marshal(h.Evidence)
	if err != nil {
		return fmt.Errorf("encode evidence anomali: %w", err)
	}
	if h.Evidence == nil {
		ev = []byte("{}")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	err = tx.Exec(`
INSERT INTO anomalies (id, rule_code, severity, status, subject_type, subject_id, party_id, region_code,
                       dedupe_key, hit_count, evidence, first_seen_at, last_seen_at, created_at, updated_at)
VALUES (CAST(@id AS uuid), CAST(@rule AS text), CAST(@severity AS text), 'OPEN', CAST(@subject_type AS text),
        CAST(@subject_id AS uuid), CAST(@party AS uuid), CAST(@region AS text), CAST(@dedupe AS text), 1,
        CAST(@evidence AS jsonb), CAST(@at AS timestamptz), CAST(@at AS timestamptz),
        CAST(@at AS timestamptz), CAST(@at AS timestamptz))
ON CONFLICT ON CONSTRAINT anomaly_dedupe_once DO UPDATE
   SET hit_count    = anomalies.hit_count + 1,
       last_seen_at = GREATEST(anomalies.last_seen_at, EXCLUDED.last_seen_at),
       evidence     = anomalies.evidence || jsonb_build_object('last', EXCLUDED.evidence),
       version      = anomalies.version + 1,
       updated_at   = EXCLUDED.updated_at`,
		map[string]any{
			"id": id, "rule": h.RuleCode, "severity": h.Severity, "subject_type": h.SubjectType,
			"subject_id": h.SubjectID, "party": h.PartyID, "region": h.RegionCode, "dedupe": h.DedupeKey,
			"evidence": string(ev), "at": h.At,
		}).Error
	if err != nil {
		return fmt.Errorf("upsert anomali %s: %w", h.RuleCode, err)
	}
	return nil
}

// VerifiedMark adalah nilai verifikasi pertama sebelum scan ini.
type VerifiedMark struct {
	PrevFirstAt      *time.Time
	PrevRegionName   *string
	FirstAtAfter     *time.Time
	RegionNameAfter  *string
	VerifyCountAfter int
}

// MarkVerified menaikkan verify_count secara atomik dan, kalau genuine,
// mengisi first_verified_at/region hanya kalau belum ada. Baris dikunci,
// jadi dua scan bersamaan tidak bisa sama-sama menjadi "pertama".
func (r *VerifyRepository) MarkVerified(tx *gorm.DB, serialID uuid.UUID, at time.Time, region *string, genuine bool) (VerifiedMark, error) {
	var rows []VerifiedMark
	err := tx.Raw(`
WITH old AS (
    SELECT id, first_verified_at, first_verified_region_code
      FROM serial_units WHERE id = CAST(@id AS uuid) FOR UPDATE
), upd AS (
    UPDATE serial_units s
       SET verify_count = s.verify_count + 1,
           first_verified_at = CASE WHEN CAST(@genuine AS boolean) AND s.first_verified_at IS NULL
                                    THEN CAST(@at AS timestamptz) ELSE s.first_verified_at END,
           first_verified_region_code = CASE WHEN CAST(@genuine AS boolean) AND s.first_verified_at IS NULL
                                    THEN CAST(@region AS text) ELSE s.first_verified_region_code END,
           updated_at = CAST(@at AS timestamptz)
      FROM old
     WHERE s.id = old.id
 RETURNING old.first_verified_at AS prev_first_at, old.first_verified_region_code AS prev_region,
           s.first_verified_at AS first_at_after, s.first_verified_region_code AS region_after,
           s.verify_count AS verify_count_after
)
SELECT upd.prev_first_at, pr.name AS prev_region_name, upd.first_at_after,
       ar.name AS region_name_after, upd.verify_count_after
  FROM upd
  LEFT JOIN regions pr ON pr.code = upd.prev_region
  LEFT JOIN regions ar ON ar.code = upd.region_after`,
		map[string]any{"id": serialID, "at": at, "region": region, "genuine": genuine}).Scan(&rows).Error
	if err != nil {
		return VerifiedMark{}, fmt.Errorf("menandai verifikasi: %w", err)
	}
	if len(rows) == 0 {
		return VerifiedMark{}, fmt.Errorf("menandai verifikasi: serial %s tidak ditemukan", serialID)
	}
	return rows[0], nil
}
