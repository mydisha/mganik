package inventorytest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/controller"
	"mganik/api/internal/inventorytest"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

// H2: qty > 32767 dulu overflow karena parameter disimpulkan smallint dari
// kolom *_sign. Sekarang semua operand di-CAST ke bigint.
func TestApplyMovement_LargeQtyNoSmallintOverflow(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	ctx := context.Background()
	apply := func(typ string, qty int64) error {
		return f.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(`INSERT INTO inventory_balances (location_id, product_id) VALUES (?, ?) ON CONFLICT DO NOTHING`,
				f.Central, f.ProductA).Error; err != nil {
				return err
			}
			_, err := f.Inv.ApplyMovement(ctx, tx, service.MovementInput{
				LocationID: f.Central, ProductID: f.ProductA, Type: typ, Qty: qty, RefType: "TEST", RefID: uuid.New(),
				IdempotencyKey: "big:" + uuid.NewString(), Actor: service.SystemLedgerActor,
			})
			return err
		})
	}
	for _, step := range []struct {
		typ string
		qty int64
	}{
		{service.MovementReceipt, 32768}, {service.MovementReceipt, 50000}, {service.MovementReceipt, service.MaxMovementQty},
		{service.MovementReserve, 32768}, {service.MovementAdjustCount, -40000},
	} {
		if err := apply(step.typ, step.qty); err != nil {
			t.Fatalf("%s %d: %v", step.typ, step.qty, err)
		}
	}
	b := f.Balance(f.Central, f.ProductA)
	if want := int64(32768 + 50000 + service.MaxMovementQty - 40000); b.PhysicalQty != want || b.ReservedQty != 32768 {
		t.Fatalf("saldo %+v, ingin fisik %d reserved 32768", b, want)
	}

	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	for _, delta := range []int64{40000, service.MaxMovementQty, -32768} {
		rec := f.Do(ops, http.MethodPost, "/api/v1/inventory/adjustments", map[string]any{
			"location_id": f.Hub, "product_id": f.ProductB, "qty_delta": delta, "reason": "Opname besar",
		}, map[string]string{controller.IdempotencyKeyHeader: uuid.NewString()})
		if rec.Code != http.StatusCreated {
			t.Fatalf("adjustment %d: %d %s", delta, rec.Code, rec.Body)
		}
	}
	if b := f.Balance(f.Hub, f.ProductB); b.PhysicalQty != 40000+service.MaxMovementQty-32768 {
		t.Fatalf("fisik hub %d", b.PhysicalQty)
	}
	f.AssertLedgerInvariant()
}

