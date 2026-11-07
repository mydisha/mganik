package postest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/controller"
	"mganik/api/internal/domain"
	"mganik/api/internal/router"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	controller.SetupValidator()
	testutil.Main(m)
}

// testNow: jam awal semua test (price list POS v2 berlaku sejak 1 Nov 2026).
var testNow = time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)

var (
	jakselID   = seed.LocationID(seed.StoreJakselCode)
	bandungID  = seed.LocationID(seed.StoreBandungCode)
	centralID  = seed.LocationID(seed.CentralWHCode)
	hubID      = seed.LocationID(seed.HubSurabayaCode)
	sku1ID     = seed.ProductID(seed.SKU1)
	sku2ID     = seed.ProductID(seed.SKU2)
	deviceID   = seed.PosDeviceID()
	kasirID    = seed.UserID("kasir.jaksel@mganik.test")
	managerID  = seed.UserID("kepala.jaksel@mganik.test")
	opsID      = seed.UserID("ops@mganik.test")
	priceSKU1  = int64(47500) // POS v2
	priceSKU2  = int64(33000) // POS v2
	quietLog   = slog.New(slog.NewTextHandler(io.Discard, nil))
	fastArgon2 = domain.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
)

// env: database ber-seed demo, jam palsu, engine penuh (resolver token
// device sungguhan), dan PosService yang berbagi jam yang sama.
type env struct {
	t      *testing.T
	db     *gorm.DB
	clk    *clock.Fake
	cfg    config.Config
	engine http.Handler
	pos    *service.PosService
	key    []byte
	token  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, config.POSConfig{})
}

// newEnvWith: env dengan konfigurasi POS khusus (nilai nol = default).
func newEnvWith(t *testing.T, posCfg config.POSConfig) *env {
	t.Helper()
	db := testutil.NewDB(t)
	clk := clock.NewFake(testNow)
	if _, err := seed.Run(context.Background(), db, seed.Options{
		Enabled: true, Pepper: []byte("pepper-test-32-byte-xxxxxxxxxxxx"), DemoPassword: "kata-sandi-demo-test",
		PasswordParams: fastArgon2, Log: quietLog, Clock: clk,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{AppEnv: config.EnvTest, MaxBodyBytes: 1 << 20, POS: posCfg}
	deps := router.Deps{Config: cfg, DB: db, Log: quietLog, Clock: clk}
	engine, err := router.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	pos, err := router.NewPosService(deps)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, db: db, clk: clk, cfg: cfg, engine: engine, pos: pos,
		key: seed.DemoDeviceSigningKey(), token: seed.DemoDeviceToken()}
}

func (e *env) must(err error) {
	e.t.Helper()
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.must(e.db.Exec(sql, args...).Error)
}

func (e *env) count(sql string, args ...any) int64 {
	e.t.Helper()
	var n int64
	e.must(e.db.Raw(sql, args...).Scan(&n).Error)
	return n
}

func (e *env) str(sql string, args ...any) string {
	e.t.Helper()
	var s string
	e.must(e.db.Raw(sql, args...).Scan(&s).Error)
	return s
}

type balance struct{ Physical, Reserved, Available int64 }

func (e *env) balance(loc, prod uuid.UUID) balance {
	e.t.Helper()
	var b struct{ PhysicalQty, ReservedQty, AvailableQty int64 }
	e.must(e.db.Raw(`SELECT physical_qty, reserved_qty, available_qty FROM inventory_balances
	                  WHERE location_id = ? AND product_id = ?`, loc, prod).Scan(&b).Error)
	return balance{b.PhysicalQty, b.ReservedQty, b.AvailableQty}
}

// assertLedgerMatchesBalances: SUM(delta) ledger = saldo untuk setiap
// (lokasi, produk) (PLANNING §6.6).
func (e *env) assertLedgerMatchesBalances() {
	e.t.Helper()
	n := e.count(`
		SELECT count(*) FROM inventory_balances b
		  LEFT JOIN (SELECT location_id, product_id, sum(physical_delta) p, sum(reserved_delta) r, sum(quarantine_delta) q
		               FROM inventory_movements GROUP BY 1, 2) m USING (location_id, product_id)
		 WHERE b.physical_qty <> COALESCE(m.p, 0) OR b.reserved_qty <> COALESCE(m.r, 0) OR b.quarantine_qty <> COALESCE(m.q, 0)`)
	if n != 0 {
		e.t.Fatalf("%d saldo tidak cocok dengan ledger", n)
	}
}

// ------------------------------------------------------------------ HTTP

type resp struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (r resp) problem(t *testing.T) apperror.Problem {
	t.Helper()
	var p apperror.Problem
	if err := json.Unmarshal(r.Body, &p); err != nil {
		t.Fatalf("body bukan problem+json (%d): %s", r.Code, r.Body)
	}
	return p
}

func (r resp) decode(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, dst); err != nil {
		t.Fatalf("decode %d: %v: %s", r.Code, err, r.Body)
	}
}

