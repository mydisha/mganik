package inventorytest_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/controller"
	"mganik/api/internal/dto"
	"mganik/api/internal/inventorytest"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

func problemCode(t *testing.T, body []byte) string {
	t.Helper()
	var p apperror.Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("bukan problem+json: %s", body)
	}
	return p.Code
}

func appCode(err error) string {
	var e *apperror.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func createBody(lines ...map[string]any) map[string]any {
	return map[string]any{"lines": lines}
}

func lineBody(prod uuid.UUID, qty int64) map[string]any {
	return map[string]any{"product_id": prod, "qty": qty}
}

// Order A,B dan B,A dibuat paralel 50× masing-masing. Line diurutkan per
// produk sebelum dikunci, jadi tidak ada deadlock (tidak ada transaksi yang
// diulang) dan total reservasi konsisten.
func TestReserve_MultiLineOppositeOrder(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 1000)
	f.Receive(f.Central, f.ProductB, 1000)
	retriesBefore := service.TxRetryCount()

	const rounds = 50
	var wg sync.WaitGroup
	var failed atomic.Int64
	start := make(chan struct{})
	for i := range rounds * 2 {
		wg.Go(func() {
			<-start
			lines := []service.CreateOrderLine{inventorytest.Line(f.ProductA, 1), inventorytest.Line(f.ProductB, 2)}
			if i%2 == 1 {
				lines[0], lines[1] = lines[1], lines[0]
			}
			if _, err := f.CreateOrder(lines...); err != nil {
				failed.Add(1)
				t.Errorf("order %d: %v", i, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if failed.Load() > 0 {
		t.FailNow()
	}
	if service.TxRetryCount() != retriesBefore {
		t.Fatalf("%d transaksi diulang karena deadlock/serialization", service.TxRetryCount()-retriesBefore)
	}
	a, b := f.Balance(f.Central, f.ProductA), f.Balance(f.Central, f.ProductB)
	if a.ReservedQty != rounds*2 || b.ReservedQty != rounds*2*2 {
		t.Fatalf("reserved A %d B %d, ingin %d dan %d", a.ReservedQty, b.ReservedQty, rounds*2, rounds*4)
	}
	if n := f.Count(`SELECT count(*) FROM orders WHERE status = 'PENDING_PAYMENT'`); n != rounds*2 {
		t.Fatalf("order %d, ingin %d", n, rounds*2)
	}
	f.AssertInvariants()
}

// Lonjakan order HTTP untuk 10 unit terakhir: tepat 10 order, sisanya 409 out_of_stock.
func TestOrder_ConcurrentLastUnitsHTTP(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 10)
	p := inventorytest.Staff("order.create")

	var wg sync.WaitGroup
	var created, outOfStock atomic.Int64
	start := make(chan struct{})
	for range 40 {
		wg.Go(func() {
			<-start
			rec := f.Do(p, http.MethodPost, "/api/v1/orders", createBody(lineBody(f.ProductA, 1)),
				map[string]string{controller.IdempotencyKeyHeader: uuid.NewString()})
			switch {
			case rec.Code == http.StatusCreated:
				created.Add(1)
			case rec.Code == http.StatusConflict && problemCode(t, rec.Body.Bytes()) == "out_of_stock":
				outOfStock.Add(1)
			default:
				t.Errorf("status tak terduga %d: %s", rec.Code, rec.Body)
			}
		})
	}
	close(start)
	wg.Wait()
	if created.Load() != 10 || outOfStock.Load() != 30 {
		t.Fatalf("dibuat %d out_of_stock %d, ingin 10 dan 30", created.Load(), outOfStock.Load())
	}
	if b := f.Balance(f.Central, f.ProductA); b.ReservedQty != 10 || b.AvailableQty != 0 {
		t.Fatalf("reserved %d available %d", b.ReservedQty, b.AvailableQty)
	}
	f.AssertInvariants()
}

func TestOrder_CreateHTTP(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)
	f.Receive(f.Central, f.ProductB, 5)
	p := inventorytest.Staff("order.create", "order.read")
	key := func() map[string]string { return map[string]string{controller.IdempotencyKeyHeader: uuid.NewString()} }

	t.Run("sukses: PENDING_PAYMENT, harga dari price list, reservasi ber-TTL", func(t *testing.T) {
		note := "Kirim ke Bapak Budi 🎁 — terima kasih, 東京"
		rec := f.Do(p, http.MethodPost, "/api/v1/orders", map[string]any{
			"lines": []any{lineBody(f.ProductA, 2), lineBody(f.ProductB, 1)}, "note": "  " + note + " ",
		}, key())
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var o dto.OrderResponse
		inventorytest.Decode(t, rec, &o)
		if o.Status != "PENDING_PAYMENT" || o.Total != 2*f.PriceA+f.PriceB || o.ChannelCode != "D2C_WEB" {
			t.Fatalf("order salah: status %s total %d channel %s", o.Status, o.Total, o.ChannelCode)
		}
		if o.FulfillmentLocationID == nil || *o.FulfillmentLocationID != f.Central {
			t.Fatal("fulfillment harus gudang pusat")
		}
		if want := inventorytest.Start.Add(f.TTL); o.PaymentExpiresAt == nil || !o.PaymentExpiresAt.Equal(want) {
			t.Fatalf("payment_expires_at %v, ingin %v", o.PaymentExpiresAt, want)
		}
		if len(o.Reservations) != 2 || o.Reservations[0].Status != "ACTIVE" || o.Reservations[0].ExpiresAt == nil {
			t.Fatalf("reservasi salah: %+v", o.Reservations)
		}
		var meta map[string]string
		_ = json.Unmarshal(o.Metadata, &meta)
		if meta["note"] != note {
			t.Fatalf("catatan unicode tidak utuh: %q", meta["note"])
		}
		if len(o.History) != 1 || o.History[0].ToStatus != "PENDING_PAYMENT" || o.History[0].FromStatus != nil {
			t.Fatalf("riwayat salah: %+v", o.History)
		}
		if n := f.Count(`SELECT count(*) FROM outbox_events WHERE event_type = 'order.created.v1' AND aggregate_id = ?`, o.ID); n != 1 {
			t.Fatalf("outbox order.created.v1 %d", n)
		}
		// GET mengembalikan order yang sama.
		get := f.Do(p, http.MethodGet, "/api/v1/orders/"+o.ID.String(), nil, nil)
		if get.Code != http.StatusOK {
			t.Fatalf("GET %d", get.Code)
		}
	})

	edge := []struct {
		name   string
		body   any
		status int
		code   string
		field  string
	}{
		{"qty 0", createBody(lineBody(f.ProductA, 0)), 422, "validation_failed", "lines[0].qty"},
		{"qty negatif", createBody(lineBody(f.ProductA, -1)), 422, "validation_failed", "lines[0].qty"},
		{"qty di atas batas", createBody(lineBody(f.ProductA, 1001)), 422, "validation_failed", "lines[0].qty"},
		{"qty batas atas 1000 lolos validasi tapi stok habis", createBody(lineBody(f.ProductA, 1000)), 409, "out_of_stock", ""},
		{"line duplikat produk", createBody(lineBody(f.ProductA, 1), lineBody(f.ProductA, 2)), 422, "validation_failed", "lines[1].product_id"},
		{"tanpa line", createBody(), 422, "validation_failed", "lines"},
		{"product_id bukan uuid", createBody(map[string]any{"product_id": "abc", "qty": 1}), 422, "validation_failed", "lines[0].product_id"},
		{"produk tidak ada", createBody(lineBody(uuid.New(), 1)), 422, "validation_failed", "lines[0].product_id"},
		{"field tak dikenal (mass assignment)", map[string]any{"lines": []any{lineBody(f.ProductA, 1)}, "status": "PAID"}, 400, "invalid_request", ""},
		{"channel bukan D2C", map[string]any{"lines": []any{lineBody(f.ProductA, 1)}, "channel_code": "SHOPEE"}, 422, "channel_invalid", ""},
	}
	for _, tt := range edge {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.Do(p, http.MethodPost, "/api/v1/orders", tt.body, key())
			if rec.Code != tt.status {
				t.Fatalf("status %d, ingin %d: %s", rec.Code, tt.status, rec.Body)
			}
			var prob apperror.Problem
			inventorytest.Decode(t, rec, &prob)
			if prob.Code != tt.code {
				t.Fatalf("kode %s, ingin %s", prob.Code, tt.code)
			}
			if tt.field != "" {
				found := false
				for _, fe := range prob.Errors {
					found = found || fe.Field == tt.field
				}
				if !found {
					t.Fatalf("field %s tidak disebut: %+v", tt.field, prob.Errors)
				}
			}
		})
	}
	// Request yang ditolak tidak meninggalkan order atau reservasi.
	if n := f.Count(`SELECT count(*) FROM orders`); n != 1 {
		t.Fatalf("order %d, ingin 1", n)
	}
	f.AssertInvariants()
}

func TestOrder_IdempotencyKey(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 50)
	p := inventorytest.Staff("order.create")
	post := func(key string, qty int64) (int, []byte, string) {
		h := map[string]string{}
		if key != "" {
			h[controller.IdempotencyKeyHeader] = key
		}
		rec := f.Do(p, http.MethodPost, "/api/v1/orders", createBody(lineBody(f.ProductA, qty)), h)
		return rec.Code, rec.Body.Bytes(), rec.Header().Get(controller.IdempotentReplayHeader)
	}

	t.Run("replay mengembalikan respons yang sama persis", func(t *testing.T) {
		key := uuid.NewString()
		c1, b1, r1 := post(key, 1)
		f.Clock.Advance(time.Minute)
		c2, b2, r2 := post(key, 1)
		if c1 != 201 || c2 != 201 || string(b1) != string(b2) {
			t.Fatalf("status %d/%d, body sama=%v", c1, c2, string(b1) == string(b2))
		}
		if r1 != "" || r2 != "true" {
			t.Fatalf("header replay %q/%q", r1, r2)
		}
	})
	t.Run("body berbeda dengan key sama ditolak 422", func(t *testing.T) {
		key := uuid.NewString()
		if c, _, _ := post(key, 1); c != 201 {
			t.Fatal("request pertama gagal")
		}
		c, b, _ := post(key, 2)
		if c != 422 || problemCode(t, b) != "idempotency_key_reused" {
			t.Fatalf("status %d: %s", c, b)
		}
	})
	t.Run("tanpa key atau key tidak valid 400", func(t *testing.T) {
		for _, k := range []string{"", "ada spasi", string(make([]byte, 256))} {
			c, b, _ := post(k, 1)
			if c != 400 || problemCode(t, b) != "idempotency_key_required" {
				t.Fatalf("key %q: status %d %s", k, c, b)
			}
		}
	})
	t.Run("bersamaan dengan key sama: satu order", func(t *testing.T) {
		before := f.Count(`SELECT count(*) FROM orders`)
		key := uuid.NewString()
		var wg sync.WaitGroup
		var mu sync.Mutex
		bodies := map[string]int{}
		var inProgress atomic.Int64
		start := make(chan struct{})
		for range 20 {
			wg.Go(func() {
				<-start
				c, b, _ := post(key, 3)
				switch {
				case c == 201:
					mu.Lock()
					bodies[string(b)]++
					mu.Unlock()
				case c == 409 && problemCode(t, b) == "idempotency_in_progress":
					inProgress.Add(1)
				default:
					t.Errorf("status tak terduga %d: %s", c, b)
				}
			})
		}
		close(start)
		wg.Wait()
		if n := f.Count(`SELECT count(*) FROM orders`) - before; n != 1 {
			t.Fatalf("order baru %d, ingin 1", n)
		}
		if len(bodies) != 1 {
			t.Fatalf("respons 201 berbeda-beda: %d variasi", len(bodies))
		}
	})
	t.Run("kegagalan melepas key: retry dengan key sama boleh", func(t *testing.T) {
		key := uuid.NewString()
		c, b, _ := post(key, 1000)
		if c != 409 || problemCode(t, b) != "out_of_stock" {
			t.Fatalf("status %d: %s", c, b)
		}
		f.Receive(f.Central, f.ProductA, 1000)
		if c, b, _ := post(key, 1000); c != 201 {
			t.Fatalf("retry setelah restock %d: %s", c, b)
		}
	})
	f.AssertInvariants()
}