// M1 (repro QA): stok 3, reserved 2, pembatalan paralel sedang memegang
// reservasi. Dulu: retry habis lalu ADJUST_DISCREPANCY 2 (stok siluman,
// available akhir 2). Sekarang penjualan fakta MENUNGGU reservasi itu.
func TestRecordFactSale_QARepro_NoPhantomStock(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 3)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 2))
	ctx := context.Background()

	// "Pembatalan" yang sedang berjalan: memegang reservasi, lalu (setelah
	// penjualan fakta mulai menunggu) melepasnya dan commit.
	holder := f.DB.Begin()
	var res model.StockReservation
	if err := holder.Raw(`SELECT * FROM stock_reservations WHERE order_id = ? FOR NO KEY UPDATE`, o.ID).Scan(&res).Error; err != nil {
		t.Fatal(err)
	}
	type result struct {
		res service.FactSaleResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := factSale(f, f.StoreA, f.ProductA, 3, "pos:qa-repro")
		done <- result{r, err}
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := f.Inv.ApplyMovement(ctx, holder, service.MovementInput{
		LocationID: res.LocationID, ProductID: res.ProductID, Type: service.MovementRelease, Qty: res.Qty,
		RefType: "ORDER", RefID: o.ID, IdempotencyKey: "reservation:" + res.ID.String() + ":release", Actor: service.SystemLedgerActor,
	}); err != nil {
		t.Fatal(err)
	}
	if err := holder.Exec(`UPDATE stock_reservations SET status = 'RELEASED', closed_at = now(), close_reason = 'uji' WHERE id = ?`, res.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.res.DiscrepancyQty != 0 || len(got.res.Preempted) != 0 {
		t.Fatalf("ingin tanpa selisih/preempt, dapat %+v", got.res)
	}
	if b := f.Balance(f.StoreA, f.ProductA); b.PhysicalQty != 0 || b.AvailableQty != 0 {
		t.Fatalf("saldo akhir %+v, ingin fisik 0 available 0", b)
	}
	if n := f.Count(`SELECT count(*) FROM inventory_movements WHERE movement_type = 'ADJUST_DISCREPANCY'`); n != 0 {
		t.Fatal("tidak boleh ada stok siluman")
	}
	f.AssertInvariants()
}

// Kontrak Fase 4: produk sama dua kali dalam satu tx aman; beberapa produk
// lewat RecordFactSales.
func TestRecordFactSales_MultiProductAndRepeatedProduct(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 2)
	f.Receive(f.StoreA, f.ProductB, 1)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1), inventorytest.Line(f.ProductB, 1))
	ctx := context.Background()
	in := func(prod uuid.UUID, qty int64, key string) service.FactSaleInput {
		return service.FactSaleInput{LocationID: f.StoreA, ProductID: prod, Qty: qty, RefType: "POS_TRANSACTION",
			RefID: uuid.New(), IdempotencyKey: key, Actor: service.SystemLedgerActor}
	}
	var results []service.FactSaleResult
	err := f.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		results, err = f.Inv.RecordFactSales(ctx, tx, []service.FactSaleInput{
			in(f.ProductB, 1, "pos:m:b"), in(f.ProductA, 2, "pos:m:a"),
		})
		if err != nil {
			return err
		}
		// Produk sama sekali lagi dalam tx yang sama.
		_, err = f.Inv.RecordFactSale(ctx, tx, in(f.ProductA, 1, "pos:m:a2"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Sale.Movement.ProductID != f.ProductB || results[1].Sale.Movement.ProductID != f.ProductA {
		t.Fatal("hasil harus mengikuti urutan input")
	}
	if len(results[0].Preempted) != 1 || len(results[1].Preempted) != 1 || results[0].Preempted[0].OrderID != o.ID {
		t.Fatalf("kedua line order harus di-preempt: %+v", results)
	}
	// A: fisik 2, terjual 2 lalu 1 → selisih 1 (benar-benar kurang setelah semua reservasi di-preempt).
	if n := f.Count(`SELECT COALESCE(sum(physical_delta), 0) FROM inventory_movements WHERE movement_type = 'ADJUST_DISCREPANCY' AND product_id = ?`, f.ProductA); n != 1 {
		t.Fatalf("selisih A %d, ingin 1", n)
	}
	f.AssertInvariants()
}

// L1: di dalam kelompok prioritas, reservasi terkecil yang cukup didahulukan.
func TestRecordFactSale_PreemptsSmallestSufficient(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 4)
	small := f.MustOrder(inventorytest.Line(f.ProductA, 1)) // lebih lama
	f.Clock.Advance(time.Second)
	big := f.MustOrder(inventorytest.Line(f.ProductA, 3)) // lebih baru

	res, err := factSale(f, f.StoreA, f.ProductA, 1, "pos:l1:a")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Preempted) != 1 || res.Preempted[0].OrderID != small.ID {
		t.Fatalf("butuh 1: ingin preempt order qty 1 (terkecil yang cukup), dapat %+v", res.Preempted)
	}
	res, err = factSale(f, f.StoreA, f.ProductA, 2, "pos:l1:b")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Preempted) != 1 || res.Preempted[0].OrderID != big.ID {
		t.Fatalf("butuh 2: ingin preempt order qty 3, dapat %+v", res.Preempted)
	}
	f.AssertInvariants()
}