func serve(h http.Handler, method, path string, body any, headers map[string]string) resp {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			panic(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return resp{Code: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes()}
}

// device memanggil route POS dengan token bearer (resolver sungguhan).
func (e *env) device(method, path, token string, body any) resp {
	return serve(e.engine, method, path, body, map[string]string{"Authorization": "Bearer " + token})
}

// staffEngine memasang route POS di belakang principal staff buatan.
func (e *env) staffEngine(p *auth.Principal) http.Handler {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(apperror.RequestIDKey, "req-test-pos-0001")
		if p != nil {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
		}
		c.Next()
	})
	router.MountPOS(r.Group("/api/v1"), router.Deps{Config: e.cfg, DB: e.db, Log: quietLog, Clock: e.clk})
	return r
}

// staff membuat principal staff dengan scope per permission (CanAt).
func staff(id uuid.UUID, all bool, locs []uuid.UUID, perms ...string) *auth.Principal {
	p := &auth.Principal{Kind: auth.KindStaff, ID: id, ActorID: id, ScopeAll: all, LocationIDs: locs,
		Permissions: map[string]struct{}{}, PermissionScopes: map[string]auth.PermScope{}}
	for _, perm := range perms {
		p.Permissions[perm] = struct{}{}
		p.PermissionScopes[perm] = auth.PermScope{All: all, LocationIDs: locs}
	}
	return p
}

// devicePrincipal: principal device demo (untuk memanggil service langsung).
func devicePrincipal() *auth.Principal {
	return &auth.Principal{Kind: auth.KindDevice, ID: deviceID, LocationIDs: []uuid.UUID{jakselID},
		Permissions:      map[string]struct{}{service.PermPosSync: {}},
		PermissionScopes: map[string]auth.PermScope{service.PermPosSync: {LocationIDs: []uuid.UUID{jakselID}}}}
}

// ------------------------------------------------------------------ transaksi POS

type txn map[string]any

func line(product uuid.UUID, qty, price int64, serials ...string) map[string]any {
	l := map[string]any{"product_id": product, "qty": qty, "unit_price": price, "discount": int64(0)}
	if len(serials) > 0 {
		l["serials"] = serials
	}
	return l
}

func cash(amount int64) map[string]any { return map[string]any{"method": "CASH", "amount": amount} }

// newTxn membuat transaksi tunai pas, terjadi "sekarang" di jam device.
func (e *env) newTxn(seq int64, lines ...map[string]any) txn {
	var total int64
	for _, l := range lines {
		total += l["qty"].(int64)*l["unit_price"].(int64) - l["discount"].(int64)
	}
	return txn{
		"client_txn_id": uuid.Must(uuid.NewV7()).String(), "device_seq": seq, "cashier_id": kasirID.String(),
		"occurred_at": e.clk.Now().Format(time.RFC3339Nano), "price_list_version": 2,
		"lines": lines, "payments": []any{cash(total)}, "total": total,
	}
}