// ATP online toko: buffer disisihkan untuk walk-in, toko OFFLINE = 0.
func TestOrder_OnlineATPForStores(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 3)
	f.SetBuffer(f.StoreA, f.ProductA, 2)
	f.Receive(f.StoreB, f.ProductB, 10) // StoreB OFFLINE

	if _, err := f.CreateOrder(inventorytest.Line(f.ProductA, 2)); appCode(err) != "out_of_stock" {
		t.Fatalf("available 3 - buffer 2 = ATP 1; qty 2 harus out_of_stock, dapat %v", err)
	}
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	if *o.FulfillmentLocationID != f.StoreA {
		t.Fatal("harus dipenuhi toko A")
	}
	if b := f.Balance(f.StoreA, f.ProductA); b.AvailableQty != 2 {
		t.Fatalf("available %d: buffer 2 unit harus tetap untuk walk-in", b.AvailableQty)
	}
	if _, err := f.CreateOrder(inventorytest.Line(f.ProductB, 1)); appCode(err) != "out_of_stock" {
		t.Fatalf("toko OFFLINE harus ATP 0, dapat %v", err)
	}
	f.SetSyncState(f.StoreB, "ONLINE")
	if o := f.MustOrder(inventorytest.Line(f.ProductB, 1)); *o.FulfillmentLocationID != f.StoreB {
		t.Fatal("toko ONLINE harus bisa memenuhi")
	}

	// Gudang pusat diprioritaskan di atas toko.
	f.Receive(f.Central, f.ProductB, 5)
	if o := f.MustOrder(inventorytest.Line(f.ProductB, 1)); *o.FulfillmentLocationID != f.Central {
		t.Fatal("gudang pusat harus diprioritaskan")
	}
	// Semua line di satu lokasi: A hanya ada di toko A, B ada di pusat dan toko B,
	// jadi order A+B tidak bisa dipenuhi satu lokasi.
	if _, err := f.CreateOrder(inventorytest.Line(f.ProductA, 1), inventorytest.Line(f.ProductB, 1)); appCode(err) != "out_of_stock" {
		t.Fatalf("order lintas lokasi harus out_of_stock (all-or-nothing satu lokasi), dapat %v", err)
	}
	f.AssertInvariants()
}