// M2: satu event refund per pembayaran, selalu dengan payment_id.
func TestRefund_NoDuplicateEvents(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)
	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	first := f.Pay(o)
	second := f.Pay(o) // bayar ganda → refund untuk pembayaran kedua
	if second.Outcome != "REFUND_REQUIRED" {
		t.Fatalf("%+v", second)
	}
	cancelBody := map[string]any{"reason": "Pelanggan minta batal"}
	for range 2 {
		if rec := f.Do(ops, http.MethodPost, "/api/v1/orders/"+o.ID.String()+"/cancel", cancelBody, nil); rec.Code != 200 {
			t.Fatalf("batal %d %s", rec.Code, rec.Body)
		}
	}
	var payloads []model.JSON
	if err := f.DB.Raw(`SELECT payload FROM outbox_events WHERE event_type = 'order.refund_requested.v1' AND aggregate_id = ? ORDER BY created_at, id`, o.ID).Scan(&payloads).Error; err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, p := range payloads {
		var ev struct {
			PaymentID *string `json:"payment_id"`
		}
		_ = json.Unmarshal(p, &ev)
		if ev.PaymentID == nil {
			t.Fatal("event refund tanpa payment_id")
		}
		seen[*ev.PaymentID]++
	}
	if len(payloads) != 2 || seen[first.PaymentID.String()] != 1 || seen[second.PaymentID.String()] != 1 {
		t.Fatalf("event refund %d, per pembayaran %v; ingin tepat satu per pembayaran", len(payloads), seen)
	}
}

// M4: order split hanya bisa di-fulfill principal yang berwenang di SEMUA lokasi reservasinya.
func TestFulfill_SplitOrderNeedsAllLocations(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 1)
	f.Receive(f.StoreA, f.ProductB, 1)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1), inventorytest.Line(f.ProductB, 1))
	f.Pay(o)
	f.Receive(f.Hub, f.ProductA, 3)
	res, err := factSale(f, f.StoreA, f.ProductA, 1, "pos:split")
	if err != nil || len(res.Preempted) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	payload, _ := json.Marshal(service.PreemptedEvent{
		ReservationID: res.Preempted[0].ReservationID, OrderID: o.ID, OrderLineID: res.Preempted[0].OrderLineID,
		LocationID: f.StoreA, ProductID: f.ProductA, Qty: 1, ConflictID: res.Preempted[0].ConflictID,
	})
	if err := f.DB.Transaction(func(tx *gorm.DB) error {
		return f.Orders.HandleReservationPreempted(context.Background(), tx, payload)
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(`SELECT count(DISTINCT location_id) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE'`, o.ID); n != 2 {
		t.Fatalf("order harus terpecah ke 2 lokasi, dapat %d", n)
	}
	path := "/api/v1/orders/" + o.ID.String() + "/fulfill"
	storeOnly := inventorytest.StaffAt([]uuid.UUID{f.StoreA}, inventorytest.AllInventoryPerms...)
	if rec := f.Do(storeOnly, http.MethodPost, path, nil, nil); rec.Code != 403 {
		t.Fatalf("hanya toko A: %d %s", rec.Code, rec.Body)
	}
	if rec := f.Do(storeOnly, http.MethodPost, "/api/v1/orders/"+o.ID.String()+"/cancel", map[string]any{"reason": "coba batal"}, nil); rec.Code != 403 {
		t.Fatalf("batal oleh toko A saja: %d", rec.Code)
	}
	both := inventorytest.StaffAt([]uuid.UUID{f.StoreA, f.Hub}, inventorytest.AllInventoryPerms...)
	rec := f.Do(both, http.MethodPost, path, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("toko A + hub: %d %s", rec.Code, rec.Body)
	}
	f.AssertInvariants()
}

// L2: karakter kontrol di teks bebas → 422 invalid_characters, bukan 500.
func TestFreeText_ControlCharactersRejected(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)
	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	key := func() map[string]string { return map[string]string{controller.IdempotencyKeyHeader: uuid.NewString()} }
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	cases := []struct {
		name, method, path string
		body               any
		signed             bool
	}{
		{"note order NUL", http.MethodPost, "/api/v1/orders", map[string]any{"lines": []any{lineBody(f.ProductA, 1)}, "note": "a\u0000b"}, false},
		{"alasan adjustment NUL", http.MethodPost, "/api/v1/inventory/adjustments",
			map[string]any{"location_id": f.Central, "product_id": f.ProductA, "qty_delta": 1, "reason": "opname\u0000"}, false},
		{"alasan karantina bel", http.MethodPost, "/api/v1/inventory/quarantine",
			map[string]any{"location_id": f.Central, "product_id": f.ProductA, "qty": 1, "direction": "IN", "reason": "rusak\u0007"}, false},
		{"alasan batal escape", http.MethodPost, "/api/v1/orders/" + o.ID.String() + "/cancel", map[string]any{"reason": "batal \u001b[31m"}, false},
		{"external_ref NUL", http.MethodPost, importPath, importBody("SHP-\u0000", mpLine(inventorytest.SKUA, 1, 1)), true},
		{"provider_ref NUL", http.MethodPost, payPath, inventorytest.PayBody("PAY-\u0000", o.ID, o.Total), true},
	}
	for _, tc := range cases {
		var code int
		var body []byte
		if tc.signed {
			rec := f.Signed(tc.path, tc.body)
			code, body = rec.Code, rec.Body.Bytes()
		} else {
			rec := f.Do(ops, tc.method, tc.path, tc.body, key())
			code, body = rec.Code, rec.Body.Bytes()
		}
		if code != 422 || problemCode(t, body) != "invalid_characters" {
			t.Errorf("%s: %d %s", tc.name, code, body)
		}
	}
	// Baris baru dan tab tetap boleh.
	rec := f.Do(ops, http.MethodPost, "/api/v1/orders", map[string]any{
		"lines": []any{lineBody(f.ProductA, 1)}, "note": "baris 1\nbaris 2\tkolom",
	}, key())
	if rec.Code != 201 {
		t.Fatalf("catatan dengan \\n\\t: %d %s", rec.Code, rec.Body)
	}
}

