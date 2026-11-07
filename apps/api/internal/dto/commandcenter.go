package dto

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ------------------------------------------------------------------ ringkasan

// CCSummaryResponse berisi fakta terstruktur untuk kalimat ringkasan
// Command Center. UI yang menyusun kalimatnya (PLANNING §8.2).
type CCSummaryResponse struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Runout      CCSummaryRunout    `json:"runout"`
	Stock       CCSummaryStock     `json:"stock"`
	Anomalies   CCSummaryAnomalies `json:"anomalies"`
	Conflicts   CCSummaryConflicts `json:"conflicts"`
	Stores      CCSummaryStoreSync `json:"stores"`
}

type CCSummaryRunout struct {
	WindowDays            int   `json:"window_days"`
	CriticalItems         int64 `json:"critical_items"`
	CriticalLocations     int64 `json:"critical_locations"`
	WarningItems          int64 `json:"warning_items"`
	WarningLocations      int64 `json:"warning_locations"`
	InsufficientDataItems int64 `json:"insufficient_data_items"`
	// EarliestRunoutDate: tanggal habis paling dekat di antara item CRITICAL/WARNING.
	EarliestRunoutDate *string `json:"earliest_runout_date"`
}

type CCSummaryStock struct {
	OutOfStockItems     int64 `json:"out_of_stock_items"`
	OutOfStockLocations int64 `json:"out_of_stock_locations"`
}

type CCSummaryAnomalies struct {
	OpenTotal      int64            `json:"open_total"`
	OpenBySeverity map[string]int64 `json:"open_by_severity"`
	// TopParty: distributor dengan anomali OPEN terbanyak (nil kalau tidak ada).
	TopParty *CCPartyCount `json:"top_party"`
}

type CCPartyCount struct {
	PartyID   uuid.UUID `json:"party_id"`
	PartyName string    `json:"party_name"`
	OpenCount int64     `json:"open_count"`
}

type CCSummaryConflicts struct {
	OpenTotal int64 `json:"open_total"`
}

type CCSummaryStoreSync struct {
	Total   int64 `json:"total"`
	Offline int64 `json:"offline"`
}

// ------------------------------------------------------------------ stok