func TestOrder_CancelAndFulfill(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 10)
	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	cancel := func(p *auth.Principal, id uuid.UUID) (int, dto.OrderResponse, []byte) {
		rec := f.Do(p, http.MethodPost, "/api/v1/orders/"+id.String()+"/cancel", map[string]any{"reason": "Pelanggan minta batal ✓"}, nil)
		var o dto.OrderResponse
		if rec.Code == 200 {
			inventorytest.Decode(t, rec, &o)
		}
		return rec.Code, o, rec.Body.Bytes()
	}
	fulfill := func(p *auth.Principal, id uuid.UUID) (int, dto.OrderResponse, []byte) {
		rec := f.Do(p, http.MethodPost, "/api/v1/orders/"+id.String()+"/fulfill", nil, nil)
		var o dto.OrderResponse
		if rec.Code == 200 {
			inventorytest.Decode(t, rec, &o)
		}
		return rec.Code, o, rec.Body.Bytes()
	}

	t.Run("batal PENDING_PAYMENT melepas stok; batal ulang aman", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 3))
		code, got, body := cancel(ops, o.ID)
		if code != 200 || got.Status != "CANCELLED" || got.Reservations[0].Status != "RELEASED" {
			t.Fatalf("batal %d: %s", code, body)
		}
		if b := f.Balance(f.Central, f.ProductA); b.ReservedQty != 0 {
			t.Fatalf("reserved %d", b.ReservedQty)
		}
		if code, got, _ := cancel(ops, o.ID); code != 200 || got.Status != "CANCELLED" {
			t.Fatal("batal ulang harus 200")
		}
		if code, _, body := fulfill(ops, o.ID); code != 409 || problemCode(t, body) != "invalid_transition" {
			t.Fatalf("fulfill order batal: %d %s", code, body)
		}
	})
	t.Run("fulfill PENDING ditolak; PAID → SHIPPED dengan COMMIT_SALE", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 2))
		if code, _, body := fulfill(ops, o.ID); code != 409 || problemCode(t, body) != "invalid_transition" {
			t.Fatalf("fulfill PENDING: %d %s", code, body)
		}
		f.Pay(o)
		before := f.Balance(f.Central, f.ProductA)
		code, got, body := fulfill(ops, o.ID)
		if code != 200 || got.Status != "SHIPPED" || got.Reservations[0].Status != "COMMITTED" {
			t.Fatalf("fulfill %d: %s", code, body)
		}
		after := f.Balance(f.Central, f.ProductA)
		if before.PhysicalQty-after.PhysicalQty != 2 || before.ReservedQty-after.ReservedQty != 2 {
			t.Fatalf("COMMIT_SALE salah: fisik %d→%d reserved %d→%d", before.PhysicalQty, after.PhysicalQty, before.ReservedQty, after.ReservedQty)
		}
		var path []string
		for _, h := range got.History {
			path = append(path, h.ToStatus)
		}
		if want := "PENDING_PAYMENT,PAID,FULFILLING,SHIPPED"; join(path) != want {
			t.Fatalf("riwayat %s, ingin %s", join(path), want)
		}
		if code, got, _ := fulfill(ops, o.ID); code != 200 || got.Status != "SHIPPED" {
			t.Fatal("fulfill ulang harus aman")
		}
		if code, _, body := cancel(ops, o.ID); code != 409 || problemCode(t, body) != "invalid_transition" {
			t.Fatalf("batal SHIPPED: %d %s", code, body)
		}
	})
	t.Run("batal PAID menandai refund", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		f.Pay(o)
		code, got, body := cancel(ops, o.ID)
		if code != 200 || got.Status != "CANCELLED" {
			t.Fatalf("%d %s", code, body)
		}
		var meta map[string]any
		_ = json.Unmarshal(got.Metadata, &meta)
		if meta["refund_required"] != true {
			t.Fatalf("metadata %v", meta)
		}
	})
	t.Run("alasan batal wajib", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		rec := f.Do(ops, http.MethodPost, "/api/v1/orders/"+o.ID.String()+"/cancel", map[string]any{"reason": "  "}, nil)
		if rec.Code != 422 {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("authz: tanpa permission 403, lokasi di luar scope 403, tidak ditemukan 404", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		storeOnly := inventorytest.StaffAt([]uuid.UUID{f.StoreA}, inventorytest.AllInventoryPerms...)
		readOnly := inventorytest.Staff("order.read")
		for _, tc := range []struct {
			name   string
			p      *auth.Principal
			method string
			path   string
			body   any
			status int
		}{
			{"anonim", nil, http.MethodGet, "/orders/" + o.ID.String(), nil, 401},
			{"get luar scope", storeOnly, http.MethodGet, "/orders/" + o.ID.String(), nil, 403},
			{"cancel luar scope", storeOnly, http.MethodPost, "/orders/" + o.ID.String() + "/cancel", map[string]any{"reason": "coba batal"}, 403},
			{"fulfill luar scope", storeOnly, http.MethodPost, "/orders/" + o.ID.String() + "/fulfill", nil, 403},
			{"cancel tanpa permission", readOnly, http.MethodPost, "/orders/" + o.ID.String() + "/cancel", map[string]any{"reason": "coba batal"}, 403},
			{"create tanpa permission", readOnly, http.MethodPost, "/orders", createBody(lineBody(f.ProductA, 1)), 403},
			{"tidak ada", ops, http.MethodGet, "/orders/" + uuid.NewString(), nil, 404},
			{"id bukan uuid", ops, http.MethodGet, "/orders/bukan-uuid", nil, 404},
		} {
			rec := f.Do(tc.p, tc.method, "/api/v1"+tc.path, tc.body, map[string]string{controller.IdempotencyKeyHeader: uuid.NewString()})
			if rec.Code != tc.status {
				t.Errorf("%s: status %d, ingin %d: %s", tc.name, rec.Code, tc.status, rec.Body)
			}
		}
		if f.OrderStatus(o.ID) != "PENDING_PAYMENT" {
			t.Fatal("aksi yang ditolak tidak boleh mengubah order")
		}
	})
	f.AssertInvariants()
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