// L4: external_ref sama dengan isi berbeda → 409 external_ref_conflict.
func TestMarketplaceImport_SameRefDifferentPayload(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)
	body := importBody("SHP-L4", mpLine(inventorytest.SKUA, 1, 45000))
	if rec := f.Signed(importPath, body); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	if rec := f.Signed(importPath, body); rec.Code != 200 {
		t.Fatalf("isi sama: %d", rec.Code)
	}
	rec := f.Signed(importPath, importBody("SHP-L4", mpLine(inventorytest.SKUA, 2, 45000)))
	if rec.Code != 409 || problemCode(t, rec.Body.Bytes()) != "external_ref_conflict" {
		t.Fatalf("isi beda: %d %s", rec.Code, rec.Body)
	}
	if n := f.Count(`SELECT count(*) FROM orders WHERE external_ref = 'SHP-L4'`); n != 1 {
		t.Fatalf("order %d", n)
	}
}

// L7: 409 out_of_stock menyebut line yang kurang.
func TestOrder_OutOfStockNamesLines(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)
	f.Receive(f.Hub, f.ProductB, 5)
	p := inventorytest.Staff("order.create")
	post := func(lines ...map[string]any) apperror.Problem {
		rec := f.Do(p, http.MethodPost, "/api/v1/orders", createBody(lines...), map[string]string{controller.IdempotencyKeyHeader: uuid.NewString()})
		if rec.Code != 409 {
			t.Fatalf("status %d %s", rec.Code, rec.Body)
		}
		var prob apperror.Problem
		inventorytest.Decode(t, rec, &prob)
		return prob
	}
	prob := post(lineBody(f.ProductA, 1), lineBody(f.ProductB, 9))
	if prob.Code != "out_of_stock" || len(prob.Errors) != 1 || prob.Errors[0].Field != "lines[1].qty" || prob.Errors[0].Code != "insufficient_stock" {
		t.Fatalf("%+v", prob)
	}
	prob = post(lineBody(f.ProductA, 1), lineBody(f.ProductB, 1))
	if len(prob.Errors) != 2 || prob.Errors[0].Code != "not_at_single_location" {
		t.Fatalf("tidak bersama di satu lokasi: %+v", prob)
	}
}
