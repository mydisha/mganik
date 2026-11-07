// Package inventorytest berisi fixture dan helper untuk test integrasi domain
// stok & order (Fase 3) terhadap Postgres sungguhan. Test-nya ada di package
// ini dan di internal/worker.
package inventorytest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/controller"
	"mganik/api/internal/dto"
	"mganik/api/internal/middleware"
	"mganik/api/internal/model"
	"mganik/api/internal/router"
	"mganik/api/internal/service"
)

// WebhookSecret adalah secret webhook khusus test.
const WebhookSecret = "rahasia-webhook-test-minimal-32-byte!!"

// Start adalah jam awal fixture.
var Start = time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)

// Fixture: katalog kecil, empat lokasi, price list D2C, dan service yang
// berbagi clock.Fake.
type Fixture struct {
	T     testing.TB
	DB    *gorm.DB
	Clock *clock.Fake
	Log   *slog.Logger
	TTL   time.Duration

	Inv      *service.InventoryService
	Idem     *service.IdempotencyService
	Orders   *service.OrderService
	Payments *service.PaymentService
	Market   *service.MarketplaceService

	// Central (CENTRAL_WH), Hub (HUB), StoreA (OWN_STORE, ONLINE), StoreB
	// (OWN_STORE, OFFLINE). Semua online_fulfillment_enabled.
	Central, Hub, StoreA, StoreB uuid.UUID
	ProductA, ProductB, ProductC uuid.UUID
	PriceA, PriceB, PriceC       int64

	engineOnce sync.Once
	engine     *gin.Engine
	principals sync.Map
}

const (
	SKUA = "TST-A"
	SKUB = "TST-B"
	SKUC = "TST-C"
)

// New menyiapkan fixture di database test db (sudah termigrasi).
func New(t testing.TB, db *gorm.DB) *Fixture {
	t.Helper()
	clk := clock.NewFake(Start)
	f := &Fixture{
		T: t, DB: db, Clock: clk, TTL: 15 * time.Minute,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		PriceA: 46000, PriceB: 33000, PriceC: 125000,
	}
	f.Inv = service.NewInventoryService(db, clk)
	f.Idem = service.NewIdempotencyService(db, clk)
	f.Orders = service.NewOrderService(db, clk, f.Inv, f.Idem, f.TTL)
	f.Payments = service.NewPaymentService(f.Orders)
	f.Market = service.NewMarketplaceService(f.Orders)

	brand := uuid.New()
	f.must(db.Exec(`INSERT INTO brands (id, code, name) VALUES (?, 'TST', 'Brand Uji')`, brand).Error)
	f.ProductA, f.ProductB, f.ProductC = uuid.New(), uuid.New(), uuid.New()
	for sku, id := range map[string]uuid.UUID{SKUA: f.ProductA, SKUB: f.ProductB, SKUC: f.ProductC} {
		f.must(db.Exec(`INSERT INTO products (id, brand_id, sku, name) VALUES (?, ?, ?, ?)`, id, brand, sku, "Produk "+sku).Error)
	}
	f.Central, f.Hub, f.StoreA, f.StoreB = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, l := range []struct {
		id        uuid.UUID
		code, typ string
		sync      string
	}{
		{f.Central, "T-GDP", "CENTRAL_WH", "NOT_TRACKED"},
		{f.Hub, "T-HUB", "HUB", "NOT_TRACKED"},
		{f.StoreA, "T-TKO-A", "OWN_STORE", "ONLINE"},
		{f.StoreB, "T-TKO-B", "OWN_STORE", "OFFLINE"},
	} {
		f.must(db.Exec(`INSERT INTO locations (id, code, name, type_code, online_fulfillment_enabled, sync_state)
		                VALUES (?, ?, ?, ?, true, ?)`, l.id, l.code, "Lokasi "+l.code, l.typ, l.sync).Error)
	}
	pl := uuid.New()
	f.must(db.Exec(`INSERT INTO price_lists (id, code, name, channel_code, version, valid_from)
	                VALUES (?, 'T-D2C', 'Harga uji', 'D2C_WEB', 1, ?)`, pl, Start.Add(-30*24*time.Hour)).Error)
	for id, price := range map[uuid.UUID]int64{f.ProductA: f.PriceA, f.ProductB: f.PriceB, f.ProductC: f.PriceC} {
		f.must(db.Exec(`INSERT INTO price_list_items (price_list_id, product_id, price) VALUES (?, ?, ?)`, pl, id, price).Error)
	}
	return f
}

func (f *Fixture) must(err error) {
	f.T.Helper()
	if err != nil {
		f.T.Fatal(err)
	}
}

// Receive menambah stok fisik lewat engine (RECEIPT), membuat saldo bila belum ada.
func (f *Fixture) Receive(loc, prod uuid.UUID, qty int64) {
	f.T.Helper()
	f.must(f.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO inventory_balances (location_id, product_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, loc, prod).Error; err != nil {
			return err
		}
		_, err := f.Inv.ApplyMovement(context.Background(), tx, service.MovementInput{
			LocationID: loc, ProductID: prod, Type: service.MovementReceipt, Qty: qty,
			RefType: "TEST", RefID: uuid.New(), IdempotencyKey: "test:receipt:" + uuid.NewString(),
			Actor: service.SystemLedgerActor,
		})
		return err
	}))
}

