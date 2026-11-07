package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/dto"
	"mganik/api/internal/model"
	"mganik/api/internal/pagination"
	"mganik/api/internal/repository"
)

const (
	// PermCCRead membaca seluruh Command Center.
	PermCCRead = "cc.read"
	// PermAnomalyResolve menindaklanjuti anomali (ACK/RESOLVED/FALSE_POSITIVE).
	PermAnomalyResolve = "anomaly.resolve"

	// RunoutWindowDays: jendela rata-rata penjualan harian (PLANNING §7.5).
	RunoutWindowDays = 28
	// LeakageDefaultDays: jendela default indeks kebocoran wilayah.
	LeakageDefaultDays = 30
	// LeakageMaxDays: jendela terpanjang yang boleh diminta. Indeks kebocoran
	// dihitung sinkron per request (agregasi scan + custodian saat scan), jadi
	// jendela dibatasi 90 hari supaya biaya query terkendali. Trade-off: untuk
	// produksi, perhitungan dipindah ke worker yang mengisi tabel/materialized
	// view harian per distributor, dan endpoint ini hanya membacanya.
	LeakageMaxDays = 90

	// Default parameter indeks kebocoran kalau anomaly_rules tidak mengaturnya.
	defaultLeakMinScans      = 20
	defaultLeakBaselineRatio = 0.05

	cursorStock     = "cc.stock.v1"
	cursorRunout    = "cc.runout.v1"
	cursorAnomalies = "cc.anomalies.v1"
	cursorConflicts = "cc.conflicts.v1"
	cursorLeakage   = "cc.leakage.v1"
)

// Status anomali dan transisi yang diizinkan (to → from yang sah).
const (
	AnomalyOpen          = "OPEN"
	AnomalyAck           = "ACK"
	AnomalyResolved      = "RESOLVED"
	AnomalyFalsePositive = "FALSE_POSITIVE"
)

var anomalyTransitions = map[string][]string{
	AnomalyAck:           {AnomalyOpen},
	AnomalyResolved:      {AnomalyOpen, AnomalyAck},
	AnomalyFalsePositive: {AnomalyOpen, AnomalyAck},
}

// ErrInvalidTransition: transisi status anomali tidak sah dari status sekarang.
func errInvalidTransition(from, to string) *apperror.Error {
	return apperror.New(http.StatusConflict, "invalid_transition", "Transisi tidak valid",
		fmt.Sprintf("Anomali berstatus %s tidak bisa diubah menjadi %s.", from, to))
}

var errAnomalyNotFound = apperror.New(http.StatusNotFound, "anomaly_not_found",
	"Anomali tidak ditemukan", "Anomali tidak ada atau di luar cakupan akun ini.")

type ccStore interface {
	ListStock(ctx context.Context, f repository.CCStockFilter) ([]repository.CCStockRow, error)
	ListRunout(ctx context.Context, f repository.CCRunoutFilter) ([]repository.CCRunoutRow, error)
	RunoutSummary(ctx context.Context, f repository.CCRunoutFilter) (repository.CCRunoutSummary, error)
	SummaryCounts(ctx context.Context, s repository.CCScope) (repository.CCSummaryCounts, error)
	TopAnomalyParty(ctx context.Context, s repository.CCScope) (*repository.CCPartyCountRow, error)
	ListAnomalies(ctx context.Context, f repository.CCAnomalyFilter) ([]repository.CCAnomalyRow, error)
	GetAnomaly(ctx context.Context, db *gorm.DB, id uuid.UUID, s repository.CCScope) (*repository.CCAnomalyRow, error)
	LockAnomaly(ctx context.Context, tx *gorm.DB, id uuid.UUID, s repository.CCScope) (*repository.AnomalyState, error)
	TransitionAnomaly(ctx context.Context, tx *gorm.DB, t repository.AnomalyTransition) (bool, error)
	InsertAudit(ctx context.Context, tx *gorm.DB, a *model.AuditLog) error
	Transaction(ctx context.Context, fn func(tx *gorm.DB) error) error
	ListConflicts(ctx context.Context, f repository.CCConflictFilter) ([]repository.CCConflictRow, error)
	OutOfTerritoryParams(ctx context.Context) (string, error)
	ListLeakage(ctx context.Context, f repository.CCLeakageFilter) ([]repository.CCLeakageRow, error)
}

