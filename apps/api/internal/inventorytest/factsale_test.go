package inventorytest_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/dto"
	"mganik/api/internal/inventorytest"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

func factSale(f *inventorytest.Fixture, loc, prod uuid.UUID, qty int64, key string) (service.FactSaleResult, error) {
	var res service.FactSaleResult
	err := f.DB.Transaction(func(tx *gorm.DB) error {
		posTxn := uuid.New()
		var err error
		res, err = f.Inv.RecordFactSale(context.Background(), tx, service.FactSaleInput{
			LocationID: loc, ProductID: prod, Qty: qty, RefType: "POS_TRANSACTION", RefID: posTxn,
			IdempotencyKey: key, Actor: service.SystemLedgerActor,
		})
		return err
	})
	return res, err
}

func TestRecordFactSale_AvailableEnough(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 5)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 2)) // reservasi 2 di toko A

	res, err := factSale(f, f.StoreA, f.ProductA, 3, "pos:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Preempted) != 0 || res.DiscrepancyQty != 0 || res.Duplicate || res.Sale.Movement.PhysicalDelta != -3 {
		t.Fatalf("hasil %+v", res)
	}
	if b := f.Balance(f.StoreA, f.ProductA); b.PhysicalQty != 2 || b.ReservedQty != 2 || b.AvailableQty != 0 {
		t.Fatalf("saldo %+v", b)
	}
	if f.OrderStatus(o.ID) != "PENDING_PAYMENT" {
		t.Fatal("order tidak boleh tersentuh")
	}

	dup, err := factSale(f, f.StoreA, f.ProductA, 3, "pos:1")
	if err != nil || !dup.Duplicate || dup.Sale.Movement.ID != res.Sale.Movement.ID {
		t.Fatalf("duplikat %+v %v", dup, err)
	}
	if b := f.Balance(f.StoreA, f.ProductA); b.PhysicalQty != 2 {
		t.Fatal("penjualan duplikat tidak boleh mengurangi stok lagi")
	}
	f.AssertInvariants()
}

// Urutan preempt: PENDING_PAYMENT terbaru dulu, lalu PAID terbaru.
func TestRecordFactSale_PreemptOrder(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 4)

	next := func() { f.Clock.Advance(time.Second) }
	pendOld := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	next()
	paidOld := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	f.Pay(paidOld)
	next()
	pendNew := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	next()
	paidNew := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	f.Pay(paidNew)
	if b := f.Balance(f.StoreA, f.ProductA); b.AvailableQty != 0 || b.ReservedQty != 4 {
		t.Fatalf("persiapan salah: %+v", b)
	}

	order := func(res service.FactSaleResult) []uuid.UUID {
		var ids []uuid.UUID
		for _, p := range res.Preempted {
			ids = append(ids, p.OrderID)
		}
		return ids
	}
	res, err := factSale(f, f.StoreA, f.ProductA, 2, "pos:a")
	if err != nil {
		t.Fatal(err)
	}
	if got := order(res); len(got) != 2 || got[0] != pendNew.ID || got[1] != pendOld.ID {
		t.Fatalf("preempt pertama %v, ingin [PENDING terbaru, PENDING lama]", got)
	}
	res, err = factSale(f, f.StoreA, f.ProductA, 1, "pos:b")
	if err != nil {
		t.Fatal(err)
	}
	if got := order(res); len(got) != 1 || got[0] != paidNew.ID {
		t.Fatalf("preempt kedua %v, ingin [PAID terbaru]", got)
	}
	if res.DiscrepancyQty != 0 {
		t.Fatal("tidak boleh ada selisih")
	}
	for _, id := range []uuid.UUID{pendNew.ID, pendOld.ID, paidNew.ID} {
		if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'PREEMPTED'`, id); n != 1 {
			t.Fatalf("reservasi order %s tidak PREEMPTED", id)
		}
	}
	if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE'`, paidOld.ID); n != 1 {
		t.Fatal("PAID lama harus tetap aktif")
	}
	if n := f.Count(`SELECT count(*) FROM allocation_conflicts WHERE kind = 'OFFLINE_SALE_PREEMPT' AND status = 'OPEN'`); n != 3 {
		t.Fatalf("konflik %d, ingin 3", n)
	}
	if n := f.Count(`SELECT count(*) FROM outbox_events WHERE event_type = 'reservation.preempted.v1'`); n != 3 {
		t.Fatalf("event preempt %d, ingin 3", n)
	}
	if n := f.Count(`SELECT count(*) FROM inventory_movements WHERE movement_type = 'PREEMPT'`); n != 3 {
		t.Fatalf("movement PREEMPT %d, ingin 3", n)
	}
	f.AssertInvariants()
}

