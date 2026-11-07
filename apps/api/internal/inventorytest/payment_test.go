package inventorytest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/dto"
	"mganik/api/internal/inventorytest"
	"mganik/api/internal/middleware"
	"mganik/api/testutil"
)

const payPath = "/api/v1/webhooks/payments/mock"

func TestPaymentWebhook(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 10)

	t.Run("bayar: PENDING_PAYMENT → PAID, reservasi tidak lagi kedaluwarsa", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 2))
		r := f.Pay(o)
		if r.Outcome != "PAID" || r.OrderStatus != "PAID" || r.Duplicate || r.PaymentID == nil {
			t.Fatalf("respons %+v", r)
		}
		if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE' AND expires_at IS NULL`, o.ID); n != 1 {
			t.Fatal("reservasi order dibayar harus tanpa expires_at")
		}
		if n := f.Count(`SELECT count(*) FROM orders WHERE id = ? AND paid_at IS NOT NULL`, o.ID); n != 1 {
			t.Fatal("paid_at kosong")
		}
	})

	t.Run("webhook dobel (berurutan dan bersamaan) → satu pembayaran, satu transisi", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		body := inventorytest.PayBody("PAY-DOBEL-"+uuid.NewString(), o.ID, o.Total)
		var wg sync.WaitGroup
		var mu sync.Mutex
		outcomes := map[string]int{}
		for range 10 {
			wg.Go(func() {
				rec := f.Signed(payPath, body)
				if rec.Code != 200 {
					t.Errorf("status %d: %s", rec.Code, rec.Body)
					return
				}
				var r dto.PaymentWebhookResponse
				_ = json.Unmarshal(rec.Body.Bytes(), &r)
				mu.Lock()
				outcomes[r.Outcome]++
				mu.Unlock()
			})
		}
		wg.Wait()
		rec := f.Signed(payPath, body)
		var again dto.PaymentWebhookResponse
		inventorytest.Decode(t, rec, &again)
		if outcomes["PAID"] != 1 || outcomes["ALREADY_PROCESSED"] != 9 || !again.Duplicate {
			t.Fatalf("hasil %v, ulang %+v", outcomes, again)
		}
		if n := f.Count(`SELECT count(*) FROM payments WHERE order_id = ?`, o.ID); n != 1 {
			t.Fatalf("pembayaran %d", n)
		}
		if n := f.Count(`SELECT count(*) FROM order_status_history WHERE order_id = ? AND to_status = 'PAID'`, o.ID); n != 1 {
			t.Fatalf("transisi PAID %d", n)
		}
	})

	t.Run("signature salah, timestamp basi, nominal beda ditolak", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		body, _ := json.Marshal(inventorytest.PayBody("PAY-X-"+uuid.NewString(), o.ID, o.Total))

		rec := f.Do(nil, http.MethodPost, payPath, body, map[string]string{
			middleware.WebhookTimestampHeader: strconv.FormatInt(f.Clock.Now().Unix(), 10),
			middleware.WebhookSignatureHeader: middleware.NewWebhookSigner([]byte("secret-palsu")).Sign(strconv.FormatInt(f.Clock.Now().Unix(), 10), body),
		})
		if rec.Code != 401 || problemCode(t, rec.Body.Bytes()) != "invalid_signature" {
			t.Fatalf("signature salah: %d %s", rec.Code, rec.Body)
		}
		old := strconv.FormatInt(f.Clock.Now().Add(-6*time.Minute).Unix(), 10)
		rec = f.Do(nil, http.MethodPost, payPath, body, map[string]string{
			middleware.WebhookTimestampHeader: old,
			middleware.WebhookSignatureHeader: middleware.NewWebhookSigner([]byte(inventorytest.WebhookSecret)).Sign(old, body),
		})
		if rec.Code != 401 || problemCode(t, rec.Body.Bytes()) != "stale_timestamp" {
			t.Fatalf("timestamp basi: %d %s", rec.Code, rec.Body)
		}
		rec = f.Signed(payPath, inventorytest.PayBody("PAY-X-"+uuid.NewString(), o.ID, o.Total-1))
		if rec.Code != 422 || problemCode(t, rec.Body.Bytes()) != "amount_mismatch" {
			t.Fatalf("nominal beda: %d %s", rec.Code, rec.Body)
		}
		wrongCurrency := inventorytest.PayBody("PAY-X-"+uuid.NewString(), o.ID, o.Total)
		wrongCurrency["currency"] = "USD"
		if rec = f.Signed(payPath, wrongCurrency); rec.Code != 422 {
			t.Fatalf("mata uang beda: %d", rec.Code)
		}
		if rec = f.Signed(payPath, inventorytest.PayBody("PAY-X-"+uuid.NewString(), uuid.New(), 1)); rec.Code != 404 {
			t.Fatalf("order tidak ada: %d", rec.Code)
		}
		if n := f.Count(`SELECT count(*) FROM payments WHERE order_id = ?`, o.ID); n != 0 || f.OrderStatus(o.ID) != "PENDING_PAYMENT" {
			t.Fatal("webhook yang ditolak tidak boleh meninggalkan efek")
		}
	})

	t.Run("pembayaran FAILED dicatat tanpa transisi", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		body := inventorytest.PayBody("PAY-F-"+uuid.NewString(), o.ID, o.Total)
		body["status"] = "FAILED"
		rec := f.Signed(payPath, body)
		var r dto.PaymentWebhookResponse
		inventorytest.Decode(t, rec, &r)
		if r.Outcome != "PAYMENT_FAILED" || f.OrderStatus(o.ID) != "PENDING_PAYMENT" {
			t.Fatalf("%+v", r)
		}
	})

	t.Run("provider_ref sama untuk order lain ditolak", func(t *testing.T) {
		o1 := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		o2 := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		ref := "PAY-REF-" + uuid.NewString()
		if rec := f.Signed(payPath, inventorytest.PayBody(ref, o1.ID, o1.Total)); rec.Code != 200 {
			t.Fatal(rec.Body)
		}
		rec := f.Signed(payPath, inventorytest.PayBody(ref, o2.ID, o2.Total))
		if rec.Code != 409 || problemCode(t, rec.Body.Bytes()) != "payment_ref_conflict" {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	})

	t.Run("pembayaran kedua untuk order yang sudah dibayar → refund", func(t *testing.T) {
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		f.Pay(o)
		r := f.Pay(o)
		if r.Outcome != "REFUND_REQUIRED" || r.OrderStatus != "PAID" {
			t.Fatalf("%+v", r)
		}
	})
	f.AssertInvariants()
}

