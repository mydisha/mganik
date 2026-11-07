// Package commandcenter_test berisi test integrasi HTTP untuk /api/v1/cc/*
// (Fase 5) terhadap PostgreSQL sungguhan. Paket test tersendiri supaya
// TestMain-nya tidak bertabrakan dengan paket lain yang juga memakai testutil.
//
// Login belum ada di paket ini: Principal dipasang middleware test lewat
// auth.WithPrincipal, lalu route dipasang dengan router.MountCommandCenter.
package commandcenter_test

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
	"mganik/api/internal/controller"
	"mganik/api/internal/domain"
	"mganik/api/internal/router"
	"mganik/api/internal/seed"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) {
	// Zona proses bukan UTC (seperti server di WIB) supaya test membuktikan
	// semua timestamp respons tetap dikirim dalam UTC.
	time.Local = time.FixedZone("WIB", 7*60*60)
	gin.SetMode(gin.TestMode)
	controller.SetupValidator()
	testutil.Main(m)
}

// testNow: "sekarang" untuk semua test (16.41 WIB, tanggal lokal = tanggal UTC).
var testNow = time.Date(2026, 11, 7, 9, 41, 0, 0, time.UTC)

var (
	opsUserID     = seed.UserID("ops@mganik.test")
	jakselMgrID   = seed.UserID("kepala.jaksel@mganik.test")
	jakselID      = seed.LocationID(seed.StoreJakselCode)
	bandungID     = seed.LocationID(seed.StoreBandungCode)
	centralID     = seed.LocationID(seed.CentralWHCode)
	partnerSulsel = seed.LocationID(seed.PartnerSulselCode)
	sku1ID        = seed.ProductID(seed.SKU1)
	sku2ID        = seed.ProductID(seed.SKU2)
	brandID       = seed.ID("brand:" + seed.BrandCode)
	jatimPartyID  = seed.ID(seed.JatimPartyKey)
	sulselPartyID = seed.ID(seed.SulselPartyKey)
)

// env adalah satu database ber-seed demo plus jam palsu.
type env struct {
	t   *testing.T
	db  *gorm.DB
	clk *clock.Fake
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testutil.NewDB(t)
	clk := clock.NewFake(testNow)
	_, err := seed.Run(context.Background(), db, seed.Options{
		Enabled: true, Pepper: []byte("pepper-test-32-byte-xxxxxxxxxxxx"), DemoPassword: "kata-sandi-demo-test",
		PasswordParams: domain.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, db: db, clk: clk}
}

// ------------------------------------------------------------------ principal

func perms(codes ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}

// opsPrincipal: manajer operasional, scope ALL, baca + tindak lanjut anomali.
func opsPrincipal() *auth.Principal {
	return &auth.Principal{Kind: auth.KindStaff, ID: opsUserID, ActorID: opsUserID,
		Permissions: perms("cc.read", "anomaly.resolve"), ScopeAll: true}
}

// jakselPrincipal: kepala toko Jakarta Selatan, scope SELECTED.
func jakselPrincipal(extra ...string) *auth.Principal {
	return &auth.Principal{Kind: auth.KindStaff, ID: jakselMgrID, ActorID: jakselMgrID,
		Permissions: perms(append([]string{"cc.read"}, extra...)...), LocationIDs: []uuid.UUID{jakselID}}
}

// ------------------------------------------------------------------ HTTP

func (e *env) engine(p *auth.Principal) http.Handler {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(apperror.RequestIDKey, "req-test-cc-0001")
		if p != nil {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
		}
		c.Next()
	})
	router.MountCommandCenter(r.Group("/api/v1"), router.Deps{
		DB: e.db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: e.clk,
	})
	return r
}

type resp struct {
	Code int
	Body []byte
}

func (r resp) problem(t *testing.T) apperror.Problem {
	t.Helper()
	var p apperror.Problem
	if err := json.Unmarshal(r.Body, &p); err != nil {
		t.Fatalf("body bukan problem+json: %s", r.Body)
	}
	return p
}

func call(t *testing.T, h http.Handler, method, path string, body any) resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return resp{Code: rec.Code, Body: rec.Body.Bytes()}
}

// getJSON memanggil GET dan men-decode body ke out; status wajib 200.
func getJSON(t *testing.T, h http.Handler, path string, out any) {
	t.Helper()
	r := call(t, h, http.MethodGet, path, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, r.Code, r.Body)
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		t.Fatalf("decode %s: %v\n%s", path, err, r.Body)
	}
}

func requireProblem(t *testing.T, r resp, status int, code string) apperror.Problem {
	t.Helper()
	if r.Code != status {
		t.Fatalf("status %d, ingin %d: %s", r.Code, status, r.Body)
	}
	p := r.problem(t)
	if p.Code != code {
		t.Fatalf("kode %q, ingin %q: %s", p.Code, code, r.Body)
	}
	return p
}

// ------------------------------------------------------------------ fixture SQL

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.db.Exec(query, args...).Error; err != nil {
		e.t.Fatalf("%v\nquery: %s", err, query)
	}
}