// Fisik pun kurang: penjualan tetap diterima, selisih dicatat sebagai
// ADJUST_DISCREPANCY + konflik + anomali stok.
func TestRecordFactSale_Discrepancy(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 1)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1))

	res, err := factSale(f, f.StoreA, f.ProductA, 3, "pos:short")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Preempted) != 1 || res.Preempted[0].OrderID != o.ID || res.DiscrepancyQty != 2 || res.DiscrepancyConflictID == nil {
		t.Fatalf("hasil %+v", res)
	}
	if b := f.Balance(f.StoreA, f.ProductA); b.PhysicalQty != 0 || b.ReservedQty != 0 {
		t.Fatalf("saldo %+v", b)
	}
	if n := f.Count(`SELECT count(*) FROM inventory_movements WHERE movement_type = 'ADJUST_DISCREPANCY' AND physical_delta = 2`); n != 1 {
		t.Fatal("ADJUST_DISCREPANCY +2 tidak ada")
	}
	if n := f.Count(`SELECT count(*) FROM allocation_conflicts WHERE kind = 'STOCK_DISCREPANCY' AND qty = 2`); n != 1 {
		t.Fatal("konflik STOCK_DISCREPANCY tidak ada")
	}
	if n := f.Count(`SELECT count(*) FROM anomalies WHERE rule_code = 'STOCK_DISCREPANCY' AND subject_id = ?`, f.StoreA); n != 1 {
		t.Fatal("anomali stok tidak ada")
	}

	// Lokasi tanpa saldo sama sekali: saldo dibuat, seluruhnya selisih.
	res, err = factSale(f, f.Hub, f.ProductC, 2, "pos:nobalance")
	if err != nil || res.DiscrepancyQty != 2 {
		t.Fatalf("tanpa saldo: %+v %v", res, err)
	}
	// Anomali di-dedupe per lokasi-produk-hari: hit_count naik.
	if _, err := factSale(f, f.StoreA, f.ProductA, 1, "pos:short2"); err != nil {
		t.Fatal(err)
	}
	if n := f.Count(`SELECT hit_count FROM anomalies WHERE rule_code = 'STOCK_DISCREPANCY' AND subject_id = ?`, f.StoreA); n != 2 {
		t.Fatalf("hit_count %d, ingin 2", n)
	}
	f.AssertInvariants()
}

// Reservasi yang dibutuhkan sedang dikunci transaksi lain (misal pembatalan
// yang menunggu saldo): penjualan fakta melepas saldo, menunggu, lalu mencoba
// lagi, tanpa deadlock dan tanpa mencatat selisih stok palsu.
func TestRecordFactSale_WaitsForLockedReservation(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.StoreA, f.ProductA, 1)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1))

	holder := f.DB.Begin()
	if err := holder.Exec(`SELECT id FROM stock_reservations WHERE order_id = ? FOR NO KEY UPDATE`, o.ID).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan service.FactSaleResult, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := factSale(f, f.StoreA, f.ProductA, 1, "pos:locked")
		errs <- err
		done <- res
	}()
	time.Sleep(40 * time.Millisecond) // beberapa percobaan ulang terjadi selama lock dipegang
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.DiscrepancyQty != 0 || len(res.Preempted) != 1 || res.Preempted[0].OrderID != o.ID {
		t.Fatalf("ingin preempt order tanpa selisih, dapat %+v", res)
	}
	f.AssertInvariants()
}