// CommandCenterService adalah read model Pilar D plus antrean kerja anomali.
// Otorisasi (permission + scope lokasi) selalu dicek di sini, bukan hanya di
// route.
type CommandCenterService struct {
	store ccStore
	clock clock.Clock
}

func NewCommandCenterService(store ccStore, clk clock.Clock) *CommandCenterService {
	if clk == nil {
		clk = clock.Real{}
	}
	return &CommandCenterService{store: store, clock: clk}
}

// Page adalah halaman query: Limit dan Cursor mentah dari query string.
type Page struct {
	Limit  string
	Cursor string
}

// authorize adalah SATU-SATUNYA tempat pemeriksaan akses Command Center:
// pemanggil wajib login dan punya perm; scope lokasi diturunkan dari mode
// eksplisit Principal.ScopeAll (daftar lokasi kosong bukan berarti semua);
// filter lokasi (bila ada) di luar scope ditolak 403 supaya tidak tertukar
// dengan "lokasi ada tetapi kosong". Saat auth.Principal punya scope per
// permission (CanAt), cukup fungsi ini yang diganti.
func authorize(ctx context.Context, perm string, locationFilter *uuid.UUID) (*auth.Principal, repository.CCScope, error) {
	p := auth.FromContext(ctx)
	if p == nil {
		return nil, repository.CCScope{}, apperror.ErrUnauthenticated
	}
	if !p.Can(perm) {
		return nil, repository.CCScope{}, apperror.ErrForbidden
	}
	if locationFilter != nil && !p.CanAccessLocation(*locationFilter) {
		return nil, repository.CCScope{}, apperror.ErrForbidden
	}
	if p.ScopeAll {
		return p, repository.CCScope{All: true}, nil
	}
	ids := append([]uuid.UUID{}, p.LocationIDs...)
	return p, repository.CCScope{LocationIDs: ids}, nil
}

// utc menyeragamkan semua waktu di respons ke UTC (akhiran Z), sama dengan
// generated_at, apa pun zona waktu proses atau driver.
func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// ------------------------------------------------------------------ ringkasan

func (s *CommandCenterService) Summary(ctx context.Context) (dto.CCSummaryResponse, error) {
	_, scope, err := authorize(ctx, PermCCRead, nil)
	if err != nil {
		return dto.CCSummaryResponse{}, err
	}
	now := utc(s.clock.Now())
	// Ringkasan hanya lokasi milik sendiri, konsisten dengan out_of_stock_*;
	// stok mitra hanya estimasi dan tidak ikut kalimat ringkasan.
	runout, err := s.store.RunoutSummary(ctx, repository.CCRunoutFilter{
		Now: now, WindowDays: RunoutWindowDays, Scope: scope, OwnOnly: true})
	if err != nil {
		return dto.CCSummaryResponse{}, err
	}
	counts, err := s.store.SummaryCounts(ctx, scope)
	if err != nil {
		return dto.CCSummaryResponse{}, err
	}
	top, err := s.store.TopAnomalyParty(ctx, scope)
	if err != nil {
		return dto.CCSummaryResponse{}, err
	}
	out := dto.CCSummaryResponse{
		GeneratedAt: now,
		Runout: dto.CCSummaryRunout{
			WindowDays:            RunoutWindowDays,
			CriticalItems:         runout.CriticalItems,
			CriticalLocations:     runout.CriticalLocations,
			WarningItems:          runout.WarningItems,
			WarningLocations:      runout.WarningLocations,
			InsufficientDataItems: runout.InsufficientDataItems,
			EarliestRunoutDate:    runout.EarliestRunoutDate,
		},
		Stock: dto.CCSummaryStock{
			OutOfStockItems:     counts.OutOfStockItems,
			OutOfStockLocations: counts.OutOfStockLocations,
		},
		Anomalies: dto.CCSummaryAnomalies{
			OpenTotal: counts.AnomaliesOpen,
			OpenBySeverity: map[string]int64{
				"LOW": counts.OpenLow, "MEDIUM": counts.OpenMedium,
				"HIGH": counts.OpenHigh, "CRITICAL": counts.OpenCritical,
			},
		},
		Conflicts: dto.CCSummaryConflicts{OpenTotal: counts.ConflictsOpen},
		Stores:    dto.CCSummaryStoreSync{Total: counts.StoresTotal, Offline: counts.StoresOffline},
	}
	if top != nil {
		out.Anomalies.TopParty = &dto.CCPartyCount{PartyID: top.PartyID, PartyName: top.PartyName, OpenCount: top.OpenCount}
	}
	return out, nil
}

