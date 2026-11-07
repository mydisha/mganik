// Package serialtest berisi test integrasi Fase 2 (serialisasi, verifikasi,
// radar, klaim) terhadap Postgres sungguhan. Dipisah ke package sendiri
// supaya punya TestMain sendiri tanpa bertabrakan dengan test package lain
// yang dikerjakan paralel.
package serialtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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
	"mganik/api/internal/middleware"
	"mganik/api/internal/model"
	"mganik/api/internal/router"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type gormDB = gorm.DB

// cryptoRand: io.Reader dari crypto/rand untuk membuat kode uji.
type cryptoRand struct{}

func (cryptoRand) Read(p []byte) (int, error) { return rand.Read(p) }

func ptr[T any](v T) *T { return &v }

var (
	testPepper = []byte("pepper-test-serialisasi-32-byte!")
	fastArgon  = domain.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
	// t0: Sabtu 7 Nov 2026 03:00 UTC = 10:00 WIB (jam buka toko).
	t0 = time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)
)

// Koordinat uji (centroid kota di seed).
var (
	geoMakassar  = domain.GeoPoint{Lat: -5.1477, Lng: 119.4327}
	geoGowa      = domain.GeoPoint{Lat: -5.3103, Lng: 119.7426}
	geoJakBar    = domain.GeoPoint{Lat: -6.1674, Lng: 106.7637}
	geoSurabaya  = domain.GeoPoint{Lat: -7.2575, Lng: 112.7521}
	geoSemarang  = domain.GeoPoint{Lat: -7.0051, Lng: 110.4381}
	geoMedan     = domain.GeoPoint{Lat: 3.5952, Lng: 98.6722}
	geoMidOcean  = domain.GeoPoint{Lat: -10.5, Lng: 125.0} // > 50 km dari centroid mana pun
	geoStoreJaks = domain.GeoPoint{Lat: -6.2441, Lng: 106.8003}
)

// fx adalah satu database ter-seed beserta service Fase 2.
type fx struct {
	t       *testing.T
	ctx     context.Context
	db      *gorm.DB
	clk     *clock.Fake
	cfg     config.Config
	serials *service.SerialService
	verify  *service.VerifyService
	loyalty *service.LoyaltyService
}

func newFx(t *testing.T) *fx {
	t.Helper()
	db := testutil.NewDB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := seed.Run(context.Background(), db, seed.Options{
		Enabled: true, Pepper: testPepper, DemoPassword: "kata-sandi-demo-test", PasswordParams: fastArgon, Log: log,
	}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(t0)
	cfg := config.Config{AppEnv: config.EnvTest, MaxBodyBytes: 1 << 20, ClaimPepper: testPepper}
	serials := service.NewSerialService(db, clk, testPepper)
	return &fx{
		t: t, ctx: context.Background(), db: db, clk: clk, cfg: cfg, serials: serials,
		verify:  service.NewVerifyService(db, clk, testPepper, nil),
		loyalty: service.NewLoyaltyService(db, clk, testPepper, cfg.Serialization, serials),
	}
}

// ---------------------------------------------------------------- principal

func perms(codes ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}

// staffAll: staff ber-scope ALL dengan permission tertentu.
func staffAll(codes ...string) *auth.Principal {
	return &auth.Principal{Kind: auth.KindStaff, ID: seed.UserID("ops@mganik.test"), Permissions: perms(codes...), ScopeAll: true}
}

// staffAt: staff ber-scope SELECTED untuk lokasi tertentu.
func staffAt(locs []uuid.UUID, codes ...string) *auth.Principal {
	return &auth.Principal{Kind: auth.KindStaff, ID: seed.UserID("gudang@mganik.test"), Permissions: perms(codes...), LocationIDs: locs}
}

var allSerialPerms = []string{
	"code.mint", "serial.commission", "serial.receive", "serial.aggregate", "serial.trace", "serial.quarantine",
	"serial.sell", "batch.recall", "shipment.create", "shipment.receive", "loyalty.review",
}

func (f *fx) admin() *auth.Principal { return staffAll(allSerialPerms...) }

// newConsumer membuat konsumen aktif dan principal-nya.
func (f *fx) newConsumer() *auth.Principal {
	f.t.Helper()
	id := uuid.Must(uuid.NewV7())
	c := model.Consumer{ID: id, PhoneEnc: []byte("enc-" + id.String()), PhoneBidx: []byte("bidx-" + id.String()), Status: "ACTIVE"}
	if err := f.db.Create(&c).Error; err != nil {
		f.t.Fatal(err)
	}
	return &auth.Principal{Kind: auth.KindConsumer, ID: id}
}

// ---------------------------------------------------------------- data demo

var (
	locCentral = seed.LocationID(seed.CentralWHCode)
	locHub     = seed.LocationID(seed.HubSurabayaCode)
	locJaksel  = seed.LocationID(seed.StoreJakselCode)
	locSulsel  = seed.LocationID(seed.PartnerSulselCode)
	locJatim   = seed.LocationID(seed.PartnerJatimCode)
	partySul   = seed.ID(seed.SulselPartyKey)
	partyCo    = seed.ID(seed.CompanyPartyKey)
)

func palletCode() string      { return seed.DemoSerialCode("PALLET", 0) }
func cartonCode(i int) string { return seed.DemoSerialCode("CARTON", i) }
func unit(i int) seed.DemoUnit {
	return seed.DemoUnits()[i]
}

// shipPalletTo mengirim palet demo (48 unit) dari gudang pusat ke lokasi dan
// menerimanya.
func (f *fx) shipPalletTo(to uuid.UUID, program *string) service.ShipmentResult {
	f.t.Helper()
	res, err := f.serials.CreateShipment(f.ctx, f.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: to, ProgramCode: program, Codes: []string{palletCode()},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.serials.ReceiveShipment(f.ctx, f.admin(), res.Shipment.ID); err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *fx) status(code string) string {
	f.t.Helper()
	var s string
	if err := f.db.Raw(`SELECT status FROM serial_units WHERE code = ?`, code).Scan(&s).Error; err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fx) count(query string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.Raw(query, args...).Scan(&n).Error; err != nil {
		f.t.Fatalf("%v\nquery: %s", err, query)
	}
	return n
}

func (f *fx) exec(query string, args ...any) {
	f.t.Helper()
	if err := f.db.Exec(query, args...).Error; err != nil {
		f.t.Fatalf("%v\nquery: %s", err, query)
	}
}

// mintUnits membuat n unit GENERATED untuk SKU1 dan mengembalikan kode+PIN.
func (f *fx) mint(level string, n int) []service.MintedCode {
	f.t.Helper()
	p := seed.ProductID(seed.SKU1)
	res, err := f.serials.Mint(f.ctx, f.admin(), service.MintInput{LevelCode: level, Qty: n, ProductID: &p, PrinterVendor: "Vendor Uji"})
	if err != nil {
		f.t.Fatal(err)
	}
	return res.Codes
}

func codesOf(ms []service.MintedCode) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Code
	}
	return out
}