// SetBuffer mengatur stock_policies.online_buffer_qty.
func (f *Fixture) SetBuffer(loc, prod uuid.UUID, buffer int64) {
	f.T.Helper()
	f.must(f.DB.Exec(`INSERT INTO stock_policies (location_id, product_id, online_buffer_qty) VALUES (?, ?, ?)
	                  ON CONFLICT (location_id, product_id) DO UPDATE SET online_buffer_qty = EXCLUDED.online_buffer_qty`,
		loc, prod, buffer).Error)
}

// SetSyncState mengubah status sinkron lokasi.
func (f *Fixture) SetSyncState(loc uuid.UUID, state string) {
	f.T.Helper()
	f.must(f.DB.Exec(`UPDATE locations SET sync_state = ? WHERE id = ?`, state, loc).Error)
}

// Balance membaca saldo (nol kalau belum ada).
func (f *Fixture) Balance(loc, prod uuid.UUID) model.InventoryBalance {
	f.T.Helper()
	var b model.InventoryBalance
	f.must(f.DB.Raw(`SELECT * FROM inventory_balances WHERE location_id = ? AND product_id = ?`, loc, prod).Scan(&b).Error)
	return b
}

// Count menjalankan SELECT count(*) ... dan mengembalikan hasilnya.
func (f *Fixture) Count(query string, args ...any) int64 {
	f.T.Helper()
	var n int64
	f.must(f.DB.Raw(query, args...).Scan(&n).Error)
	return n
}

// Order membaca status order.
func (f *Fixture) OrderStatus(id uuid.UUID) string {
	f.T.Helper()
	var s string
	f.must(f.DB.Raw(`SELECT status FROM orders WHERE id = ?`, id).Scan(&s).Error)
	return s
}

// AssertInvariants memeriksa invariant stok di seluruh database:
//  1. SUM(delta) ledger = saldo per (lokasi, produk) untuk tiga bucket;
//  2. SUM(qty) reservasi ACTIVE = reserved_qty per (lokasi, produk).
func (f *Fixture) AssertInvariants() {
	f.T.Helper()
	f.AssertLedgerInvariant()
	f.AssertReservationInvariant()
}

// AssertLedgerInvariant: SUM(delta) ledger = saldo per (lokasi, produk).
func (f *Fixture) AssertLedgerInvariant() {
	f.T.Helper()
	ledger := f.Count(`
		SELECT count(*) FROM (
		  SELECT b.location_id, b.product_id
		    FROM inventory_balances b
		    LEFT JOIN inventory_movements m ON m.location_id = b.location_id AND m.product_id = b.product_id
		   GROUP BY b.location_id, b.product_id, b.physical_qty, b.reserved_qty, b.quarantine_qty
		  HAVING b.physical_qty   <> COALESCE(sum(m.physical_delta), 0)
		      OR b.reserved_qty   <> COALESCE(sum(m.reserved_delta), 0)
		      OR b.quarantine_qty <> COALESCE(sum(m.quarantine_delta), 0)) d`)
	if ledger != 0 {
		f.T.Fatalf("invariant ledger: %d saldo tidak sama dengan SUM(delta) ledger", ledger)
	}
}

// AssertReservationInvariant: SUM(qty) reservasi ACTIVE = reserved_qty.
func (f *Fixture) AssertReservationInvariant() {
	f.T.Helper()
	res := f.Count(`
		SELECT count(*) FROM (
		  SELECT b.location_id
		    FROM inventory_balances b
		    LEFT JOIN stock_reservations r ON r.location_id = b.location_id AND r.product_id = b.product_id AND r.status = 'ACTIVE'
		   GROUP BY b.location_id, b.product_id, b.reserved_qty
		  HAVING b.reserved_qty <> COALESCE(sum(r.qty), 0)) d`)
	if res != 0 {
		f.T.Fatalf("invariant reservasi: %d saldo reserved_qty tidak sama dengan SUM reservasi ACTIVE", res)
	}
}

// ------------------------------------------------------------------ order

// Line adalah singkatan baris order D2C.
func Line(prod uuid.UUID, qty int64) service.CreateOrderLine {
	return service.CreateOrderLine{ProductID: prod, Qty: qty}
}

// CreateOrder membuat order D2C lewat service (Idempotency-Key acak).
func (f *Fixture) CreateOrder(lines ...service.CreateOrderLine) (dto.OrderResponse, error) {
	f.T.Helper()
	resp, err := f.Orders.CreateIdempotent(context.Background(), Staff("order.create"), uuid.NewString(),
		service.CreateOrderInput{Lines: lines}, func(v service.OrderView) any { return controller.OrderResponse(v) })
	if err != nil {
		return dto.OrderResponse{}, err
	}
	var o dto.OrderResponse
	if err := json.Unmarshal(resp.Body, &o); err != nil {
		f.T.Fatal(err)
	}
	return o, nil
}