// Bayar telat setelah order EXPIRED: stok dipesan ulang (PAID), atau kalau
// stok sudah habis → NEEDS_ACTION + refund (tidak pernah PAID tanpa stok).
func TestPaymentWebhook_LatePayment(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 3)
	ctx := context.Background()

	ok := f.MustOrder(inventorytest.Line(f.ProductA, 2))
	short := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	f.Clock.Advance(f.TTL + time.Second)
	if n, err := f.Orders.ExpireDueOrders(ctx, 10); err != nil || n != 2 {
		t.Fatalf("expire %d %v", n, err)
	}
	if f.OrderStatus(ok.ID) != "EXPIRED" || f.Balance(f.Central, f.ProductA).ReservedQty != 0 {
		t.Fatal("order harus EXPIRED dan stok dilepas")
	}

	// Order lain mengambil 2 dari 3 unit; order "short" (1 unit) masih bisa
	// dipesan ulang, order "ok" (2 unit) tidak.
	other := f.MustOrder(inventorytest.Line(f.ProductA, 2))
	_ = other

	r := f.Pay(short)
	if r.Outcome != "LATE_PAYMENT_REALLOCATED" || r.OrderStatus != "PAID" {
		t.Fatalf("bayar telat dengan stok: %+v", r)
	}
	if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE' AND expires_at IS NULL`, short.ID); n != 1 {
		t.Fatal("reservasi baru tanpa expiry harus ada")
	}

	r = f.Pay(ok)
	if r.Outcome != "LATE_PAYMENT_NEEDS_ACTION" || r.OrderStatus != "NEEDS_ACTION" {
		t.Fatalf("bayar telat tanpa stok: %+v", r)
	}
	var meta map[string]any
	var raw []byte
	if err := f.DB.Raw(`SELECT metadata FROM orders WHERE id = ?`, ok.ID).Row().Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &meta)
	if meta["refund_required"] != true || meta["refund_reason"] != "late_payment_out_of_stock" {
		t.Fatalf("metadata %v", meta)
	}
	if n := f.Count(`SELECT count(*) FROM order_status_history WHERE order_id = ? AND from_status = 'EXPIRED' AND to_status = 'NEEDS_ACTION'`, ok.ID); n != 1 {
		t.Fatal("riwayat EXPIRED → NEEDS_ACTION tidak ada")
	}
	if n := f.Count(`SELECT count(*) FROM outbox_events WHERE event_type = 'order.refund_requested.v1' AND aggregate_id = ?`, ok.ID); n != 1 {
		t.Fatal("event refund tidak ada")
	}
	f.AssertInvariants()
}

const importPath = "/api/v1/channels/marketplace/orders"

