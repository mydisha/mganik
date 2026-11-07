package postest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
	"mganik/api/internal/service"
	"mganik/api/internal/worker"
)

// kasus4 menyiapkan skenario PLANNING §7.4 persis dari data seed:
// stok Toko Jakarta Selatan SKU1 = 1 dan toko ONLINE (heartbeat), order D2C
// online me-reserve unit terakhir itu dengan fulfillment di toko (gudang
// pusat, hub, dan toko Bandung sedang tidak melayani online), lalu heartbeat
// berhenti dan job worker menandai toko OFFLINE.
type kasus4 struct {
	e       *env
	orders  *service.OrderService
	payment *service.PaymentService
	orderID uuid.UUID
}

func setupKasus4(t *testing.T) *kasus4 {
	e := newEnv(t)
	ctx := context.Background()
	inv := service.NewInventoryService(e.db, e.clk)
	orders := service.NewOrderService(e.db, e.clk, inv, service.NewIdempotencyService(e.db, e.clk), 15*time.Minute)
	k := &kasus4{e: e, orders: orders, payment: service.NewPaymentService(orders)}

	if got := e.balance(jakselID, sku1ID); got.Physical != 1 || got.Available != 1 {
		t.Fatalf("seed Kasus 4: stok Jaksel SKU1 %+v, ingin 1", got)
	}
	// Hanya toko Jakarta Selatan yang melayani online saat order dibuat.
	e.exec(`UPDATE locations SET online_fulfillment_enabled = false WHERE id IN (?, ?, ?)`, centralID, hubID, bandungID)
	e.heartbeat()
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, jakselID); s != domain.SyncStateOnline {
		t.Fatalf("toko harus ONLINE setelah heartbeat, dapat %s", s)
	}

	ops := staff(opsID, true, nil, "order.create")
	res, err := orders.CreateIdempotent(ctx, ops, "kasus4-order-1", service.CreateOrderInput{
		Lines: []service.CreateOrderLine{{ProductID: sku1ID, Qty: 1}},
	}, func(v service.OrderView) any { return map[string]any{"id": v.Order.ID} })
	if err != nil {
		t.Fatal(err)
	}
	var created struct{ ID uuid.UUID }
	e.must(json.Unmarshal(res.Body, &created))
	k.orderID = created.ID
	if n := e.count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND location_id = ? AND status = 'ACTIVE'`,
		k.orderID, jakselID); n != 1 {
		t.Fatal("order online harus me-reserve unit di toko Jakarta Selatan")
	}
	if got := e.balance(jakselID, sku1ID); got.Reserved != 1 || got.Available != 0 {
		t.Fatalf("setelah reserve: %+v", got)
	}

	// Internet toko putus: heartbeat berhenti, job menandai OFFLINE.
	e.clk.Advance(e.pos.Config().HeartbeatTimeout + time.Second)
	// 2 = toko Jaksel (OFFLINE) + toko Bandung seed tanpa device (NOT_TRACKED).
	n, err := e.pos.MarkStaleLocationsOffline(ctx)
	if err != nil || n != 2 {
		t.Fatalf("job OFFLINE: n=%d err=%v", n, err)
	}
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, jakselID); s != domain.SyncStateOffline {
		t.Fatalf("toko harus OFFLINE, dapat %s", s)
	}
	// ATP online toko = 0 selama OFFLINE: order online berikutnya tidak bisa
	// dijanjikan dari toko itu (walau ada stok SKU2 di sana).
	if domain.OnlineATP(12, 0, domain.SyncStateOffline) != 0 {
		t.Fatal("ATP online lokasi OFFLINE harus 0")
	}
	_, err = orders.CreateIdempotent(ctx, ops, "kasus4-order-2", service.CreateOrderInput{
		Lines: []service.CreateOrderLine{{ProductID: sku2ID, Qty: 1}},
	}, func(v service.OrderView) any { return nil })
	if !errors.Is(err, service.ErrOutOfStock) {
		t.Fatalf("order saat toko OFFLINE harus out_of_stock, dapat %v", err)
	}
	return k
}

// sellOffline: kasir menjual unit terakhir saat offline. Internet kembali:
// heartbeat pertama masih membawa pending_count 1 (toko tetap OFFLINE), klien
// mengirim outbox (sync), lalu heartbeat dengan outbox kosong → ONLINE.
func (k *kasus4) sellOffline(t *testing.T) syncResult {
	e := k.e
	sale := e.newTxn(1, line(sku1ID, 1, priceSKU1)) // occurred_at = saat offline
	e.clk.Advance(10 * time.Minute)
	if hb := e.heartbeatPending(1); hb.LocationSyncState != "OFFLINE" || hb.OnlineBlockedReason == nil ||
		*hb.OnlineBlockedReason != "pending_outbox" {
		t.Fatalf("heartbeat dengan outbox belum habis tidak boleh ONLINE: %+v", hb)
	}
	out := e.sync(signed(t, e.key, sale))
	if len(out.Results) != 1 || out.AckUpToSeq != 1 {
		t.Fatalf("sync: %+v", out)
	}
	if hb := e.heartbeatPending(0); hb.LocationSyncState != "ONLINE" || hb.OnlineBlockedReason != nil {
		t.Fatalf("heartbeat setelah sync: %+v", hb)
	}
	return out.Results[0]
}

func (k *kasus4) assertPreempted(t *testing.T, r syncResult) uuid.UUID {
	e := k.e
	if r.Status != service.PosStatusAcceptedWithConflict {
		t.Fatalf("penjualan offline harus ACCEPTED_WITH_CONFLICT, dapat %s %s", r.Status, r.Detail)
	}
	d := r.detail(t)
	if len(d.Conflicts) != 1 || d.Conflicts[0].Type != service.PosConflictPreempt || d.Conflicts[0].OrderID == nil ||
		*d.Conflicts[0].OrderID != k.orderID || d.Conflicts[0].ConflictID == nil {
		t.Fatalf("konflik: %+v", d.Conflicts)
	}
	if n := e.count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND location_id = ? AND status = 'PREEMPTED'`,
		k.orderID, jakselID); n != 1 {
		t.Fatal("reservasi online harus PREEMPTED")
	}
	if n := e.count(`SELECT count(*) FROM allocation_conflicts WHERE id = ? AND kind = 'OFFLINE_SALE_PREEMPT'
	                  AND pos_transaction_id = ? AND order_id = ?`, *d.Conflicts[0].ConflictID, d.ServerTxnID, k.orderID); n != 1 {
		t.Fatal("allocation_conflicts tidak tercatat dengan benar")
	}
	if got := e.balance(jakselID, sku1ID); got.Physical != 0 || got.Reserved != 0 {
		t.Fatalf("stok toko setelah fakta: %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM outbox_events WHERE event_type = 'reservation.preempted.v1' AND aggregate_id = ?`,
		k.orderID); n != 1 {
		t.Fatal("event reservation.preempted.v1 tidak ditulis")
	}
	// Penjualan dicatat sebagai order POS COMPLETED.
	if n := e.count(`SELECT count(*) FROM orders WHERE id = ? AND channel_code = 'POS' AND status = 'COMPLETED'`, *d.OrderID); n != 1 {
		t.Fatal("order POS tidak termaterialisasi")
	}
	return *d.Conflicts[0].ConflictID
}