// signed menandatangani txn dengan key (kanonisasi persis seperti klien).
func signed(t *testing.T, key []byte, x txn) json.RawMessage {
	t.Helper()
	cp := txn{}
	for k, v := range x {
		if k != "signature" {
			cp[k] = v
		}
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	canon, err := domain.CanonicalPosJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	cp["signature"] = domain.NewPosSigner(key).Sign(canon)
	out, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type syncResult struct {
	Index       int             `json:"index"`
	ClientTxnID uuid.UUID       `json:"client_txn_id"`
	DeviceSeq   int64           `json:"device_seq"`
	Status      string          `json:"status"`
	Result      string          `json:"result"`
	Detail      json.RawMessage `json:"detail"`
}

type syncResponse struct {
	ServerTime           time.Time    `json:"server_time"`
	AckUpToSeq           int64        `json:"ack_up_to_seq"`
	MissingSeqs          []int64      `json:"missing_seqs"`
	MissingSeqsTruncated bool         `json:"missing_seqs_truncated"`
	Results              []syncResult `json:"results"`
}

type detail struct {
	ServerTxnID uuid.UUID   `json:"server_txn_id"`
	OrderID     *uuid.UUID  `json:"order_id"`
	Reason      string      `json:"reason"`
	Flags       []string    `json:"flags"`
	SerialsSold []string    `json:"serials_sold"`
	Invalid     []string    `json:"invalid_serials"`
	ClockSkewMs *int64      `json:"clock_skew_ms"`
	OccurredAt  *time.Time  `json:"occurred_at"`
	Conflicts   []conflict  `json:"conflicts"`
	Errors      []fieldErr  `json:"errors"`
	Totals      *totalsView `json:"totals"`
}

type conflict struct {
	Type          string     `json:"type"`
	ConflictID    *uuid.UUID `json:"conflict_id"`
	OrderID       *uuid.UUID `json:"order_id"`
	ReservationID *uuid.UUID `json:"reservation_id"`
	Qty           *int64     `json:"qty"`
	Code          string     `json:"code"`
	Reason        string     `json:"reason"`
	AnomalyRule   string     `json:"anomaly_rule"`
}

type fieldErr struct {
	Field, Code string
}

type totalsView struct {
	Total     int64 `json:"total"`
	Paid      int64 `json:"paid"`
	ChangeDue int64 `json:"change_due"`
}

func (r syncResult) detail(t *testing.T) detail {
	t.Helper()
	var d detail
	if err := json.Unmarshal(r.Detail, &d); err != nil {
		t.Fatalf("detail: %v: %s", err, r.Detail)
	}
	return d
}

// sync mengirim batch transaksi bertanda tangan lewat HTTP dan mewajibkan 200.
func (e *env) sync(raws ...json.RawMessage) syncResponse {
	e.t.Helper()
	if raws == nil {
		raws = []json.RawMessage{}
	}
	return e.syncWith(e.token, map[string]any{"client_time": e.clk.Now(), "transactions": raws})
}

func (e *env) syncWith(token string, body any) syncResponse {
	e.t.Helper()
	r := e.device(http.MethodPost, "/api/v1/pos/sync", token, body)
	if r.Code != http.StatusOK {
		e.t.Fatalf("sync %d: %s", r.Code, r.Body)
	}
	var out syncResponse
	r.decode(e.t, &out)
	return out
}

// heartbeat: outbox kosong (pending_count 0), toko boleh ONLINE.
func (e *env) heartbeat() {
	e.t.Helper()
	e.heartbeatPending(0)
}

type heartbeatResp struct {
	LocationSyncState   string  `json:"location_sync_state"`
	OnlineBlockedReason *string `json:"online_blocked_reason"`
	ClockSkewMs         *int64  `json:"clock_skew_ms"`
}

func (e *env) heartbeatPending(pending int64) heartbeatResp {
	e.t.Helper()
	r := e.device(http.MethodPost, "/api/v1/pos/heartbeat", e.token, map[string]any{"pending_count": pending})
	if r.Code != http.StatusOK {
		e.t.Fatalf("heartbeat %d: %s", r.Code, r.Body)
	}
	var out heartbeatResp
	r.decode(e.t, &out)
	return out
}