func importBody(ref string, lines ...map[string]any) map[string]any {
	return map[string]any{"channel_code": "SHOPEE", "external_ref": ref, "lines": lines}
}

func mpLine(sku string, qty, price int64) map[string]any {
	return map[string]any{"sku": sku, "qty": qty, "unit_price": price}
}

func TestMarketplaceImport(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)

	t.Run("impor: PAID, reservasi tanpa expiry, pembayaran dicatat; impor ulang = order sama", func(t *testing.T) {
		body := importBody("SHP-0001", mpLine(inventorytest.SKUA, 2, 45000))
		body["lines"].([]map[string]any)[0]["discount"] = 5000
		rec := f.Signed(importPath, body)
		if rec.Code != 201 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var first dto.MarketplaceImportResponse
		inventorytest.Decode(t, rec, &first)
		o := first.Order
		if o.Status != "PAID" || o.Total != 85000 || o.Discount != 5000 || *o.ExternalRef != "SHP-0001" || first.Duplicate {
			t.Fatalf("order %+v", o)
		}
		if len(o.Reservations) != 1 || o.Reservations[0].ExpiresAt != nil || len(o.Payments) != 1 {
			t.Fatalf("reservasi/pembayaran salah: %+v %+v", o.Reservations, o.Payments)
		}
		rec = f.Signed(importPath, body)
		var again dto.MarketplaceImportResponse
		inventorytest.Decode(t, rec, &again)
		if rec.Code != 200 || !again.Duplicate || again.Order.ID != o.ID {
			t.Fatalf("impor ulang %d %+v", rec.Code, again)
		}
	})

	t.Run("impor dobel bersamaan → satu order", func(t *testing.T) {
		body := importBody("SHP-0002", mpLine(inventorytest.SKUA, 1, 45000))
		var wg sync.WaitGroup
		var mu sync.Mutex
		codes := map[int]int{}
		for range 10 {
			wg.Go(func() {
				rec := f.Signed(importPath, body)
				mu.Lock()
				codes[rec.Code]++
				mu.Unlock()
			})
		}
		wg.Wait()
		if codes[201] != 1 || codes[200] != 9 {
			t.Fatalf("status %v", codes)
		}
		if n := f.Count(`SELECT count(*) FROM orders WHERE channel_code = 'SHOPEE' AND external_ref = 'SHP-0002'`); n != 1 {
			t.Fatalf("order %d", n)
		}
	})

	t.Run("stok tidak cukup → order tetap dibuat, NEEDS_ACTION", func(t *testing.T) {
		rec := f.Signed(importPath, importBody("SHP-0003", mpLine(inventorytest.SKUA, 50, 45000)))
		if rec.Code != 201 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var r dto.MarketplaceImportResponse
		inventorytest.Decode(t, rec, &r)
		if r.Order.Status != "NEEDS_ACTION" || len(r.Order.Reservations) != 0 {
			t.Fatalf("%+v", r.Order)
		}
		var path []string
		for _, h := range r.Order.History {
			path = append(path, h.ToStatus)
		}
		if join(path) != "PAID,NEEDS_ACTION" {
			t.Fatalf("riwayat %v", path)
		}
	})

	t.Run("validasi dan signature", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			body   map[string]any
			status int
			code   string
		}{
			{"SKU tidak dikenal", importBody("SHP-9", mpLine("TIDAK-ADA", 1, 1000)), 422, "validation_failed"},
			{"channel bukan marketplace", map[string]any{"channel_code": "D2C_WEB", "external_ref": "X", "lines": []any{mpLine(inventorytest.SKUA, 1, 1)}}, 422, "channel_invalid"},
			{"SKU duplikat", importBody("SHP-10", mpLine(inventorytest.SKUA, 1, 1), mpLine(inventorytest.SKUA, 1, 1)), 422, "validation_failed"},
			{"diskon melebihi nilai line", importBody("SHP-11", map[string]any{"sku": inventorytest.SKUA, "qty": 1, "unit_price": 100, "discount": 101}), 422, "validation_failed"},
			{"qty 0", importBody("SHP-12", mpLine(inventorytest.SKUA, 0, 1)), 422, "validation_failed"},
		} {
			rec := f.Signed(importPath, tc.body)
			if rec.Code != tc.status || problemCode(t, rec.Body.Bytes()) != tc.code {
				t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body)
			}
		}
		rec := f.Do(nil, http.MethodPost, importPath, importBody("SHP-13", mpLine(inventorytest.SKUA, 1, 1)), nil)
		if rec.Code != 401 {
			t.Fatalf("tanpa signature: %d", rec.Code)
		}
	})
	f.AssertInvariants()
}