// TestKasus4_OfflineSaleBeatsOnlineReservation: penjualan offline menang
// (fakta), reservasi online di-preempt, konflik tercatat, worker
// merealokasi order ke gudang pusat; pelanggan online tidak terdampak.
func TestKasus4_OfflineSaleBeatsOnlineReservation(t *testing.T) {
	k := setupKasus4(t)
	e := k.e
	// Gudang pusat kembali melayani online sebelum device tersinkron.
	e.exec(`UPDATE locations SET online_fulfillment_enabled = true WHERE id = ?`, centralID)
	conflictID := k.assertPreempted(t, k.sellOffline(t))

	w := worker.New(worker.Deps{DB: e.db, Log: quietLog, Clock: e.clk})
	if _, err := w.Dispatcher.RunOnce(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND location_id = ? AND status = 'ACTIVE'`,
		k.orderID, centralID); n != 1 {
		t.Fatal("order harus direalokasi ke gudang pusat")
	}
	if s := e.str(`SELECT status FROM orders WHERE id = ?`, k.orderID); s != "PENDING_PAYMENT" {
		t.Fatalf("order online tetap berjalan, dapat %s", s)
	}
	if s := e.str(`SELECT status FROM allocation_conflicts WHERE id = ?`, conflictID); s != "AUTO_RESOLVED" {
		t.Fatalf("konflik harus AUTO_RESOLVED setelah realokasi, dapat %s", s)
	}
	if got := e.balance(centralID, sku1ID); got.Reserved != 1 {
		t.Fatalf("reservasi gudang pusat: %+v", got)
	}
	e.assertLedgerMatchesBalances()
}

// TestKasus4_NoStockAnywhere_NeedsAction: order sudah dibayar dan tidak ada
// stok di lokasi lain mana pun → NEEDS_ACTION (refund/kompensasi), konflik
// tetap OPEN untuk ops.
func TestKasus4_NoStockAnywhere_NeedsAction(t *testing.T) {
	k := setupKasus4(t)
	e := k.e
	method := "QRIS"
	if _, err := k.payment.HandleMockWebhook(context.Background(), service.PaymentWebhookInput{
		ProviderRef: "PAY-KASUS4", OrderID: k.orderID, Amount: 46000, Currency: "IDR", Status: "SUCCEEDED", Method: &method,
	}); err != nil {
		t.Fatal(err)
	}
	if s := e.str(`SELECT status FROM orders WHERE id = ?`, k.orderID); s != "PAID" {
		t.Fatalf("order harus PAID, dapat %s", s)
	}
	conflictID := k.assertPreempted(t, k.sellOffline(t))

	w := worker.New(worker.Deps{DB: e.db, Log: quietLog, Clock: e.clk})
	if _, err := w.Dispatcher.RunOnce(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if s := e.str(`SELECT status FROM orders WHERE id = ?`, k.orderID); s != "NEEDS_ACTION" {
		t.Fatalf("tanpa stok di mana pun order PAID harus NEEDS_ACTION, dapat %s", s)
	}
	if s := e.str(`SELECT status FROM allocation_conflicts WHERE id = ?`, conflictID); s != "OPEN" {
		t.Fatalf("konflik harus tetap OPEN untuk ops, dapat %s", s)
	}
	if n := e.count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE'`, k.orderID); n != 0 {
		t.Fatal("order NEEDS_ACTION tidak boleh memegang reservasi")
	}
	e.assertLedgerMatchesBalances()
}
