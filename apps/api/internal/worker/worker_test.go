package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/config"
	"mganik/api/internal/inventorytest"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
	"mganik/api/internal/worker"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func newRunner(f *inventorytest.Fixture) *worker.Runner {
	return worker.New(worker.Deps{DB: f.DB, Log: f.Log, Clock: f.Clock, Config: config.InventoryConfig{
		ReservationTTL: f.TTL, WorkerInterval: 10 * time.Millisecond,
	}})
}

// Dua sweeper paralel: setiap order kedaluwarsa diproses tepat sekali dan
// setiap reservasi dilepas tepat sekali (FOR NO KEY UPDATE SKIP LOCKED).
func TestSweeper_ReleasesExpiredOnceWithTwoInstances(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 100)
	f.Receive(f.Central, f.ProductB, 100)
	const orders = 30
	for range orders {
		f.MustOrder(inventorytest.Line(f.ProductA, 2), inventorytest.Line(f.ProductB, 1))
	}
	// Order yang belum lewat TTL tidak boleh tersentuh.
	f.Clock.Advance(f.TTL - time.Minute)
	fresh := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	paid := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	f.Pay(paid)
	f.Clock.Advance(2 * time.Minute)

	a := worker.NewSweeper(f.Orders, 4)
	b := worker.NewSweeper(f.Orders, 4)
	var na, nb int
	var wg sync.WaitGroup
	wg.Go(func() {
		var err error
		if na, err = a.RunOnce(context.Background()); err != nil {
			t.Error(err)
		}
	})
	wg.Go(func() {
		var err error
		if nb, err = b.RunOnce(context.Background()); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	if na+nb != orders {
		t.Fatalf("diproses %d + %d, ingin total %d", na, nb, orders)
	}
	if n := f.Count(`SELECT count(*) FROM orders WHERE status = 'EXPIRED'`); n != orders {
		t.Fatalf("EXPIRED %d", n)
	}
	if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE status = 'EXPIRED'`); n != orders*2 {
		t.Fatalf("reservasi EXPIRED %d", n)
	}
	// Tepat satu RELEASE per reservasi kedaluwarsa.
	if n := f.Count(`
		SELECT count(*) FROM stock_reservations r
		 WHERE r.status = 'EXPIRED'
		   AND (SELECT count(*) FROM inventory_movements m
		         WHERE m.idempotency_key = 'reservation:' || r.id || ':release') <> 1`); n != 0 {
		t.Fatalf("%d reservasi tidak dilepas tepat sekali", n)
	}
	if n := f.Count(`SELECT count(*) FROM order_status_history WHERE to_status = 'EXPIRED'`); n != orders {
		t.Fatalf("riwayat EXPIRED %d", n)
	}
	if f.OrderStatus(fresh.ID) != "PENDING_PAYMENT" || f.OrderStatus(paid.ID) != "PAID" {
		t.Fatal("order yang belum lewat TTL atau sudah dibayar tidak boleh kedaluwarsa")
	}
	if bal := f.Balance(f.Central, f.ProductA); bal.ReservedQty != 2 {
		t.Fatalf("reserved A %d, ingin 2 (fresh + paid)", bal.ReservedQty)
	}
	// Putaran berikutnya tidak menemukan apa pun.
	if n, err := a.RunOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("putaran ulang %d %v", n, err)
	}
	f.AssertInvariants()
}

func TestDispatcher_RetryBackoffAndExactlyOnce(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	ctx := context.Background()
	d := worker.NewDispatcher(f.DB, f.Clock, f.Log)
	insert := func(eventType string) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		if err := f.DB.Create(&model.OutboxEvent{ID: id, EventType: eventType, AggregateType: "TEST", AggregateID: uuid.New(),
			Payload: model.JSON(`{}`), NextAttemptAt: f.Clock.Now(), CreatedAt: f.Clock.Now()}).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}

	t.Run("handler gagal: efek dibatalkan, attempts naik, backoff eksponensial", func(t *testing.T) {
		calls := 0
		d.Register("test.failing.v1", func(ctx context.Context, tx *gorm.DB, ev model.OutboxEvent) error {
			calls++
			if err := tx.Exec(`INSERT INTO audit_logs (id, actor_type, action, entity) VALUES (?, 'SYSTEM', 'efek.handler', 'x')`, uuid.New()).Error; err != nil {
				return err
			}
			return errors.New("downstream mati ✗")
		})
		id := insert("test.failing.v1")
		var ev model.OutboxEvent
		for i, wantDelay := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
			if ok, err := d.ProcessNext(ctx); err != nil || !ok {
				t.Fatalf("percobaan %d: %v %v", i+1, ok, err)
			}
			f.DB.Take(&ev, "id = ?", id)
			if ev.Attempts != i+1 || ev.LastError == nil || *ev.LastError != "downstream mati ✗" || ev.PublishedAt != nil ||
				!ev.NextAttemptAt.Equal(f.Clock.Now().Add(wantDelay)) {
				t.Fatalf("percobaan %d: attempts %d next %v", i+1, ev.Attempts, ev.NextAttemptAt)
			}
			// Belum waktunya: tidak diambil.
			if ok, _ := d.ProcessNext(ctx); ok {
				t.Fatal("event diambil sebelum next_attempt_at")
			}
			f.Clock.Advance(wantDelay)
		}
		if n := f.Count(`SELECT count(*) FROM audit_logs WHERE action = 'efek.handler'`); n != 0 {
			t.Fatal("efek handler yang gagal harus dibatalkan")
		}
		d.Register("test.failing.v1", func(context.Context, *gorm.DB, model.OutboxEvent) error { return nil })
		if ok, err := d.ProcessNext(ctx); err != nil || !ok {
			t.Fatal(err)
		}
		f.DB.Take(&ev, "id = ?", id)
		if ev.PublishedAt == nil || ev.LastError != nil {
			t.Fatal("event harus terbit setelah handler pulih")
		}
		if d.Backoff(30) != worker.DefaultMaxBackoff {
			t.Fatal("backoff harus dibatasi")
		}
	})

	t.Run("event tanpa handler dianggap terbit", func(t *testing.T) {
		id := insert("test.nobody.v1")
		if _, err := d.RunOnce(ctx, 10); err != nil {
			t.Fatal(err)
		}
		if n := f.Count(`SELECT count(*) FROM outbox_events WHERE id = ? AND published_at IS NOT NULL`, id); n != 1 {
			t.Fatal("belum terbit")
		}
	})

	t.Run("dua dispatcher paralel memproses tiap event tepat sekali", func(t *testing.T) {
		var mu sync.Mutex
		seen := map[uuid.UUID]int{}
		h := func(ctx context.Context, tx *gorm.DB, ev model.OutboxEvent) error {
			mu.Lock()
			seen[ev.ID]++
			mu.Unlock()
			return nil
		}
		d2 := worker.NewDispatcher(f.DB, f.Clock, f.Log)
		d.Register("test.count.v1", h)
		d2.Register("test.count.v1", h)
		for range 60 {
			insert("test.count.v1")
		}
		var wg sync.WaitGroup
		var total atomic.Int64
		for _, disp := range []*worker.Dispatcher{d, d2} {
			wg.Go(func() {
				n, err := disp.RunOnce(ctx, 1000)
				if err != nil {
					t.Error(err)
				}
				total.Add(int64(n))
			})
		}
		wg.Wait()
		if len(seen) != 60 || total.Load() != 60 {
			t.Fatalf("event unik %d, diproses %d", len(seen), total.Load())
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("event %s diproses %d kali", id, n)
			}
		}
	})
}

func preempt(t *testing.T, f *inventorytest.Fixture, loc, prod uuid.UUID, qty int64) service.FactSaleResult {
	t.Helper()
	var res service.FactSaleResult
	if err := f.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		res, err = f.Inv.RecordFactSale(context.Background(), tx, service.FactSaleInput{
			LocationID: loc, ProductID: prod, Qty: qty, RefType: "POS_TRANSACTION", RefID: uuid.New(),
			IdempotencyKey: "pos:" + uuid.NewString(), Actor: service.SystemLedgerActor,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return res
}

func orderMeta(t *testing.T, f *inventorytest.Fixture, id uuid.UUID) map[string]any {
	t.Helper()
	var raw []byte
	if err := f.DB.Raw(`SELECT metadata FROM orders WHERE id = ?`, id).Row().Scan(&raw); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

// Kasus 4 end-to-end di level worker: penjualan POS mem-preempt reservasi,
// handler reservation.preempted.v1 merealokasi ke lokasi lain atau
// menjadikan order NEEDS_ACTION / CANCELLED.
func TestReallocationHandler(t *testing.T) {
	ctx := context.Background()

	t.Run("berhasil: order PAID direalokasi ke hub, tetap PAID", func(t *testing.T) {
		f := inventorytest.New(t, testutil.NewDB(t))
		r := newRunner(f)
		f.Receive(f.StoreA, f.ProductA, 1)
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		f.Pay(o)
		f.Receive(f.Hub, f.ProductA, 5) // stok datang di hub setelah order dibuat
		res := preempt(t, f, f.StoreA, f.ProductA, 1)
		if len(res.Preempted) != 1 {
			t.Fatal("reservasi harus di-preempt")
		}
		if _, err := r.Dispatcher.RunOnce(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if f.OrderStatus(o.ID) != "PAID" {
			t.Fatalf("status %s, ingin tetap PAID", f.OrderStatus(o.ID))
		}
		if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE' AND location_id = ? AND expires_at IS NULL`, o.ID, f.Hub); n != 1 {
			t.Fatal("reservasi baru di hub tidak ada")
		}
		if n := f.Count(`SELECT count(*) FROM allocation_conflicts WHERE order_id = ? AND status = 'AUTO_RESOLVED'`, o.ID); n != 1 {
			t.Fatal("konflik harus AUTO_RESOLVED")
		}
		if orderMeta(t, f, o.ID)["split_fulfillment"] != true {
			t.Fatal("metadata split_fulfillment tidak ada")
		}
		if n := f.Count(`SELECT count(*) FROM outbox_events WHERE event_type = 'order.reallocated.v1' AND aggregate_id = ?`, o.ID); n != 1 {
			t.Fatal("event reallocated tidak ada")
		}
		// Event yang sama diproses lagi (redelivery): tidak ada efek ganda.
		if err := f.DB.Exec(`UPDATE outbox_events SET published_at = NULL WHERE event_type = 'reservation.preempted.v1'`).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := r.Dispatcher.RunOnce(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE'`, o.ID); n != 1 {
			t.Fatalf("redelivery membuat reservasi ganda: %d", n)
		}
		f.AssertInvariants()
	})

	t.Run("gagal: order PAID → NEEDS_ACTION, reservasi lain dilepas", func(t *testing.T) {
		f := inventorytest.New(t, testutil.NewDB(t))
		r := newRunner(f)
		f.Receive(f.StoreA, f.ProductA, 1)
		f.Receive(f.StoreA, f.ProductB, 1)
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1), inventorytest.Line(f.ProductB, 1))
		f.Pay(o)
		preempt(t, f, f.StoreA, f.ProductA, 1)
		if _, err := r.Dispatcher.RunOnce(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if f.OrderStatus(o.ID) != "NEEDS_ACTION" {
			t.Fatalf("status %s", f.OrderStatus(o.ID))
		}
		if n := f.Count(`SELECT count(*) FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE'`, o.ID); n != 0 {
			t.Fatal("NEEDS_ACTION tidak memegang stok")
		}
		if f.Balance(f.StoreA, f.ProductB).ReservedQty != 0 {
			t.Fatal("reservasi line B harus dilepas")
		}
		if n := f.Count(`SELECT count(*) FROM allocation_conflicts WHERE order_id = ? AND status = 'OPEN'`, o.ID); n != 1 {
			t.Fatal("konflik tetap OPEN untuk ops")
		}
		if orderMeta(t, f, o.ID)["needs_action_reason"] != "preempted_reallocation_failed" {
			t.Fatal("alasan NEEDS_ACTION tidak dicatat")
		}
		f.AssertInvariants()
	})

	t.Run("gagal: order PENDING_PAYMENT → EXPIRED, konflik OPEN, bayar telat tetap dicoba", func(t *testing.T) {
		f := inventorytest.New(t, testutil.NewDB(t))
		r := newRunner(f)
		f.Receive(f.StoreA, f.ProductA, 1)
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		preempt(t, f, f.StoreA, f.ProductA, 1)
		r.Tick(ctx)
		if f.OrderStatus(o.ID) != "EXPIRED" {
			t.Fatalf("status %s, ingin EXPIRED", f.OrderStatus(o.ID))
		}
		if n := f.Count(`SELECT count(*) FROM allocation_conflicts WHERE order_id = ? AND status = 'OPEN'`, o.ID); n != 1 {
			t.Fatal("konflik harus tetap OPEN")
		}
		if n := f.Count(`SELECT count(*) FROM order_status_history WHERE order_id = ? AND from_status = 'PENDING_PAYMENT' AND to_status = 'EXPIRED'`, o.ID); n != 1 {
			t.Fatal("riwayat PENDING_PAYMENT → EXPIRED tidak ada")
		}
		// Pelanggan tetap membayar setelah stok datang di gudang: bayar telat → re-reserve → PAID.
		f.Receive(f.Central, f.ProductA, 2)
		if res := f.Pay(o); res.Outcome != "LATE_PAYMENT_REALLOCATED" || res.OrderStatus != "PAID" {
			t.Fatalf("bayar telat: %+v", res)
		}
		f.AssertInvariants()
	})

	t.Run("berhasil: order PENDING_PAYMENT tetap menunggu bayar dengan TTL semula", func(t *testing.T) {
		f := inventorytest.New(t, testutil.NewDB(t))
		r := newRunner(f)
		f.Receive(f.StoreA, f.ProductA, 1)
		o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
		f.Receive(f.Central, f.ProductA, 3)
		preempt(t, f, f.StoreA, f.ProductA, 1)
		r.Tick(ctx)
		if f.OrderStatus(o.ID) != "PENDING_PAYMENT" {
			t.Fatalf("status %s", f.OrderStatus(o.ID))
		}
		var exp time.Time
		if err := f.DB.Raw(`SELECT expires_at FROM stock_reservations WHERE order_id = ? AND status = 'ACTIVE'`, o.ID).Row().Scan(&exp); err != nil {
			t.Fatal(err)
		}
		if !exp.Equal(*o.PaymentExpiresAt) {
			t.Fatalf("expires_at %v, ingin %v", exp, *o.PaymentExpiresAt)
		}
		f.AssertInvariants()
	})
}

func TestRunner_StartStop(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)
	o := f.MustOrder(inventorytest.Line(f.ProductA, 1))
	f.Clock.Advance(f.TTL + time.Second)
	r := worker.New(worker.Deps{DB: f.DB, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: f.Clock,
		Config: config.InventoryConfig{ReservationTTL: f.TTL, WorkerInterval: 5 * time.Millisecond}})
	r.Start(context.Background())
	// Sweeper dan dispatcher berjalan berurutan dalam satu putaran: tunggu
	// sampai order kedaluwarsa DAN event-nya (order.expired.v1) sudah diproses.
	deadline := time.Now().Add(5 * time.Second)
	for f.OrderStatus(o.ID) != "EXPIRED" || f.Count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("worker tidak menyapu order/event: status %s", f.OrderStatus(o.ID))
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Stop()
	r.Stop() // aman dipanggil dua kali
}