func (e *env) count(query string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.db.Raw(query, args...).Scan(&n).Error; err != nil {
		e.t.Fatalf("%v\nquery: %s", err, query)
	}
	return n
}

func newID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

func (e *env) product(sku string) uuid.UUID {
	e.t.Helper()
	id := newID()
	e.exec(`INSERT INTO products (id, brand_id, sku, name) VALUES (?, ?, ?, ?)`, id, brandID, sku, "Produk uji "+sku)
	return id
}

func (e *env) location(code, typ string, party *uuid.UUID, region *string) uuid.UUID {
	e.t.Helper()
	id := newID()
	e.exec(`INSERT INTO locations (id, code, name, type_code, party_id, region_code, timezone)
	        VALUES (?, ?, ?, ?, ?, ?, 'Asia/Jakarta')`, id, code, "Lokasi uji "+code, typ, party, region)
	return id
}

// balance membuat saldo dengan available = avail (physical = avail + reserved).
func (e *env) balance(loc, prod uuid.UUID, avail, reserved int64) {
	e.t.Helper()
	e.exec(`INSERT INTO inventory_balances (location_id, product_id, physical_qty, reserved_qty) VALUES (?, ?, ?, ?)`,
		loc, prod, avail+reserved, reserved)
}

func (e *env) policy(loc, prod uuid.UUID, minQty int64, lead, safety int) {
	e.t.Helper()
	e.exec(`INSERT INTO stock_policies (location_id, product_id, min_qty, lead_time_days, safety_days) VALUES (?, ?, ?, ?, ?)`,
		loc, prod, minQty, lead, safety)
}

// dailySales mencatat penjualan qty unit per hari selama days hari terakhir
// (hari ke-1 = kemarin), satu INSERT set-based.
func (e *env) dailySales(loc, prod uuid.UUID, movementType string, qty int64, days int) {
	e.t.Helper()
	reserved := int64(0)
	if movementType == "COMMIT_SALE" {
		reserved = -qty
	}
	e.exec(`INSERT INTO inventory_movements (id, location_id, product_id, movement_type, physical_delta, reserved_delta,
	                                          ref_type, ref_id, idempotency_key, actor_type, occurred_at)
	        SELECT gen_random_uuid(), ?, ?, ?, ?, ?, 'TEST', gen_random_uuid(), 'test:' || gen_random_uuid(), 'SYSTEM',
	               CAST(? AS timestamptz) - make_interval(days => g) + interval '1 hour'
	          FROM generate_series(1, ?) g`, loc, prod, movementType, -qty, reserved, testNow, days)
}

// saleAt mencatat satu penjualan pada waktu at.
func (e *env) saleAt(loc, prod uuid.UUID, qty int64, at time.Time) {
	e.t.Helper()
	e.exec(`INSERT INTO inventory_movements (id, location_id, product_id, movement_type, physical_delta,
	                                          ref_type, ref_id, idempotency_key, actor_type, occurred_at)
	        VALUES (gen_random_uuid(), ?, ?, 'DIRECT_SALE', ?, 'TEST', gen_random_uuid(), 'test:' || gen_random_uuid(), 'SYSTEM', ?)`,
		loc, prod, -qty, at)
}

type anomalyFx struct {
	rule, severity, status, subjectType string
	subjectID, partyID                  *uuid.UUID
	createdAt                           time.Time
}

func (e *env) anomaly(a anomalyFx) uuid.UUID {
	e.t.Helper()
	id := newID()
	if a.rule == "" {
		a.rule = "OUT_OF_TERRITORY"
	}
	if a.severity == "" {
		a.severity = "MEDIUM"
	}
	if a.status == "" {
		a.status = "OPEN"
	}
	if a.subjectType == "" {
		a.subjectType = "PARTY"
	}
	if a.createdAt.IsZero() {
		a.createdAt = testNow.Add(-time.Hour)
	}
	e.exec(`INSERT INTO anomalies (id, rule_code, severity, status, subject_type, subject_id, party_id, dedupe_key,
	                               evidence, first_seen_at, last_seen_at, created_at, updated_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{"sumber":"test"}', ?, ?, ?, ?)`,
		id, a.rule, a.severity, a.status, a.subjectType, a.subjectID, a.partyID, "test:"+id.String(),
		a.createdAt, a.createdAt, a.createdAt, a.createdAt)
	return id
}

func (e *env) conflict(loc, prod uuid.UUID, status string, createdAt time.Time) uuid.UUID {
	e.t.Helper()
	id := newID()
	var resolvedAt *time.Time
	if status != "OPEN" {
		resolvedAt = &createdAt
	}
	e.exec(`INSERT INTO allocation_conflicts (id, kind, location_id, product_id, qty, status, resolved_at, created_at, updated_at)
	        VALUES (?, 'OFFLINE_SALE_PREEMPT', ?, ?, 1, ?, ?, ?, ?)`, id, loc, prod, status, resolvedAt, createdAt, createdAt)
	return id
}

func ptr[T any](v T) *T { return &v }

func q(path string, kv ...string) string {
	if len(kv) == 0 {
		return path
	}
	s := path + "?"
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			s += "&"
		}
		s += kv[i] + "=" + kv[i+1]
	}
	return s
}