// ------------------------------------------------------------------ stok

type StockQuery struct {
	Page
	LocationType *string
	LocationID   *uuid.UUID
	ProductID    *uuid.UUID
}

func (s *CommandCenterService) Stock(ctx context.Context, in StockQuery) (pagination.Page[dto.CCStockItem], error) {
	var zero pagination.Page[dto.CCStockItem]
	_, scope, err := authorize(ctx, PermCCRead, in.LocationID)
	if err != nil {
		return zero, err
	}
	var key repository.CCStockKey
	req, err := pagination.Parse(in.Limit, in.Cursor, cursorStock, &key)
	if err != nil {
		return zero, err
	}
	f := repository.CCStockFilter{Scope: scope, LocationType: in.LocationType, LocationID: in.LocationID,
		ProductID: in.ProductID, Limit: req.Limit}
	if req.HasCursor {
		f.After = &key
	}
	rows, err := s.store.ListStock(ctx, f)
	if err != nil {
		return zero, err
	}
	page, err := pagination.Trim(rows, req.Limit, cursorStock, func(r repository.CCStockRow) any { return r.Key() })
	if err != nil {
		return zero, err
	}
	out := pagination.Page[dto.CCStockItem]{Items: make([]dto.CCStockItem, len(page.Items)), NextCursor: page.NextCursor}
	for i, r := range page.Items {
		out.Items[i] = dto.CCStockItem{
			LocationID: r.LocationID, LocationCode: r.LocationCode, LocationName: r.LocationName,
			LocationType: r.LocationType, SyncState: r.SyncState, LastHeartbeatAt: utcPtr(r.LastHeartbeatAt),
			ProductID: r.ProductID, SKU: r.SKU, ProductName: r.ProductName,
			PhysicalQty: r.PhysicalQty, ReservedQty: r.ReservedQty, QuarantineQty: r.QuarantineQty,
			AvailableQty: r.AvailableQty, IsEstimate: r.IsEstimate, UpdatedAt: utc(r.UpdatedAt),
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ runout

type RunoutQuery struct {
	Page
	LocationType *string
	LocationID   *uuid.UUID
	ProductID    *uuid.UUID
	Level        *string
}

func (s *CommandCenterService) Runout(ctx context.Context, in RunoutQuery) (dto.CCRunoutResponse, error) {
	_, scope, err := authorize(ctx, PermCCRead, in.LocationID)
	if err != nil {
		return dto.CCRunoutResponse{}, err
	}
	var key repository.CCRunoutKey
	req, err := pagination.Parse(in.Limit, in.Cursor, cursorRunout, &key)
	if err != nil {
		return dto.CCRunoutResponse{}, err
	}
	now := utc(s.clock.Now())
	f := repository.CCRunoutFilter{
		Now: now, WindowDays: RunoutWindowDays, Scope: scope,
		LocationType: in.LocationType, LocationID: in.LocationID, ProductID: in.ProductID, Level: in.Level,
		Limit: req.Limit,
	}
	if req.HasCursor {
		f.After = &key
	}
	rows, err := s.store.ListRunout(ctx, f)
	if err != nil {
		return dto.CCRunoutResponse{}, err
	}
	page, err := pagination.Trim(rows, req.Limit, cursorRunout, func(r repository.CCRunoutRow) any { return r.Key() })
	if err != nil {
		return dto.CCRunoutResponse{}, err
	}
	out := dto.CCRunoutResponse{
		Items: make([]dto.CCRunoutItem, len(page.Items)), NextCursor: page.NextCursor,
		WindowDays: RunoutWindowDays, GeneratedAt: now,
	}
	for i, r := range page.Items {
		out.Items[i] = dto.CCRunoutItem{
			LocationID: r.LocationID, LocationCode: r.LocationCode, LocationName: r.LocationName,
			LocationType: r.LocationType, ProductID: r.ProductID, SKU: r.SKU, ProductName: r.ProductName,
			AvailableQty: r.AvailableQty, IsEstimate: r.IsEstimate, SoldQty: r.SoldQty,
			HistoryDays: r.HistoryDays, AvgDailySales: r.AvgDailySales, DaysOfInventory: r.DOI, RunoutDate: r.RunoutDate,
			Level: r.Level, Reasons: r.ReasonList(), HasPolicy: r.HasPolicy,
			MinQty: r.MinQty, LeadTimeDays: r.LeadTimeDays, SafetyDays: r.SafetyDays,
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ anomali

type AnomalyQuery struct {
	Page
	Status   *string
	Severity *string
	RuleCode *string
	PartyID  *uuid.UUID
}

func (s *CommandCenterService) Anomalies(ctx context.Context, in AnomalyQuery) (pagination.Page[dto.CCAnomalyItem], error) {
	var zero pagination.Page[dto.CCAnomalyItem]
	_, scope, err := authorize(ctx, PermCCRead, nil)
	if err != nil {
		return zero, err
	}
	var key pagination.TimeID
	req, err := pagination.Parse(in.Limit, in.Cursor, cursorAnomalies, &key)
	if err != nil {
		return zero, err
	}
	f := repository.CCAnomalyFilter{Scope: scope, Status: in.Status, Severity: in.Severity,
		RuleCode: in.RuleCode, PartyID: in.PartyID, Limit: req.Limit}
	if req.HasCursor {
		f.After = &repository.CCTimeIDKey{At: key.At, ID: key.ID}
	}
	rows, err := s.store.ListAnomalies(ctx, f)
	if err != nil {
		return zero, err
	}
	page, err := pagination.Trim(rows, req.Limit, cursorAnomalies, func(r repository.CCAnomalyRow) any {
		return pagination.TimeID{At: r.CreatedAt, ID: r.ID}
	})
	if err != nil {
		return zero, err
	}
	out := pagination.Page[dto.CCAnomalyItem]{Items: make([]dto.CCAnomalyItem, len(page.Items)), NextCursor: page.NextCursor}
	for i, r := range page.Items {
		out.Items[i] = anomalyDTO(r)
	}
	return out, nil
}

func anomalyDTO(r repository.CCAnomalyRow) dto.CCAnomalyItem {
	evidence := json.RawMessage(r.Evidence)
	if len(evidence) == 0 {
		evidence = json.RawMessage(`{}`)
	}
	return dto.CCAnomalyItem{
		ID: r.ID, RuleCode: r.RuleCode, RuleName: r.RuleName, Severity: r.Severity, Status: r.Status,
		SubjectType: r.SubjectType, SubjectID: r.SubjectID, PartyID: r.PartyID, PartyName: r.PartyName,
		RegionCode: r.RegionCode, RegionName: r.RegionName, HitCount: r.HitCount, Evidence: evidence,
		FirstSeenAt: utc(r.FirstSeenAt), LastSeenAt: utc(r.LastSeenAt), ResolvedBy: r.ResolvedBy,
		ResolvedAt: utcPtr(r.ResolvedAt), ResolutionNote: r.ResolutionNote, Version: r.Version,
		CreatedAt: utc(r.CreatedAt), UpdatedAt: utc(r.UpdatedAt),
	}
}

// AnomalyUpdate adalah permintaan transisi status yang sudah lolos validasi DTO.
type AnomalyUpdate struct {
	ID        uuid.UUID
	Status    string
	Note      *string
	RequestID string
}

// UpdateAnomaly memindahkan status anomali: OPEN→ACK, OPEN/ACK→RESOLVED,
// OPEN/ACK→FALSE_POSITIVE. Baris dikunci FOR UPDATE lalu di-UPDATE bersyarat
// `WHERE status = ANY(from)`; dari dua permintaan bersamaan hanya satu yang
// menang, yang lain mendapat 409. Perubahan dan audit_logs ditulis dalam satu
// transaksi. Catatan pada ACK hanya masuk audit_logs.reason; resolution_note
// anomali hanya diisi saat ditutup (RESOLVED/FALSE_POSITIVE).
func (s *CommandCenterService) UpdateAnomaly(ctx context.Context, in AnomalyUpdate) (dto.CCAnomalyItem, error) {
	p, scope, err := authorize(ctx, PermAnomalyResolve, nil)
	if err != nil {
		return dto.CCAnomalyItem{}, err
	}
	from, ok := anomalyTransitions[in.Status]
	if !ok {
		return dto.CCAnomalyItem{}, apperror.Validation(apperror.FieldError{
			Field: "status", Code: "oneof", Message: "harus salah satu dari: ACK RESOLVED FALSE_POSITIVE"})
	}
	terminal := in.Status == AnomalyResolved || in.Status == AnomalyFalsePositive
	if terminal && in.Note == nil {
		return dto.CCAnomalyItem{}, apperror.Validation(apperror.FieldError{
			Field: "note", Code: "required", Message: "wajib diisi untuk menutup anomali (RESOLVED/FALSE_POSITIVE)"})
	}

	var actor *uuid.UUID
	if id := actorID(p); id != uuid.Nil {
		actor = &id
	}
	now := utc(s.clock.Now())
	var result *repository.CCAnomalyRow
	err = s.store.Transaction(ctx, func(tx *gorm.DB) error {
		cur, err := s.store.LockAnomaly(ctx, tx, in.ID, scope)
		if err != nil {
			return err
		}
		if cur == nil {
			return errAnomalyNotFound
		}
		if !contains(from, cur.Status) {
			return errInvalidTransition(cur.Status, in.Status)
		}
		changed, err := s.store.TransitionAnomaly(ctx, tx, repository.AnomalyTransition{
			ID: in.ID, From: from, To: in.Status, Terminal: terminal, Actor: actor, Note: in.Note, At: now,
		})
		if err != nil {
			return err
		}
		if !changed {
			// Tidak terjadi selama baris terkunci; tetap dijaga sebagai konflik.
			return errInvalidTransition(cur.Status, in.Status)
		}
		entityID := in.ID
		after := map[string]any{"status": in.Status, "version": cur.Version + 1}
		if terminal {
			after["resolution_note"] = in.Note
		}
		audit := &model.AuditLog{
			ID: uuid.Must(uuid.NewV7()), ActorType: actorType(p), ActorID: actor,
			Action: "anomaly.status_changed", Entity: "anomalies", EntityID: &entityID,
			Before: model.MustJSON(map[string]any{"status": cur.Status, "version": cur.Version}),
			After:  model.MustJSON(after),
			Reason: in.Note, At: now,
		}
		if in.RequestID != "" {
			rid := in.RequestID
			audit.RequestID = &rid
		}
		if err := s.store.InsertAudit(ctx, tx, audit); err != nil {
			return err
		}
		result, err = s.store.GetAnomaly(ctx, tx, in.ID, scope)
		return err
	})
	if err != nil {
		return dto.CCAnomalyItem{}, err
	}
	if result == nil {
		return dto.CCAnomalyItem{}, errAnomalyNotFound
	}
	return anomalyDTO(*result), nil
}

func actorID(p *auth.Principal) uuid.UUID {
	if p.ActorID != uuid.Nil {
		return p.ActorID
	}
	if p.Kind == auth.KindStaff {
		return p.ID
	}
	return uuid.Nil
}

func actorType(p *auth.Principal) string {
	switch p.Kind {
	case auth.KindDevice:
		if p.ActorID != uuid.Nil {
			return model.ActorUser
		}
		return model.ActorDevice
	case auth.KindConsumer:
		return model.ActorConsumer
	default:
		return model.ActorUser
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ konflik

type ConflictQuery struct {
	Page
	Status     *string
	Kind       *string
	LocationID *uuid.UUID
}

func (s *CommandCenterService) Conflicts(ctx context.Context, in ConflictQuery) (pagination.Page[dto.CCConflictItem], error) {
	var zero pagination.Page[dto.CCConflictItem]
	_, scope, err := authorize(ctx, PermCCRead, in.LocationID)
	if err != nil {
		return zero, err
	}
	var key pagination.TimeID
	req, err := pagination.Parse(in.Limit, in.Cursor, cursorConflicts, &key)
	if err != nil {
		return zero, err
	}
	f := repository.CCConflictFilter{Scope: scope, Status: in.Status, Kind: in.Kind, LocationID: in.LocationID, Limit: req.Limit}
	if req.HasCursor {
		f.After = &repository.CCTimeIDKey{At: key.At, ID: key.ID}
	}
	rows, err := s.store.ListConflicts(ctx, f)
	if err != nil {
		return zero, err
	}
	page, err := pagination.Trim(rows, req.Limit, cursorConflicts, func(r repository.CCConflictRow) any {
		return pagination.TimeID{At: r.CreatedAt, ID: r.ID}
	})
	if err != nil {
		return zero, err
	}
	out := pagination.Page[dto.CCConflictItem]{Items: make([]dto.CCConflictItem, len(page.Items)), NextCursor: page.NextCursor}
	for i, r := range page.Items {
		details := json.RawMessage(r.Details)
		if len(details) == 0 {
			details = json.RawMessage(`{}`)
		}
		out.Items[i] = dto.CCConflictItem{
			ID: r.ID, Kind: r.Kind, Status: r.Status, LocationID: r.LocationID, LocationCode: r.LocationCode,
			LocationName: r.LocationName, ProductID: r.ProductID, SKU: r.SKU, ProductName: r.ProductName,
			Qty: r.Qty, PosTransactionID: r.PosTransactionID, ReservationID: r.ReservationID, OrderID: r.OrderID,
			SerialUnitID: r.SerialUnitID, Resolution: r.Resolution, Details: details,
			ResolvedBy: r.ResolvedBy, ResolvedAt: utcPtr(r.ResolvedAt), CreatedAt: utc(r.CreatedAt),
		}
	}
	return out, nil
}

// ------------------------------------------------------------------ kebocoran wilayah

type LeakageQuery struct {
	Page
	// Days: jendela waktu (default 30).
	Days    int
	PartyID *uuid.UUID
}

// Leakage menghitung indeks kebocoran secara sinkron (lihat LeakageMaxDays
// untuk batas jendela dan jalur produksi lewat worker/materialized view).
func (s *CommandCenterService) Leakage(ctx context.Context, in LeakageQuery) (dto.CCLeakageResponse, error) {
	_, scope, err := authorize(ctx, PermCCRead, nil)
	if err != nil {
		return dto.CCLeakageResponse{}, err
	}
	var key repository.CCLeakageKey
	req, err := pagination.Parse(in.Limit, in.Cursor, cursorLeakage, &key)
	if err != nil {
		return dto.CCLeakageResponse{}, err
	}
	days := in.Days
	if days == 0 {
		days = LeakageDefaultDays
	}
	if days < 1 || days > LeakageMaxDays {
		return dto.CCLeakageResponse{}, apperror.Validation(apperror.FieldError{
			Field: "days", Code: "out_of_range", Message: fmt.Sprintf("harus antara 1 dan %d", LeakageMaxDays)})
	}
	raw, err := s.store.OutOfTerritoryParams(ctx)
	if err != nil {
		return dto.CCLeakageResponse{}, err
	}
	params := parseLeakageParams(raw)

	to := utc(s.clock.Now())
	from := to.Add(-time.Duration(days) * 24 * time.Hour)
	f := repository.CCLeakageFilter{From: from, To: to, Scope: scope, PartyID: in.PartyID, Params: params, Limit: req.Limit}
	if req.HasCursor {
		f.After = &key
	}
	rows, err := s.store.ListLeakage(ctx, f)
	if err != nil {
		return dto.CCLeakageResponse{}, err
	}
	page, err := pagination.Trim(rows, req.Limit, cursorLeakage, func(r repository.CCLeakageRow) any { return r.Key() })
	if err != nil {
		return dto.CCLeakageResponse{}, err
	}
	out := dto.CCLeakageResponse{
		Items: make([]dto.CCLeakageItem, len(page.Items)), NextCursor: page.NextCursor,
		WindowDays: days, From: from, To: to, MinScans: params.MinScans, BaselineRatio: params.BaselineRatio,
	}
	for i, r := range page.Items {
		item := dto.CCLeakageItem{
			PartyID: r.PartyID, PartyName: r.PartyName, CoverageMode: r.CoverageMode,
			TotalScans: r.TotalScans, LocatedScans: r.LocatedScans, UnlocatedScans: r.TotalScans - r.LocatedScans,
			JudgedScans: r.JudgedScans, UnjudgedScans: r.LocatedScans - r.JudgedScans,
			LeakRatio: r.LeakRatio, SampleTooSmall: r.SampleTooSmall,
			TestBuyScans: r.TestBuyScans, TestBuyHits: r.TestBuyHits,
			AboveBaseline: r.AboveBaseline, Alert: r.Alert,
		}
		// Jumlah luar wilayah hanya bermakna kalau ada scan yang bisa dinilai.
		if r.JudgedScans > 0 {
			n := r.OutOfTerritory
			item.OutOfTerritory = &n
		}
		out.Items[i] = item
	}
	return out, nil
}

// parseLeakageParams membaca params OUT_OF_TERRITORY; nilai yang hilang atau
// tidak masuk akal diganti default supaya endpoint tetap jalan.
func parseLeakageParams(raw string) repository.LeakageParams {
	out := repository.LeakageParams{MinScans: defaultLeakMinScans, BaselineRatio: defaultLeakBaselineRatio, TestBuyImmediate: true}
	if raw == "" {
		return out
	}
	var p struct {
		MinScans         *float64 `json:"leak_index_min_scans"`
		BaselineRatio    *float64 `json:"leak_index_baseline_ratio"`
		TestBuyImmediate *bool    `json:"test_buy_immediate"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return out
	}
	if p.MinScans != nil && *p.MinScans >= 1 && *p.MinScans <= 1e6 {
		out.MinScans = int64(*p.MinScans)
	}
	if p.BaselineRatio != nil && *p.BaselineRatio >= 0 && *p.BaselineRatio <= 1 {
		out.BaselineRatio = *p.BaselineRatio
	}
	if p.TestBuyImmediate != nil {
		out.TestBuyImmediate = *p.TestBuyImmediate
	}
	return out
}