// Pembatalan order dan penjualan fakta bersamaan di lokasi yang sama
// (urutan lock berlawanan: reservasi→saldo vs saldo→reservasi) tidak pernah
// deadlock dan tidak menghasilkan selisih palsu.
func TestRecordFactSale_ConcurrentWithCancel(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	retriesBefore := service.TxRetryCount()
	for i := range 20 {
		f.Receive(f.StoreA, f.ProductA, 1)
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		var wg sync.WaitGroup
		wg.Go(func() {
			if _, err := f.Orders.Cancel(context.Background(), ops, o.ID, "batal bersamaan"); err != nil {
				t.Errorf("cancel: %v", err)
			}
		})
		wg.Go(func() {
			if _, err := factSale(f, f.StoreA, f.ProductA, 1, fmt.Sprintf("pos:cc:%d", i)); err != nil {
				t.Errorf("fakta: %v", err)
			}
		})
		wg.Wait()
	}
	if n := f.Count(`SELECT count(*) FROM inventory_movements WHERE movement_type = 'ADJUST_DISCREPANCY'`); n != 0 {
		t.Fatalf("selisih palsu %d", n)
	}
	if service.TxRetryCount() != retriesBefore {
		t.Fatal("ada deadlock yang diulang")
	}
	if b := f.Balance(f.StoreA, f.ProductA); b.PhysicalQty != 0 || b.ReservedQty != 0 {
		t.Fatalf("saldo akhir %+v", b)
	}
	f.AssertInvariants()
}

func TestRecordFactSale_InvalidInput(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	for _, qty := range []int64{0, -1, service.MaxMovementQty + 1} {
		if _, err := factSale(f, f.StoreA, f.ProductA, qty, "pos:x"); err != service.ErrInvalidMovement {
			t.Fatalf("qty %d: %v", qty, err)
		}
	}
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'k'
	}
	if _, err := factSale(f, f.StoreA, f.ProductA, 1, string(long)); err != service.ErrInvalidMovement {
		t.Fatalf("key panjang: %v", err)
	}
	if _, err := factSale(f, uuid.New(), f.ProductA, 1, "pos:unknown-loc"); err != service.ErrStockLocationNotFound {
		t.Fatalf("lokasi tak dikenal: %v", err)
	}
}

// D2C + marketplace + penjualan fakta POS berebut stok toko yang sama secara
// bersamaan. Fakta POS selalu diterima; janji yang kalah di-preempt; total
// terjual + dijanjikan tidak pernah melebihi stok fisik.
func TestOrder_OversellAcrossChannels(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	const initial = 10
	f.Receive(f.StoreA, f.ProductA, initial)

	// Gelombang 1: 6 POS + 8 D2C + 8 marketplace berebut 10 unit.
	raceWave(t, f, "w1", 6, 8, 8)
	checkOversell(t, f, initial, 6)

	// Gelombang 2: sisa 4 unit kini (hampir pasti) sudah dijanjikan ke order
	// online; 4 penjualan POS bersamaan dengan order baru harus menang dengan
	// mem-preempt janji itu.
	activeBefore := f.Count(`SELECT COALESCE(sum(qty), 0) FROM stock_reservations WHERE status = 'ACTIVE'`)
	raceWave(t, f, "w2", 4, 4, 4)
	checkOversell(t, f, initial, 10)
	if preempted := f.Count(`SELECT count(*) FROM stock_reservations WHERE status = 'PREEMPTED'`); preempted < activeBefore {
		t.Fatalf("preempt %d, ingin >= %d janji yang aktif sebelum gelombang 2", preempted, activeBefore)
	}
	f.AssertInvariants()
}

