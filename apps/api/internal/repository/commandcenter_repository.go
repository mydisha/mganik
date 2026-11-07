package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// CommandCenterRepository adalah read model Command Center (Pilar D). Semua
// list memakai satu query set-based per halaman (tanpa query per baris) dan
// pagination keyset. Satu-satunya tulisan adalah transisi status anomali.
type CommandCenterRepository struct {
	db *gorm.DB
}

func NewCommandCenterRepository(db *gorm.DB) *CommandCenterRepository {
	return &CommandCenterRepository{db: db}
}

// CCScope membatasi data ke lokasi yang boleh dilihat pemanggil. All=false
// dengan LocationIDs kosong berarti tidak ada lokasi (bukan semua lokasi).
type CCScope struct {
	All         bool
	LocationIDs []uuid.UUID
}

// sqlq menyusun SQL dan argumen posisional (`?`) sesuai urutan teks, supaya
// kondisi opsional hanya muncul kalau dipakai (rencana query tetap memakai index).
type sqlq struct {
	sb   strings.Builder
	args []any
}

func (q *sqlq) w(s string, args ...any) *sqlq {
	q.sb.WriteString(s)
	q.args = append(q.args, args...)
	return q
}

// scope menambahkan `AND col = ANY(lokasi)` kalau pemanggil tidak ber-scope ALL.
func (q *sqlq) scope(col string, s CCScope) *sqlq {
	if s.All {
		return q
	}
	return q.w(" AND "+col+" = ANY(CAST(? AS uuid[]))", uuidArray(s.LocationIDs))
}

func (q *sqlq) String() string { return q.sb.String() }