// MustOrder seperti CreateOrder tetapi menggagalkan test kalau error.
func (f *Fixture) MustOrder(lines ...service.CreateOrderLine) dto.OrderResponse {
	f.T.Helper()
	o, err := f.CreateOrder(lines...)
	if err != nil {
		f.T.Fatalf("membuat order: %v", err)
	}
	return o
}

// PayBody adalah body webhook pembayaran mock.
func PayBody(ref string, orderID uuid.UUID, amount int64) map[string]any {
	return map[string]any{"provider_ref": ref, "order_id": orderID, "amount": amount, "currency": "IDR", "status": "SUCCEEDED"}
}

// Pay membayar order lewat webhook bertanda tangan dan mengharapkan 200.
func (f *Fixture) Pay(o dto.OrderResponse) dto.PaymentWebhookResponse {
	f.T.Helper()
	rec := f.Signed("/api/v1/webhooks/payments/mock", PayBody("PAY-"+uuid.NewString(), o.ID, o.Total))
	if rec.Code != http.StatusOK {
		f.T.Fatalf("webhook bayar %d: %s", rec.Code, rec.Body)
	}
	var r dto.PaymentWebhookResponse
	Decode(f.T, rec, &r)
	return r
}

// ------------------------------------------------------------------ principal & HTTP

// Staff membuat principal staff ber-scope ALL dengan permission perms.
func Staff(perms ...string) *auth.Principal {
	p := &auth.Principal{Kind: auth.KindStaff, ID: uuid.New(), ScopeAll: true, Permissions: map[string]struct{}{}}
	for _, perm := range perms {
		p.Permissions[perm] = struct{}{}
	}
	return p
}

// StaffAt membuat principal staff ber-scope SELECTED pada locs.
func StaffAt(locs []uuid.UUID, perms ...string) *auth.Principal {
	p := Staff(perms...)
	p.ScopeAll = false
	p.LocationIDs = locs
	return p
}

// AllInventoryPerms adalah semua permission domain stok & order.
var AllInventoryPerms = []string{"inventory.read", "inventory.adjust", "inventory.quarantine", "inventory.transfer",
	"order.read", "order.create", "order.cancel", "order.fulfill"}

const principalHeader = "X-Test-Principal"

// Engine memasang route Fase 3 di engine Gin dengan middleware test yang
// memasang Principal dari header X-Test-Principal (pengganti login IAM).
func (f *Fixture) Engine() *gin.Engine {
	f.engineOnce.Do(func() {
		gin.SetMode(gin.TestMode)
		controller.SetupValidator()
		r := gin.New()
		r.Use(middleware.RequestID(), middleware.BodyLimit(1<<20))
		api := r.Group("/api/v1", func(c *gin.Context) {
			if v, ok := f.principals.Load(c.GetHeader(principalHeader)); ok {
				c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), v.(*auth.Principal)))
			}
			c.Next()
		})
		router.MountInventory(api, router.Deps{
			Config: config.Config{AppEnv: config.EnvTest, MaxBodyBytes: 1 << 20, Inventory: config.InventoryConfig{
				ReservationTTL: f.TTL, WebhookSecret: []byte(WebhookSecret),
			}},
			DB: f.DB, Log: f.Log, Clock: f.Clock,
		})
		f.engine = r
	})
	return f.engine
}

// Do mengirim request ke Engine sebagai principal p (nil = anonim). body
// boleh []byte, string, atau nilai yang di-encode JSON.
func (f *Fixture) Do(p *auth.Principal, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	f.T.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(encodeBody(f.T, body)))
	req.Header.Set("Content-Type", "application/json")
	if p != nil {
		token := uuid.NewString()
		f.principals.Store(token, p)
		defer f.principals.Delete(token)
		req.Header.Set(principalHeader, token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.Engine().ServeHTTP(rec, req)
	return rec
}

// Signed mengirim request webhook bertanda tangan HMAC dengan timestamp clock fixture.
func (f *Fixture) Signed(path string, body any) *httptest.ResponseRecorder {
	f.T.Helper()
	b := encodeBody(f.T, body)
	ts := strconv.FormatInt(f.Clock.Now().Unix(), 10)
	return f.Do(nil, http.MethodPost, path, b, map[string]string{
		middleware.WebhookTimestampHeader: ts,
		middleware.WebhookSignatureHeader: middleware.NewWebhookSigner([]byte(WebhookSecret)).Sign(ts, b),
	})
}

func encodeBody(t testing.TB, body any) []byte {
	t.Helper()
	switch v := body.(type) {
	case nil:
		return nil
	case []byte:
		return v
	case string:
		return []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
}

// Decode meng-decode body respons JSON ke v.
func Decode(t testing.TB, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode respons %d: %v\n%s", rec.Code, err, rec.Body)
	}
}