func raceWave(t *testing.T, f *inventorytest.Fixture, prefix string, posSales, d2c, mp int) {
	t.Helper()
	var wg sync.WaitGroup
	var posOK, d2cOK, d2cOut, mpPaid, mpNeedsAction atomic.Int64
	start := make(chan struct{})
	for i := range posSales {
		wg.Go(func() {
			<-start
			if _, err := factSale(f, f.StoreA, f.ProductA, 1, fmt.Sprintf("pos:%s:%d", prefix, i)); err != nil {
				t.Errorf("penjualan POS ditolak: %v", err)
				return
			}
			posOK.Add(1)
		})
	}
	for range d2c {
		wg.Go(func() {
			<-start
			_, err := f.CreateOrder(inventorytest.Line(f.ProductA, 1))
			switch {
			case err == nil:
				d2cOK.Add(1)
			case appCode(err) == "out_of_stock":
				d2cOut.Add(1)
			default:
				t.Errorf("D2C: %v", err)
			}
		})
	}
	for i := range mp {
		wg.Go(func() {
			<-start
			rec := f.Signed(importPath, importBody(fmt.Sprintf("SHP-RACE-%s-%d", prefix, i), mpLine(inventorytest.SKUA, 1, 45000)))
			if rec.Code != 201 {
				t.Errorf("marketplace %d: %s", rec.Code, rec.Body)
				return
			}
			var r dto.MarketplaceImportResponse
			inventorytest.Decode(t, rec, &r)
			if r.Order.Status == "PAID" {
				mpPaid.Add(1)
			} else {
				mpNeedsAction.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	if posOK.Load() != int64(posSales) {
		t.Fatalf("penjualan POS diterima %d, ingin %d", posOK.Load(), posSales)
	}
	if d2cOK.Load()+d2cOut.Load() != int64(d2c) || mpPaid.Load()+mpNeedsAction.Load() != int64(mp) {
		t.Fatal("ada request yang hilang")
	}
	t.Logf("%s: POS %d, D2C ok %d/out %d, marketplace PAID %d/NEEDS_ACTION %d",
		prefix, posOK.Load(), d2cOK.Load(), d2cOut.Load(), mpPaid.Load(), mpNeedsAction.Load())
}

// checkOversell: terjual + dijanjikan tidak pernah melebihi stok fisik awal,
// tidak ada selisih buatan, dan setiap preempt punya konflik + event.
func checkOversell(t *testing.T, f *inventorytest.Fixture, initial, posSold int64) {
	t.Helper()
	b := f.Balance(f.StoreA, f.ProductA)
	sold := -f.Count(`SELECT COALESCE(sum(physical_delta), 0) FROM inventory_movements WHERE movement_type IN ('DIRECT_SALE', 'COMMIT_SALE')`)
	adjusted := f.Count(`SELECT COALESCE(sum(physical_delta), 0) FROM inventory_movements WHERE movement_type = 'ADJUST_DISCREPANCY'`)
	active := f.Count(`SELECT COALESCE(sum(qty), 0) FROM stock_reservations WHERE status = 'ACTIVE'`)
	preempted := f.Count(`SELECT count(*) FROM stock_reservations WHERE status = 'PREEMPTED'`)
	if adjusted != 0 {
		t.Fatalf("ada ADJUST_DISCREPANCY %d padahal stok fisik cukup untuk semua fakta", adjusted)
	}
	if sold != posSold || sold+active > initial || b.PhysicalQty != initial-posSold || b.ReservedQty != active {
		t.Fatalf("terjual %d + dijanjikan %d > fisik %d (saldo %+v)", sold, active, initial, b)
	}
	if active != initial-posSold {
		t.Fatalf("stok sisa %d harus habis dijanjikan (aktif %d)", initial-posSold, active)
	}
	conflicts := f.Count(`SELECT count(*) FROM allocation_conflicts WHERE kind = 'OFFLINE_SALE_PREEMPT'`)
	events := f.Count(`SELECT count(*) FROM outbox_events WHERE event_type = 'reservation.preempted.v1'`)
	if conflicts != preempted || events != preempted {
		t.Fatalf("preempt %d, konflik %d, event %d harus sama", preempted, conflicts, events)
	}
	f.AssertInvariants()
}