// uuidArray membuat literal array Postgres. Dikirim sebagai satu parameter
// teks, jadi GORM tidak mengembangkan slice menjadi daftar `(?, ?)`.
func uuidArray(ids []uuid.UUID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String()
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func textArray(vals []string) string {
	// Hanya dipakai untuk kode status internal (huruf besar + garis bawah).
	return "{" + strings.Join(vals, ",") + "}"
}

// ------------------------------------------------------------------ stok

type CCStockFilter struct {
	Scope        CCScope
	LocationType *string
	LocationID   *uuid.UUID
	ProductID    *uuid.UUID
	After        *CCStockKey
	Limit        int
}

// CCStockKey: urutan (kode lokasi, SKU, location_id, product_id).
type CCStockKey struct {
	LocationCode string    `json:"lc"`
	SKU          string    `json:"sku"`
	LocationID   uuid.UUID `json:"lid"`
	ProductID    uuid.UUID `json:"pid"`
}

func (k CCStockKey) Validate() error {
	if k.LocationID == uuid.Nil || k.ProductID == uuid.Nil {
		return errors.New("key stok tidak lengkap")
	}
	return nil
}

type CCStockRow struct {
	LocationID      uuid.UUID  `gorm:"column:location_id"`
	LocationCode    string     `gorm:"column:location_code"`
	LocationName    string     `gorm:"column:location_name"`
	LocationType    string     `gorm:"column:location_type"`
	SyncState       string     `gorm:"column:sync_state"`
	LastHeartbeatAt *time.Time `gorm:"column:last_heartbeat_at"`
	ProductID       uuid.UUID  `gorm:"column:product_id"`
	SKU             string     `gorm:"column:sku"`
	ProductName     string     `gorm:"column:product_name"`
	PhysicalQty     int64      `gorm:"column:physical_qty"`
	ReservedQty     int64      `gorm:"column:reserved_qty"`
	QuarantineQty   int64      `gorm:"column:quarantine_qty"`
	AvailableQty    int64      `gorm:"column:available_qty"`
	IsEstimate      bool       `gorm:"column:is_estimate"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (r CCStockRow) Key() CCStockKey {
	return CCStockKey{LocationCode: r.LocationCode, SKU: r.SKU, LocationID: r.LocationID, ProductID: r.ProductID}
}

// ListStock mengembalikan maksimal Limit+1 baris saldo (baris ekstra = ada
// halaman berikutnya).
func (r *CommandCenterRepository) ListStock(ctx context.Context, f CCStockFilter) ([]CCStockRow, error) {
	q := &sqlq{}
	q.w(`SELECT b.location_id, l.code AS location_code, l.name AS location_name, l.type_code AS location_type,
	            l.sync_state, l.last_heartbeat_at, b.product_id, p.sku, p.name AS product_name,
	            b.physical_qty, b.reserved_qty, b.quarantine_qty, b.available_qty,
	            NOT lt.is_own AS is_estimate, b.updated_at
	       FROM inventory_balances b
	       JOIN locations l       ON l.id = b.location_id AND l.archived_at IS NULL
	       JOIN location_types lt ON lt.code = l.type_code
	       JOIN products p        ON p.id = b.product_id AND p.archived_at IS NULL
	      WHERE true`)
	q.scope("b.location_id", f.Scope)
	if f.LocationType != nil {
		q.w(" AND l.type_code = ?", *f.LocationType)
	}
	if f.LocationID != nil {
		q.w(" AND b.location_id = ?", *f.LocationID)
	}
	if f.ProductID != nil {
		q.w(" AND b.product_id = ?", *f.ProductID)
	}
	if k := f.After; k != nil {
		q.w(" AND (l.code, p.sku, b.location_id, b.product_id) > (?, ?, CAST(? AS uuid), CAST(? AS uuid))",
			k.LocationCode, k.SKU, k.LocationID, k.ProductID)
	}
	q.w(" ORDER BY l.code, p.sku, b.location_id, b.product_id LIMIT ?", f.Limit+1)

	var rows []CCStockRow
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("membaca stok command center: %w", err)
	}
	return rows, nil
}

// ------------------------------------------------------------------ runout

// Level runout, urut dari paling kritis.
const (
	RunoutCritical         = "CRITICAL"
	RunoutWarning          = "WARNING"
	RunoutInsufficientData = "INSUFFICIENT_DATA"
	RunoutOK               = "OK"
)

type CCRunoutFilter struct {
	// Now dari clock.Clock; jendela penjualan = [Now - WindowDays, Now).
	Now          time.Time
	WindowDays   int
	Scope        CCScope
	LocationType *string
	LocationID   *uuid.UUID
	ProductID    *uuid.UUID
	Level        *string
	// OwnOnly: hanya lokasi milik sendiri (ringkasan; stok mitra hanya estimasi).
	OwnOnly bool
	After   *CCRunoutKey
	Limit   int
}

// CCRunoutKey: urutan (rank level, DOI, kode lokasi, SKU, location_id,
// product_id). DOI disimpan sebagai teks numeric supaya perbandingan keyset
// persis sama dengan nilai di database (tanpa pembulatan float).
type CCRunoutKey struct {
	Rank         int       `json:"r"`
	DOI          string    `json:"d"`
	LocationCode string    `json:"lc"`
	SKU          string    `json:"sku"`
	LocationID   uuid.UUID `json:"lid"`
	ProductID    uuid.UUID `json:"pid"`
}

func (k CCRunoutKey) Validate() error {
	if k.Rank < 1 || k.Rank > 4 || k.LocationID == uuid.Nil || k.ProductID == uuid.Nil || !isDecimal(k.DOI) {
		return errors.New("key runout tidak valid")
	}
	return nil
}

type CCRunoutRow struct {
	LocationID    uuid.UUID `gorm:"column:location_id"`
	LocationCode  string    `gorm:"column:location_code"`
	LocationName  string    `gorm:"column:location_name"`
	LocationType  string    `gorm:"column:location_type"`
	ProductID     uuid.UUID `gorm:"column:product_id"`
	SKU           string    `gorm:"column:sku"`
	ProductName   string    `gorm:"column:product_name"`
	AvailableQty  int64     `gorm:"column:available_qty"`
	IsEstimate    bool      `gorm:"column:is_estimate"`
	SoldQty       int64     `gorm:"column:sold_qty"`
	HistoryDays   *int      `gorm:"column:history_days"`
	AvgDailySales *float64  `gorm:"column:avg_daily_sales"`
	DOI           *float64  `gorm:"column:doi_display"`
	RunoutDate    *string   `gorm:"column:runout_date"`
	Level         string    `gorm:"column:level"`
	Reasons       string    `gorm:"column:reasons"`
	HasPolicy     bool      `gorm:"column:has_policy"`
	MinQty        *int64    `gorm:"column:min_qty"`
	LeadTimeDays  *int      `gorm:"column:lead_time_days"`
	SafetyDays    *int      `gorm:"column:safety_days"`
	LevelRank     int       `gorm:"column:level_rank"`
	DOISort       string    `gorm:"column:doi_sort"`
}

func (r CCRunoutRow) Key() CCRunoutKey {
	return CCRunoutKey{Rank: r.LevelRank, DOI: r.DOISort, LocationCode: r.LocationCode, SKU: r.SKU,
		LocationID: r.LocationID, ProductID: r.ProductID}
}

// ReasonList memecah kolom reasons (dipisah koma).
func (r CCRunoutRow) ReasonList() []string {
	if r.Reasons == "" {
		return []string{}
	}
	return strings.Split(r.Reasons, ",")
}

// ShortHistoryDays: riwayat penjualan lebih pendek dari ini diberi reason
// SHORT_HISTORY (DOI tetap dihitung, tetapi kurang stabil).
const ShortHistoryDays = 7

// runoutCTE menulis CTE `ranked`: satu baris per (lokasi, produk) dengan DOI,
// level, dan alasan. Satu query agregat: penjualan dalam jendela diagregasi
// sekali (index parsial inventory_movements_sales_window), lalu di-join ke saldo.
//
//   - history_days = hari sejak mutasi pertama (lokasi, produk), dibulatkan ke
//     atas, minimal 1 dan maksimal panjang jendela. SKU yang baru 2 hari tidak
//     dibagi 28 hari (itu membuat DOI 14x terlalu optimistis).
//   - avg_daily_sales = terjual dalam jendela ÷ history_days
//   - DOI = available ÷ avg_daily_sales, dibulatkan 2 desimal SEKALI; nilai
//     yang sama dipakai untuk level, urutan, dan tampilan. NULL kalau tidak
//     ada penjualan: tidak mengarang angka, pembagian dengan nol tidak terjadi.
//   - CRITICAL: DOI < lead_time_days, atau stok habis padahal masih laku
//   - WARNING : DOI < lead_time_days + safety_days, atau available < min_qty
//   - INSUFFICIENT_DATA: tanpa riwayat penjualan (dan tidak di bawah min_qty)
//   - tanpa stock_policies: hanya aturan stok habis yang berlaku, ditandai NO_POLICY
//
// Lokasi virtual (IN_TRANSIT) tidak ikut: stok di jalan tidak dijual.
func runoutCTE(q *sqlq, f CCRunoutFilter) {
	since := f.Now.Add(-time.Duration(f.WindowDays) * 24 * time.Hour)
	q.w(`WITH sales AS (
	  SELECT m.location_id, m.product_id, -SUM(m.physical_delta) AS sold_qty
	    FROM inventory_movements m
	   WHERE m.movement_type IN ('DIRECT_SALE', 'COMMIT_SALE')
	     AND m.occurred_at >= ? AND m.occurred_at < ?`, since, f.Now)
	// Filter didorong ke agregasi penjualan supaya tidak menjumlah seluruh jaringan.
	q.scope("m.location_id", f.Scope)
	if f.LocationID != nil {
		q.w(" AND m.location_id = ?", *f.LocationID)
	}
	if f.ProductID != nil {
		q.w(" AND m.product_id = ?", *f.ProductID)
	}
	q.w(`
	   GROUP BY m.location_id, m.product_id
	), base AS (
	  SELECT b.location_id, b.product_id, l.code AS location_code, l.name AS location_name,
	         l.type_code AS location_type, l.timezone, NOT lt.is_own AS is_estimate,
	         p.sku, p.name AS product_name, b.available_qty,
	         COALESCE(s.sold_qty, 0) AS sold_qty,
	         sp.location_id IS NOT NULL AS has_policy, sp.min_qty, sp.lead_time_days, sp.safety_days,
	         CASE WHEN s.sold_qty > 0 THEN
	           LEAST(?, GREATEST(1, ceil(extract(epoch FROM (CAST(? AS timestamptz) - fm.first_at)) / 86400)))::int
	         END AS history_days
	    FROM inventory_balances b
	    JOIN locations l       ON l.id = b.location_id AND l.archived_at IS NULL
	    JOIN location_types lt ON lt.code = l.type_code AND NOT lt.is_virtual
	    JOIN products p        ON p.id = b.product_id AND p.archived_at IS NULL
	    LEFT JOIN stock_policies sp ON sp.location_id = b.location_id AND sp.product_id = b.product_id
	    LEFT JOIN sales s           ON s.location_id = b.location_id AND s.product_id = b.product_id
	    -- Mutasi pertama (index inventory_movements_loc_prod_time), hanya untuk yang laku.
	    LEFT JOIN LATERAL (
	      SELECT min(m2.occurred_at) AS first_at FROM inventory_movements m2
	       WHERE m2.location_id = b.location_id AND m2.product_id = b.product_id AND s.sold_qty > 0
	    ) fm ON true
	   WHERE true`, f.WindowDays, f.Now)
	q.scope("b.location_id", f.Scope)
	if f.OwnOnly {
		q.w(" AND lt.is_own")
	}
	if f.LocationType != nil {
		q.w(" AND l.type_code = ?", *f.LocationType)
	}
	if f.LocationID != nil {
		q.w(" AND b.location_id = ?", *f.LocationID)
	}
	if f.ProductID != nil {
		q.w(" AND b.product_id = ?", *f.ProductID)
	}
	q.w(`
	), calc AS (
	  SELECT base.*,
	         CASE WHEN sold_qty > 0 THEN round(available_qty::numeric * history_days / sold_qty, 2) END AS doi
	    FROM base
	), leveled AS (
	  SELECT calc.*,
	         CASE
	           WHEN doi IS NOT NULL AND (available_qty <= 0 OR doi < lead_time_days) THEN 'CRITICAL'
	           WHEN doi IS NOT NULL AND doi < lead_time_days + safety_days THEN 'WARNING'
	           WHEN available_qty < min_qty THEN 'WARNING'
	           WHEN doi IS NULL THEN 'INSUFFICIENT_DATA'
	           ELSE 'OK'
	         END AS level
	    FROM calc
	), ranked AS (
	  SELECT leveled.*,
	         CASE level WHEN 'CRITICAL' THEN 1 WHEN 'WARNING' THEN 2 WHEN 'INSUFFICIENT_DATA' THEN 3 ELSE 4 END AS level_rank,
	         COALESCE(doi, 1e12) AS doi_key,
	         CASE WHEN doi IS NOT NULL AND doi <= 3650
	              THEN to_char((CAST(? AS timestamptz) AT TIME ZONE timezone)::date + floor(doi)::int, 'YYYY-MM-DD')
	         END AS runout_date
	    FROM leveled
	)`, f.Now)
}

// ListRunout mengembalikan maksimal Limit+1 baris, paling kritis dulu.
func (r *CommandCenterRepository) ListRunout(ctx context.Context, f CCRunoutFilter) ([]CCRunoutRow, error) {
	q := runoutListQuery(f)
	var rows []CCRunoutRow
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("menghitung runout: %w", err)
	}
	return rows, nil
}

// RunoutSQL mengembalikan SQL + argumen ListRunout (untuk EXPLAIN di test).
func RunoutSQL(f CCRunoutFilter) (string, []any) {
	q := runoutListQuery(f)
	return q.String(), q.args
}

func runoutListQuery(f CCRunoutFilter) *sqlq {
	q := &sqlq{}
	runoutCTE(q, f)
	q.w(`
	SELECT location_id, location_code, location_name, location_type, product_id, sku, product_name,
	       available_qty, is_estimate, sold_qty, history_days,
	       CASE WHEN sold_qty > 0 THEN round(sold_qty::numeric / history_days, 4)::float8 END AS avg_daily_sales,
	       doi::float8 AS doi_display, runout_date, level,
	       array_to_string(array_remove(ARRAY[
	         CASE WHEN doi IS NOT NULL AND available_qty <= 0 THEN 'STOCK_OUT' END,
	         CASE WHEN doi < lead_time_days THEN 'BELOW_LEAD_TIME' END,
	         CASE WHEN doi >= lead_time_days AND doi < lead_time_days + safety_days THEN 'BELOW_SAFETY_DAYS' END,
	         CASE WHEN available_qty < min_qty THEN 'BELOW_MIN_QTY' END,
	         CASE WHEN doi IS NULL THEN 'NO_SALES_HISTORY' END,
	         CASE WHEN history_days < ? THEN 'SHORT_HISTORY' END,
	         CASE WHEN NOT has_policy THEN 'NO_POLICY' END
	       ], NULL), ',') AS reasons,
	       has_policy, min_qty, lead_time_days, safety_days, level_rank, doi_key::text AS doi_sort
	  FROM ranked
	 WHERE true`, ShortHistoryDays)
	if f.Level != nil {
		q.w(" AND level = ?", *f.Level)
	}
	if k := f.After; k != nil {
		q.w(` AND (level_rank, doi_key, location_code, sku, location_id, product_id)
		        > (?, CAST(? AS numeric), ?, ?, CAST(? AS uuid), CAST(? AS uuid))`,
			k.Rank, k.DOI, k.LocationCode, k.SKU, k.LocationID, k.ProductID)
	}
	q.w(" ORDER BY level_rank, doi_key, location_code, sku, location_id, product_id LIMIT ?", f.Limit+1)
	return q
}

// ------------------------------------------------------------------ ringkasan

type CCRunoutSummary struct {
	CriticalItems         int64   `gorm:"column:critical_items"`
	CriticalLocations     int64   `gorm:"column:critical_locations"`
	WarningItems          int64   `gorm:"column:warning_items"`
	WarningLocations      int64   `gorm:"column:warning_locations"`
	InsufficientDataItems int64   `gorm:"column:insufficient_data_items"`
	EarliestRunoutDate    *string `gorm:"column:earliest_runout_date"`
}

type CCSummaryCounts struct {
	OutOfStockItems     int64 `gorm:"column:out_of_stock_items"`
	OutOfStockLocations int64 `gorm:"column:out_of_stock_locations"`
	ConflictsOpen       int64 `gorm:"column:conflicts_open"`
	StoresTotal         int64 `gorm:"column:stores_total"`
	StoresOffline       int64 `gorm:"column:stores_offline"`
	AnomaliesOpen       int64 `gorm:"column:anomalies_open"`
	OpenLow             int64 `gorm:"column:open_low"`
	OpenMedium          int64 `gorm:"column:open_medium"`
	OpenHigh            int64 `gorm:"column:open_high"`
	OpenCritical        int64 `gorm:"column:open_critical"`
}

type CCPartyCountRow struct {
	PartyID   uuid.UUID `gorm:"column:party_id"`
	PartyName string    `gorm:"column:party_name"`
	OpenCount int64     `gorm:"column:open_count"`
}

// RunoutSummary menghitung jumlah item/lokasi per level dari CTE runout yang sama.
func (r *CommandCenterRepository) RunoutSummary(ctx context.Context, f CCRunoutFilter) (CCRunoutSummary, error) {
	q := &sqlq{}
	runoutCTE(q, f)
	q.w(`
	SELECT count(*) FILTER (WHERE level = 'CRITICAL') AS critical_items,
	       count(DISTINCT location_id) FILTER (WHERE level = 'CRITICAL') AS critical_locations,
	       count(*) FILTER (WHERE level = 'WARNING') AS warning_items,
	       count(DISTINCT location_id) FILTER (WHERE level = 'WARNING') AS warning_locations,
	       count(*) FILTER (WHERE level = 'INSUFFICIENT_DATA') AS insufficient_data_items,
	       min(runout_date) FILTER (WHERE level IN ('CRITICAL', 'WARNING')) AS earliest_runout_date
	  FROM ranked`)
	var out CCRunoutSummary
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&out).Error; err != nil {
		return CCRunoutSummary{}, fmt.Errorf("ringkasan runout: %w", err)
	}
	return out, nil
}

// SummaryCounts menghitung stok habis, konflik terbuka, toko offline, dan
// anomali OPEN per severity dalam satu query.
func (r *CommandCenterRepository) SummaryCounts(ctx context.Context, s CCScope) (CCSummaryCounts, error) {
	q := &sqlq{}
	q.w(`SELECT
	  (SELECT count(*) FROM inventory_balances b
	     JOIN locations l ON l.id = b.location_id AND l.archived_at IS NULL
	     JOIN location_types lt ON lt.code = l.type_code AND lt.is_own AND NOT lt.is_virtual
	    WHERE b.available_qty = 0`)
	q.scope("b.location_id", s)
	q.w(`) AS out_of_stock_items,
	  (SELECT count(DISTINCT b.location_id) FROM inventory_balances b
	     JOIN locations l ON l.id = b.location_id AND l.archived_at IS NULL
	     JOIN location_types lt ON lt.code = l.type_code AND lt.is_own AND NOT lt.is_virtual
	    WHERE b.available_qty = 0`)
	q.scope("b.location_id", s)
	q.w(`) AS out_of_stock_locations,
	  (SELECT count(*) FROM allocation_conflicts c WHERE c.status = 'OPEN'`)
	q.scope("c.location_id", s)
	q.w(`) AS conflicts_open,
	  (SELECT count(*) FROM locations l WHERE l.type_code = 'OWN_STORE' AND l.archived_at IS NULL`)
	q.scope("l.id", s)
	q.w(`) AS stores_total,
	  (SELECT count(*) FROM locations l WHERE l.type_code = 'OWN_STORE' AND l.archived_at IS NULL AND l.sync_state = 'OFFLINE'`)
	q.scope("l.id", s)
	q.w(`) AS stores_offline,
	  a.anomalies_open, a.open_low, a.open_medium, a.open_high, a.open_critical
	  FROM (SELECT count(*) AS anomalies_open,
	               count(*) FILTER (WHERE severity = 'LOW') AS open_low,
	               count(*) FILTER (WHERE severity = 'MEDIUM') AS open_medium,
	               count(*) FILTER (WHERE severity = 'HIGH') AS open_high,
	               count(*) FILTER (WHERE severity = 'CRITICAL') AS open_critical
	          FROM anomalies an WHERE an.status = 'OPEN'`)
	anomalyScope(q, "an", s)
	q.w(`) a`)
	var out CCSummaryCounts
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&out).Error; err != nil {
		return CCSummaryCounts{}, fmt.Errorf("ringkasan command center: %w", err)
	}
	return out, nil
}

// TopAnomalyParty: party dengan anomali OPEN terbanyak (nil kalau tidak ada).
func (r *CommandCenterRepository) TopAnomalyParty(ctx context.Context, s CCScope) (*CCPartyCountRow, error) {
	q := &sqlq{}
	q.w(`SELECT an.party_id, p.name AS party_name, count(*) AS open_count
	       FROM anomalies an JOIN parties p ON p.id = an.party_id
	      WHERE an.status = 'OPEN'`)
	anomalyScope(q, "an", s)
	q.w(` GROUP BY an.party_id, p.name ORDER BY open_count DESC, p.name, an.party_id LIMIT 1`)
	var rows []CCPartyCountRow
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("party anomali terbanyak: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// ------------------------------------------------------------------ anomali

// Subject anomali yang terikat ke satu lokasi. Pemanggil ber-scope SELECTED
// hanya melihat anomali dengan subject ini di lokasinya; anomali distribusi
// (party/wilayah) hanya untuk scope ALL.
const AnomalySubjectLocation = "LOCATION"

func anomalyScope(q *sqlq, alias string, s CCScope) {
	if s.All {
		return
	}
	q.w(" AND "+alias+".subject_type = ? AND "+alias+".subject_id = ANY(CAST(? AS uuid[]))",
		AnomalySubjectLocation, uuidArray(s.LocationIDs))
}

type CCAnomalyFilter struct {
	Scope    CCScope
	Status   *string
	Severity *string
	RuleCode *string
	PartyID  *uuid.UUID
	After    *CCTimeIDKey
	Limit    int
}

// CCTimeIDKey sama bentuknya dengan pagination.TimeID; didefinisikan di sini
// supaya repository tidak bergantung ke paket HTTP-level.
type CCTimeIDKey struct {
	At time.Time
	ID uuid.UUID
}

type CCAnomalyRow struct {
	ID             uuid.UUID  `gorm:"column:id"`
	RuleCode       string     `gorm:"column:rule_code"`
	RuleName       string     `gorm:"column:rule_name"`
	Severity       string     `gorm:"column:severity"`
	Status         string     `gorm:"column:status"`
	SubjectType    string     `gorm:"column:subject_type"`
	SubjectID      *uuid.UUID `gorm:"column:subject_id"`
	PartyID        *uuid.UUID `gorm:"column:party_id"`
	PartyName      *string    `gorm:"column:party_name"`
	RegionCode     *string    `gorm:"column:region_code"`
	RegionName     *string    `gorm:"column:region_name"`
	HitCount       int        `gorm:"column:hit_count"`
	Evidence       string     `gorm:"column:evidence"`
	FirstSeenAt    time.Time  `gorm:"column:first_seen_at"`
	LastSeenAt     time.Time  `gorm:"column:last_seen_at"`
	ResolvedBy     *uuid.UUID `gorm:"column:resolved_by"`
	ResolvedAt     *time.Time `gorm:"column:resolved_at"`
	ResolutionNote *string    `gorm:"column:resolution_note"`
	Version        int64      `gorm:"column:version"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

const anomalySelect = `SELECT an.id, an.rule_code, r.name AS rule_name, an.severity, an.status,
	       an.subject_type, an.subject_id, an.party_id, p.name AS party_name,
	       an.region_code, rg.name AS region_name, an.hit_count, an.evidence::text AS evidence,
	       an.first_seen_at, an.last_seen_at, an.resolved_by, an.resolved_at, an.resolution_note,
	       an.version, an.created_at, an.updated_at
	  FROM anomalies an
	  JOIN anomaly_rules r ON r.code = an.rule_code
	  LEFT JOIN parties p  ON p.id = an.party_id
	  LEFT JOIN regions rg ON rg.code = an.region_code`

// ListAnomalies: maksimal Limit+1 baris, terbaru dulu (created_at, id DESC).
func (r *CommandCenterRepository) ListAnomalies(ctx context.Context, f CCAnomalyFilter) ([]CCAnomalyRow, error) {
	q := &sqlq{}
	q.w(anomalySelect + " WHERE true")
	anomalyScope(q, "an", f.Scope)
	if f.Status != nil {
		q.w(" AND an.status = ?", *f.Status)
	}
	if f.Severity != nil {
		q.w(" AND an.severity = ?", *f.Severity)
	}
	if f.RuleCode != nil {
		q.w(" AND an.rule_code = ?", *f.RuleCode)
	}
	if f.PartyID != nil {
		q.w(" AND an.party_id = ?", *f.PartyID)
	}
	if k := f.After; k != nil {
		q.w(" AND (an.created_at, an.id) < (?, CAST(? AS uuid))", k.At, k.ID)
	}
	q.w(" ORDER BY an.created_at DESC, an.id DESC LIMIT ?", f.Limit+1)
	var rows []CCAnomalyRow
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("membaca anomali: %w", err)
	}
	return rows, nil
}

// GetAnomaly membaca satu anomali dalam scope (nil kalau tidak ada/di luar scope).
func (r *CommandCenterRepository) GetAnomaly(ctx context.Context, db *gorm.DB, id uuid.UUID, s CCScope) (*CCAnomalyRow, error) {
	if db == nil {
		db = r.db
	}
	q := &sqlq{}
	q.w(anomalySelect+" WHERE an.id = ?", id)
	anomalyScope(q, "an", s)
	var rows []CCAnomalyRow
	if err := db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("membaca anomali: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// AnomalyState adalah status anomali yang dikunci untuk transisi.
type AnomalyState struct {
	Status         string  `gorm:"column:status"`
	Version        int64   `gorm:"column:version"`
	ResolutionNote *string `gorm:"column:resolution_note"`
}

// LockAnomaly mengunci baris anomali (FOR UPDATE) di transaksi tx. nil kalau
// tidak ada atau di luar scope.
func (r *CommandCenterRepository) LockAnomaly(ctx context.Context, tx *gorm.DB, id uuid.UUID, s CCScope) (*AnomalyState, error) {
	q := &sqlq{}
	q.w("SELECT an.status, an.version, an.resolution_note FROM anomalies an WHERE an.id = ?", id)
	anomalyScope(q, "an", s)
	q.w(" FOR UPDATE")
	var rows []AnomalyState
	if err := tx.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("mengunci anomali: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// AnomalyTransition adalah perubahan status bersyarat.
type AnomalyTransition struct {
	ID       uuid.UUID
	From     []string
	To       string
	Terminal bool
	Actor    *uuid.UUID
	Note     *string
	At       time.Time
}

// TransitionAnomaly menjalankan UPDATE bersyarat `WHERE status = ANY(from)`.
// false kalau tidak ada baris yang berubah (status sudah berpindah).
// Untuk status akhir, resolved_by/resolved_at/resolution_note diisi.
func (r *CommandCenterRepository) TransitionAnomaly(ctx context.Context, tx *gorm.DB, t AnomalyTransition) (bool, error) {
	res := tx.WithContext(ctx).Exec(`
		UPDATE anomalies
		   SET status          = ?,
		       version         = version + 1,
		       updated_at      = ?,
		       resolved_by     = CASE WHEN ? THEN CAST(? AS uuid) ELSE resolved_by END,
		       resolved_at     = CASE WHEN ? THEN CAST(? AS timestamptz) ELSE resolved_at END,
		       resolution_note = CASE WHEN ? THEN CAST(? AS text) ELSE resolution_note END
		 WHERE id = ? AND status = ANY(CAST(? AS text[]))`,
		t.To, t.At, t.Terminal, t.Actor, t.Terminal, t.At, t.Terminal, t.Note, t.ID, textArray(t.From))
	if res.Error != nil {
		return false, fmt.Errorf("mengubah status anomali: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// InsertAudit menulis satu baris audit_logs di transaksi tx.
func (r *CommandCenterRepository) InsertAudit(ctx context.Context, tx *gorm.DB, a *model.AuditLog) error {
	if err := tx.WithContext(ctx).Create(a).Error; err != nil {
		return fmt.Errorf("menulis audit log: %w", err)
	}
	return nil
}

// Transaction menjalankan fn dalam satu transaksi database.
func (r *CommandCenterRepository) Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}

// ------------------------------------------------------------------ konflik

type CCConflictFilter struct {
	Scope      CCScope
	Status     *string
	Kind       *string
	LocationID *uuid.UUID
	After      *CCTimeIDKey
	Limit      int
}

type CCConflictRow struct {
	ID               uuid.UUID  `gorm:"column:id"`
	Kind             string     `gorm:"column:kind"`
	Status           string     `gorm:"column:status"`
	LocationID       uuid.UUID  `gorm:"column:location_id"`
	LocationCode     string     `gorm:"column:location_code"`
	LocationName     string     `gorm:"column:location_name"`
	ProductID        uuid.UUID  `gorm:"column:product_id"`
	SKU              string     `gorm:"column:sku"`
	ProductName      string     `gorm:"column:product_name"`
	Qty              *int64     `gorm:"column:qty"`
	PosTransactionID *uuid.UUID `gorm:"column:pos_transaction_id"`
	ReservationID    *uuid.UUID `gorm:"column:reservation_id"`
	OrderID          *uuid.UUID `gorm:"column:order_id"`
	SerialUnitID     *uuid.UUID `gorm:"column:serial_unit_id"`
	Resolution       *string    `gorm:"column:resolution"`
	Details          string     `gorm:"column:details"`
	ResolvedBy       *uuid.UUID `gorm:"column:resolved_by"`
	ResolvedAt       *time.Time `gorm:"column:resolved_at"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
}

// ListConflicts: maksimal Limit+1 baris, terbaru dulu (created_at, id DESC).
func (r *CommandCenterRepository) ListConflicts(ctx context.Context, f CCConflictFilter) ([]CCConflictRow, error) {
	q := &sqlq{}
	q.w(`SELECT c.id, c.kind, c.status, c.location_id, l.code AS location_code, l.name AS location_name,
	            c.product_id, p.sku, p.name AS product_name, c.qty, c.pos_transaction_id, c.reservation_id,
	            c.order_id, c.serial_unit_id, c.resolution, c.details::text AS details,
	            c.resolved_by, c.resolved_at, c.created_at
	       FROM allocation_conflicts c
	       JOIN locations l ON l.id = c.location_id
	       JOIN products p  ON p.id = c.product_id
	      WHERE true`)
	q.scope("c.location_id", f.Scope)
	if f.Status != nil {
		q.w(" AND c.status = ?", *f.Status)
	}
	if f.Kind != nil {
		q.w(" AND c.kind = ?", *f.Kind)
	}
	if f.LocationID != nil {
		q.w(" AND c.location_id = ?", *f.LocationID)
	}
	if k := f.After; k != nil {
		q.w(" AND (c.created_at, c.id) < (?, CAST(? AS uuid))", k.At, k.ID)
	}
	q.w(" ORDER BY c.created_at DESC, c.id DESC LIMIT ?", f.Limit+1)
	var rows []CCConflictRow
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("membaca konflik alokasi: %w", err)
	}
	return rows, nil
}

// ------------------------------------------------------------------ kebocoran wilayah

// LeakageParams berasal dari anomaly_rules OUT_OF_TERRITORY.params.
type LeakageParams struct {
	MinScans         int64
	BaselineRatio    float64
	TestBuyImmediate bool
}

// OutOfTerritoryParams membaca params mentah aturan OUT_OF_TERRITORY ("" kalau
// aturan tidak ada).
func (r *CommandCenterRepository) OutOfTerritoryParams(ctx context.Context) (string, error) {
	var raw sql.NullString
	err := r.db.WithContext(ctx).Raw(`SELECT params::text FROM anomaly_rules WHERE code = 'OUT_OF_TERRITORY'`).Scan(&raw).Error
	if err != nil {
		return "", fmt.Errorf("membaca aturan OUT_OF_TERRITORY: %w", err)
	}
	return raw.String, nil
}

type CCLeakageFilter struct {
	From    time.Time
	To      time.Time
	Scope   CCScope
	PartyID *uuid.UUID
	Params  LeakageParams
	After   *CCLeakageKey
	Limit   int
}

// CCLeakageKey: urutan (alert dulu, rasio tertinggi, nama, id). Rasio disimpan
// sebagai teks numeric (dinegasikan) supaya keyset persis.
type CCLeakageKey struct {
	AlertRank int       `json:"a"`
	RatioKey  string    `json:"r"`
	Name      string    `json:"n"`
	PartyID   uuid.UUID `json:"id"`
}

func (k CCLeakageKey) Validate() error {
	if (k.AlertRank != 0 && k.AlertRank != 1) || k.PartyID == uuid.Nil || !isDecimal(k.RatioKey) {
		return errors.New("key kebocoran tidak valid")
	}
	return nil
}

type CCLeakageRow struct {
	PartyID        uuid.UUID `gorm:"column:party_id"`
	PartyName      string    `gorm:"column:party_name"`
	CoverageMode   *string   `gorm:"column:coverage_mode"`
	TotalScans     int64     `gorm:"column:total_scans"`
	LocatedScans   int64     `gorm:"column:located_scans"`
	JudgedScans    int64     `gorm:"column:judged_scans"`
	OutOfTerritory int64     `gorm:"column:out_of_territory"`
	LeakRatio      *float64  `gorm:"column:leak_ratio"`
	SampleTooSmall bool      `gorm:"column:sample_too_small"`
	TestBuyScans   int64     `gorm:"column:test_buy_scans"`
	TestBuyHits    int64     `gorm:"column:test_buy_hits"`
	AboveBaseline  bool      `gorm:"column:above_baseline"`
	Alert          bool      `gorm:"column:alert"`
	AlertRank      int       `gorm:"column:alert_rank"`
	RatioKey       string    `gorm:"column:ratio_key"`
}

func (r CCLeakageRow) Key() CCLeakageKey {
	return CCLeakageKey{AlertRank: r.AlertRank, RatioKey: r.RatioKey, Name: r.PartyName, PartyID: r.PartyID}
}

// ListLeakage menghitung indeks kebocoran per distributor (PLANNING §7.1) dalam
// satu query: scan unit (verifikasi konsumen, klaim, test-buy) dalam jendela
// [From, To), dinilai terhadap wilayah resmi yang berlaku SAAT scan.
//
// Custodian sebuah scan = party_id unit_events terakhir unit itu dengan
// occurred_at <= waktu scan; fallback serial_units.custodian_party_id kalau
// unit belum punya event ber-party. Periode custodian dibangun sekali per unit
// (window function lead) lalu di-hash join ke scan; LATERAL per scan lebih
// lambat pada volume besar (lihat laporan EXPLAIN).
// Jadi transfer sah A → B tidak membuat scan lama di wilayah A dihitung untuk B.
// Scan hanya dihitung untuk distributor yang role DISTRIBUTOR-nya aktif saat
// scan; distributor yang role-nya berakhir di tengah jendela tetap tampil
// untuk periode aktifnya.
//
//   - NATIONAL berlaku → tidak pernah di luar wilayah;
//   - SELECTED → di dalam kalau wilayah scan sama dengan, atau turunan dari,
//     salah satu territory_regions (kode Kemendagri berjenjang: 35 ⊃ 35.78);
//   - wilayah scan lebih kasar dari territory (scan 35, territory 35.78) →
//     tidak bisa dinilai, bukan luar wilayah;
//   - tanpa wilayah resmi yang berlaku → tidak dinilai (bukan "semua wilayah");
//   - scan tanpa region_code → tidak dinilai (dihitung terpisah).
//
// Rasio = luar wilayah ÷ scan yang dinilai, dibulatkan 4 desimal SEKALI; nilai
// itu yang dipakai untuk above_baseline, urutan, dan tampilan.
// sample_too_small kalau scan yang dinilai < MinScans.
func (r *CommandCenterRepository) ListLeakage(ctx context.Context, f CCLeakageFilter) ([]CCLeakageRow, error) {
	q := leakageQuery(f)
	var rows []CCLeakageRow
	if err := r.db.WithContext(ctx).Raw(q.String(), q.args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("menghitung indeks kebocoran: %w", err)
	}
	return rows, nil
}

// LeakageSQL mengembalikan SQL + argumen ListLeakage (untuk EXPLAIN di test).
func LeakageSQL(f CCLeakageFilter) (string, []any) {
	q := leakageQuery(f)
	return q.String(), q.args
}

func leakageQuery(f CCLeakageFilter) *sqlq {
	q := &sqlq{}
	q.w(`WITH dist AS (
	  SELECT p.id, p.name
	    FROM parties p
	   WHERE p.archived_at IS NULL
	     AND EXISTS (SELECT 1 FROM party_roles pr
	                  WHERE pr.party_id = p.id AND pr.role_code = 'DISTRIBUTOR'
	                    AND pr.valid_from < ? AND (pr.valid_to IS NULL OR pr.valid_to > ?))`, f.To, f.From)
	if !f.Scope.All {
		q.w(` AND EXISTS (SELECT 1 FROM locations l WHERE l.party_id = p.id AND l.archived_at IS NULL
		                     AND l.id = ANY(CAST(? AS uuid[])))`, uuidArray(f.Scope.LocationIDs))
	}
	if f.PartyID != nil {
		q.w(" AND p.id = ?", *f.PartyID)
	}
	q.w(`
	), coverage AS (
	  -- Mode untuk tampilan: periode wilayah yang beririsan dengan jendela.
	  SELECT d.id AS party_id,
	         CASE WHEN bool_or(pt.coverage_mode = 'NATIONAL') THEN 'NATIONAL'
	              WHEN bool_or(pt.coverage_mode = 'SELECTED') THEN 'SELECTED' END AS coverage_mode
	    FROM dist d
	    LEFT JOIN partner_territories pt
	           ON pt.party_id = d.id AND pt.valid_from < ? AND (pt.valid_to IS NULL OR pt.valid_to > ?)
	   GROUP BY d.id
	), cov AS (
	  -- Satu baris per periode wilayah resmi, dengan daftar kode wilayahnya.
	  SELECT pt.id, pt.party_id, pt.valid_from, pt.valid_to, pt.coverage_mode = 'NATIONAL' AS national,
	         COALESCE(array_agg(tr.region_code) FILTER (WHERE tr.region_code IS NOT NULL), '{}') AS regions
	    FROM partner_territories pt
	    LEFT JOIN territory_regions tr ON tr.territory_id = pt.territory_id
	   WHERE pt.party_id IN (SELECT id FROM dist)
	   GROUP BY pt.id
	), raw_scans AS (
	  SELECT s.id, s.serial_unit_id, s.context, s.region_code, s.occurred_at
	    FROM scan_events s
	   WHERE s.serial_unit_id IS NOT NULL
	     AND s.occurred_at >= ? AND s.occurred_at < ?
	     AND s.context IN ('CONSUMER_VERIFY', 'LOYALTY_CLAIM', 'TEST_BUY')
	), custody AS (
	  -- Periode custodian per unit dari unit_events ber-party: [event, event berikutnya).
	  -- Dihitung sekali per unit yang dipindai (window function + hash join),
	  -- bukan lookup per scan.
	  SELECT ue.serial_unit_id, ue.party_id, ue.occurred_at AS from_at,
	         lead(ue.occurred_at) OVER (PARTITION BY ue.serial_unit_id ORDER BY ue.occurred_at, ue.id) AS to_at
	    FROM unit_events ue
	   WHERE ue.party_id IS NOT NULL
	     AND ue.occurred_at < ?
	     AND ue.serial_unit_id IN (SELECT serial_unit_id FROM raw_scans)
	), scans_in_window AS (
	  -- Custodian saat scan: event ber-party terakhir dengan occurred_at <= scan;
	  -- fallback custodian sekarang kalau unit belum punya event ber-party.
	  SELECT rs.id, COALESCE(c.party_id, su.custodian_party_id) AS party_id,
	         rs.context, rs.region_code, rs.occurred_at
	    FROM raw_scans rs
	    JOIN serial_units su ON su.id = rs.serial_unit_id
	    LEFT JOIN custody c ON c.serial_unit_id = rs.serial_unit_id AND c.from_at <= rs.occurred_at
	                       AND (c.to_at IS NULL OR c.to_at > rs.occurred_at)
	), scans AS (
	  SELECT sw.* FROM scans_in_window sw
	   WHERE sw.party_id IN (SELECT id FROM dist)
	     AND EXISTS (SELECT 1 FROM party_roles pr
	                  WHERE pr.party_id = sw.party_id AND pr.role_code = 'DISTRIBUTOR'
	                    AND pr.valid_from <= sw.occurred_at AND (pr.valid_to IS NULL OR pr.valid_to > sw.occurred_at))
	), matched AS (
	  -- Satu baris per (scan, periode wilayah yang berlaku saat scan).
	  --   inside: periode NATIONAL, atau memuat wilayah scan / leluhurnya
	  --           (35.78.01 → prefix 2/5/8 = 35, 35.78, 35.78.01);
	  --   coarse: wilayah scan adalah leluhur salah satu wilayah resmi
	  --           (scan 35, territory 35.78) sehingga tidak bisa diputuskan.
	  SELECT sc.id, sc.party_id, sc.context, sc.region_code, cv.id AS cov_id,
	         (cv.national OR ARRAY[left(sc.region_code, 2), left(sc.region_code, 5),
	                               left(sc.region_code, 8), sc.region_code] && cv.regions) AS inside,
	         EXISTS (SELECT 1 FROM unnest(cv.regions) AS r(code) WHERE r.code LIKE sc.region_code || '.%') AS coarse
	    FROM scans sc
	    LEFT JOIN cov cv ON cv.party_id = sc.party_id AND cv.valid_from <= sc.occurred_at
	                    AND (cv.valid_to IS NULL OR cv.valid_to > sc.occurred_at)
	), judged AS (
	  SELECT party_id, context, region_code IS NOT NULL AS located,
	         CASE WHEN region_code IS NULL OR count(cov_id) = 0 THEN NULL
	              WHEN bool_or(inside) THEN false
	              WHEN bool_or(coarse) THEN NULL
	              ELSE true
	         END AS out_of_territory
	    FROM matched
	   GROUP BY id, party_id, context, region_code
	), agg AS (
	  SELECT d.id AS party_id, d.name AS party_name, c.coverage_mode,
	         count(j.party_id) AS total_scans,
	         count(*) FILTER (WHERE j.located) AS located_scans,
	         count(*) FILTER (WHERE j.out_of_territory IS NOT NULL) AS judged_scans,
	         count(*) FILTER (WHERE j.out_of_territory) AS out_of_territory,
	         count(*) FILTER (WHERE j.context = 'TEST_BUY') AS test_buy_scans,
	         count(*) FILTER (WHERE j.context = 'TEST_BUY' AND j.out_of_territory) AS test_buy_hits
	    FROM dist d
	    JOIN coverage c ON c.party_id = d.id
	    LEFT JOIN judged j ON j.party_id = d.id
	   GROUP BY d.id, d.name, c.coverage_mode
	), scored AS (
	  SELECT agg.*,
	         CASE WHEN judged_scans > 0 THEN round(out_of_territory::numeric / judged_scans, 4) END AS ratio,
	         judged_scans < ? AS sample_too_small
	    FROM agg
	), flagged AS (
	  SELECT scored.*,
	         (NOT sample_too_small AND ratio > ?) AS above_baseline,
	         ((NOT sample_too_small AND ratio > ?) OR (? AND test_buy_hits > 0)) AS alert
	    FROM scored
	), keyed AS (
	  SELECT flagged.*,
	         CASE WHEN alert THEN 0 ELSE 1 END AS alert_rank,
	         -COALESCE(ratio, -1) AS ratio_sort
	    FROM flagged
	)
	SELECT party_id, party_name, coverage_mode, total_scans, located_scans, judged_scans, out_of_territory,
	       ratio::float8 AS leak_ratio, sample_too_small, test_buy_scans, test_buy_hits,
	       COALESCE(above_baseline, false) AS above_baseline, COALESCE(alert, false) AS alert,
	       alert_rank, ratio_sort::text AS ratio_key
	  FROM keyed
	 WHERE true`,
		f.To, f.From, f.From, f.To, f.To, f.Params.MinScans,
		f.Params.BaselineRatio, f.Params.BaselineRatio, f.Params.TestBuyImmediate)
	if k := f.After; k != nil {
		q.w(" AND (alert_rank, ratio_sort, party_name, party_id) > (?, CAST(? AS numeric), ?, CAST(? AS uuid))",
			k.AlertRank, k.RatioKey, k.Name, k.PartyID)
	}
	q.w(" ORDER BY alert_rank, ratio_sort, party_name, party_id LIMIT ?", f.Limit+1)
	return q
}

// isDecimal: teks angka desimal bertanda (nilai numeric dari cursor).
func isDecimal(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	digits, dot := 0, false
	for i, c := range s {
		switch {
		case c == '-' && i == 0:
		case c == '.' && !dot:
			dot = true
		case c >= '0' && c <= '9':
			digits++
		default:
			return false
		}
	}
	return digits > 0
}