type CCStockItem struct {
	LocationID      uuid.UUID  `json:"location_id"`
	LocationCode    string     `json:"location_code"`
	LocationName    string     `json:"location_name"`
	LocationType    string     `json:"location_type"`
	SyncState       string     `json:"sync_state"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	ProductID       uuid.UUID  `json:"product_id"`
	SKU             string     `json:"sku"`
	ProductName     string     `json:"product_name"`
	PhysicalQty     int64      `json:"physical_qty"`
	ReservedQty     int64      `json:"reserved_qty"`
	QuarantineQty   int64      `json:"quarantine_qty"`
	AvailableQty    int64      `json:"available_qty"`
	// IsEstimate: stok di lokasi bukan milik sendiri (gudang mitra/marketplace)
	// hanya perkiraan dari sell-in yang tercatat.
	IsEstimate bool      `json:"is_estimate"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ------------------------------------------------------------------ runout

type CCRunoutItem struct {
	LocationID   uuid.UUID `json:"location_id"`
	LocationCode string    `json:"location_code"`
	LocationName string    `json:"location_name"`
	LocationType string    `json:"location_type"`
	ProductID    uuid.UUID `json:"product_id"`
	SKU          string    `json:"sku"`
	ProductName  string    `json:"product_name"`
	AvailableQty int64     `json:"available_qty"`
	IsEstimate   bool      `json:"is_estimate"`
	// Sold28d: unit terjual (DIRECT_SALE + COMMIT_SALE) dalam jendela 28 hari.
	SoldQty int64 `json:"sold_qty"`
	// HistoryDays: pembagi rata-rata = hari sejak mutasi pertama (1..28);
	// nil tanpa penjualan.
	HistoryDays     *int     `json:"history_days"`
	AvgDailySales   *float64 `json:"avg_daily_sales"`
	DaysOfInventory *float64 `json:"days_of_inventory"`
	RunoutDate      *string  `json:"runout_date"`
	Level           string   `json:"level"`
	Reasons         []string `json:"reasons"`
	HasPolicy       bool     `json:"has_policy"`
	MinQty          *int64   `json:"min_qty"`
	LeadTimeDays    *int     `json:"lead_time_days"`
	SafetyDays      *int     `json:"safety_days"`
}

type CCRunoutResponse struct {
	Items       []CCRunoutItem `json:"items"`
	NextCursor  *string        `json:"next_cursor"`
	WindowDays  int            `json:"window_days"`
	GeneratedAt time.Time      `json:"generated_at"`
}

// ------------------------------------------------------------------ anomali

type CCAnomalyItem struct {
	ID             uuid.UUID       `json:"id"`
	RuleCode       string          `json:"rule_code"`
	RuleName       string          `json:"rule_name"`
	Severity       string          `json:"severity"`
	Status         string          `json:"status"`
	SubjectType    string          `json:"subject_type"`
	SubjectID      *uuid.UUID      `json:"subject_id"`
	PartyID        *uuid.UUID      `json:"party_id"`
	PartyName      *string         `json:"party_name"`
	RegionCode     *string         `json:"region_code"`
	RegionName     *string         `json:"region_name"`
	HitCount       int             `json:"hit_count"`
	Evidence       json.RawMessage `json:"evidence"`
	FirstSeenAt    time.Time       `json:"first_seen_at"`
	LastSeenAt     time.Time       `json:"last_seen_at"`
	ResolvedBy     *uuid.UUID      `json:"resolved_by"`
	ResolvedAt     *time.Time      `json:"resolved_at"`
	ResolutionNote *string         `json:"resolution_note"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// CCAnomalyUpdateRequest: transisi status anomali. note wajib untuk
// RESOLVED dan FALSE_POSITIVE (dicek di service).
type CCAnomalyUpdateRequest struct {
	Status string  `json:"status" binding:"required,oneof=ACK RESOLVED FALSE_POSITIVE"`
	Note   *string `json:"note" binding:"omitempty,max=1000"`
}

// ------------------------------------------------------------------ konflik

type CCConflictItem struct {
	ID               uuid.UUID       `json:"id"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status"`
	LocationID       uuid.UUID       `json:"location_id"`
	LocationCode     string          `json:"location_code"`
	LocationName     string          `json:"location_name"`
	ProductID        uuid.UUID       `json:"product_id"`
	SKU              string          `json:"sku"`
	ProductName      string          `json:"product_name"`
	Qty              *int64          `json:"qty"`
	PosTransactionID *uuid.UUID      `json:"pos_transaction_id"`
	ReservationID    *uuid.UUID      `json:"reservation_id"`
	OrderID          *uuid.UUID      `json:"order_id"`
	SerialUnitID     *uuid.UUID      `json:"serial_unit_id"`
	Resolution       *string         `json:"resolution"`
	Details          json.RawMessage `json:"details"`
	ResolvedBy       *uuid.UUID      `json:"resolved_by"`
	ResolvedAt       *time.Time      `json:"resolved_at"`
	CreatedAt        time.Time       `json:"created_at"`
}

// ------------------------------------------------------------------ kebocoran wilayah

type CCLeakageItem struct {
	PartyID   uuid.UUID `json:"party_id"`
	PartyName string    `json:"party_name"`
	// CoverageMode: NATIONAL, SELECTED, atau nil kalau distributor belum punya
	// wilayah resmi yang berlaku (rasio tidak dihitung).
	CoverageMode *string `json:"coverage_mode"`
	// TotalScans: scan unit yang custodian-nya distributor ini.
	TotalScans int64 `json:"total_scans"`
	// LocatedScans: scan yang wilayahnya diketahui.
	LocatedScans   int64 `json:"located_scans"`
	UnlocatedScans int64 `json:"unlocated_scans"`
	// JudgedScans: scan yang bisa dinilai dalam/luar wilayah (dasar rasio).
	JudgedScans int64 `json:"judged_scans"`
	// UnjudgedScans: scan berwilayah yang tidak bisa dinilai: wilayah scan
	// lebih kasar dari wilayah resmi, atau tanpa wilayah resmi saat scan.
	UnjudgedScans  int64    `json:"unjudged_scans"`
	OutOfTerritory *int64   `json:"out_of_territory_scans"`
	LeakRatio      *float64 `json:"leak_ratio"`
	SampleTooSmall bool     `json:"sample_too_small"`
	TestBuyScans   int64    `json:"test_buy_scans"`
	TestBuyHits    int64    `json:"test_buy_hits"`
	AboveBaseline  bool     `json:"above_baseline"`
	Alert          bool     `json:"alert"`
}

type CCLeakageResponse struct {
	Items         []CCLeakageItem `json:"items"`
	NextCursor    *string         `json:"next_cursor"`
	WindowDays    int             `json:"window_days"`
	From          time.Time       `json:"from"`
	To            time.Time       `json:"to"`
	MinScans      int64           `json:"min_scans"`
	BaselineRatio float64         `json:"baseline_ratio"`
}