// ---------------------------------------------------------------- error

// requireCode menggagalkan test kalau err bukan *apperror.Error dengan code.
func requireCode(t *testing.T, err error, code string) *apperror.Error {
	t.Helper()
	var ae *apperror.Error
	if !errors.As(err, &ae) {
		t.Fatalf("ingin error %s, dapat %v", code, err)
	}
	if ae.Code != code {
		t.Fatalf("ingin error %s, dapat %s (%s) %+v", code, ae.Code, ae.Detail, ae.Fields)
	}
	return ae
}

func errCode(err error) string {
	var ae *apperror.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	if err != nil {
		return "error:" + err.Error()
	}
	return ""
}

// ---------------------------------------------------------------- HTTP

const principalHeader = "X-Test-Principal"

// engine memasang route serialisasi yang sama persis dengan produksi di bawah
// middleware yang memasang principal dari peta (pengganti resolver IAM).
func (f *fx) engine(cfg config.Config, principals map[string]*auth.Principal) http.Handler {
	f.t.Helper()
	gin.SetMode(gin.TestMode)
	controller.SetupValidator()
	r := gin.New()
	if err := r.SetTrustedProxies(nil); err != nil {
		f.t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r.Use(middleware.RequestID(), middleware.BodyLimit(1<<20))
	api := r.Group("/api/v1", func(c *gin.Context) {
		if p, ok := principals[c.GetHeader(principalHeader)]; ok {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
		}
		c.Next()
	})
	cfg.ClaimPepper = testPepper
	router.RegisterSerializationRoutes(api, router.Deps{Config: cfg, DB: f.db, Log: log, Clock: f.clk})
	return r
}

type httpResp struct {
	Code   int
	Body   []byte
	Header http.Header
}

func (r httpResp) problem(t *testing.T) apperror.Problem {
	t.Helper()
	var p apperror.Problem
	if err := json.Unmarshal(r.Body, &p); err != nil {
		t.Fatalf("body bukan problem+json: %s", r.Body)
	}
	return p
}

func do(h http.Handler, method, path, principal string, body any, mutate ...func(*http.Request)) httpResp {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = bytes.NewBufferString(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if principal != "" {
		req.Header.Set(principalHeader, principal)
	}
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return httpResp{Code: rec.Code, Body: rec.Body.Bytes(), Header: rec.Header()}
}
