package worker_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/inventorytest"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
	"mganik/api/internal/worker"
	"mganik/api/testutil"
)

// H1 (probe QA): dua sweeper melepas ratusan order kedaluwarsa sementara 12
// pembuat order multi-line [A,B]/[B,A] berjalan serentak di lokasi yang sama.
// Sweeper kini memproses SATU order per transaksi dengan urutan lock order →
// reservasi → saldo, jadi tidak ada error yang sampai ke klien dan tidak ada
// deadlock.
func TestSweeper_ConcurrentWithMultiLineCreates(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 100000)
	f.Receive(f.Central, f.ProductB, 100000)
	const due = 120
	for i := range due {
		if i%2 == 0 {
			f.MustOrder(inventorytest.Line(f.ProductA, 1), inventorytest.Line(f.ProductB, 1))
		} else {
			f.MustOrder(inventorytest.Line(f.ProductB, 2), inventorytest.Line(f.ProductA, 1))
		}
	}
	f.Clock.Advance(f.TTL + time.Second)
	retriesBefore := service.TxRetryCount()

	ctx := context.Background()
	var wg sync.WaitGroup
	var swept atomic.Int64
	var createErrs, sweepErrs atomic.Int64
	var created atomic.Int64
	stop := make(chan struct{})
	for range 2 {
		wg.Go(func() {
			s := worker.NewSweeper(f.Orders, 25)
			for {
				n, err := s.RunOnce(ctx)
				if err != nil {
					sweepErrs.Add(1)
					t.Errorf("sweeper: %v", err)
					return
				}
				swept.Add(int64(n))
				if n == 0 {
					return
				}
			}
		})
	}
	for w := range 12 {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				lines := []service.CreateOrderLine{inventorytest.Line(f.ProductA, 1), inventorytest.Line(f.ProductB, 1)}
				if (w+i)%2 == 1 {
					lines[0], lines[1] = lines[1], lines[0]
				}
				if _, err := f.CreateOrder(lines...); err != nil {
					createErrs.Add(1)
					t.Errorf("create: %v", err)
					return
				}
				created.Add(1)
			}
		})
	}
	// Pembuat order berjalan selama sweeper bekerja, lalu dihentikan.
	deadline := time.Now().Add(10 * time.Second)
	for f.Count(`SELECT count(*) FROM orders WHERE status = 'PENDING_PAYMENT' AND payment_expires_at <= ?`, f.Clock.Now()) > 0 &&
		time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	retries := service.TxRetryCount() - retriesBefore
	t.Logf("disapu %d, order baru %d, transaksi diulang %d", swept.Load(), created.Load(), retries)
	if createErrs.Load() != 0 || sweepErrs.Load() != 0 {
		t.Fatalf("error ke klien: create %d sweeper %d", createErrs.Load(), sweepErrs.Load())
	}
	if swept.Load() != due {
		t.Fatalf("disapu %d, ingin %d", swept.Load(), due)
	}
	if retries != 0 {
		t.Fatalf("%d transaksi diulang karena deadlock/serialization", retries)
	}
	if created.Load() == 0 {
		t.Fatal("pembuat order tidak berjalan bersamaan dengan sweeper")
	}
	f.AssertInvariants()
}

// M5: setelah MaxAttempts kegagalan event ditandai mati (dead_at), tidak
// diambil lagi, dan dihitung.
func TestDispatcher_DeadLetter(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	ctx := context.Background()
	d := worker.NewDispatcher(f.DB, f.Clock, f.Log)
	d.MaxAttempts = 3
	d.Register("test.always_fail.v1", func(context.Context, *gorm.DB, model.OutboxEvent) error {
		return errors.New("selalu gagal")
	})
	id := uuid.Must(uuid.NewV7())
	if err := f.DB.Create(&model.OutboxEvent{ID: id, EventType: "test.always_fail.v1", AggregateType: "TEST",
		AggregateID: uuid.New(), Payload: model.JSON(`{}`), NextAttemptAt: f.Clock.Now(), CreatedAt: f.Clock.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if ok, err := d.ProcessNext(ctx); err != nil || !ok {
			t.Fatalf("percobaan %d: %v %v", i+1, ok, err)
		}
		f.Clock.Advance(time.Hour)
	}
	var ev model.OutboxEvent
	if err := f.DB.Take(&ev, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if ev.DeadAt == nil || ev.Attempts != 3 || ev.LastError == nil || *ev.LastError != "selalu gagal" || ev.PublishedAt != nil {
		t.Fatalf("event %+v", ev)
	}
	if ok, err := d.ProcessNext(ctx); err != nil || ok {
		t.Fatal("event mati tidak boleh diambil lagi")
	}
	if d.DeadLettered() != 1 {
		t.Fatalf("DeadLettered %d", d.DeadLettered())
	}
	if n, err := d.CountDead(ctx); err != nil || n != 1 {
		t.Fatalf("CountDead %d %v", n, err)
	}
}

// Runner bisa menjalankan job berkala selain handler outbox (misal pelepasan
// klaim loyalty, pembersihan sesi/OTP).
func TestRunner_RegisterJob(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	r := newRunner(f)
	var calls atomic.Int64
	r.RegisterJob("hitung", func(context.Context) error { calls.Add(1); return nil })
	r.RegisterJob("gagal", func(context.Context) error { return errors.New("job gagal") })
	r.Tick(context.Background())
	r.Tick(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("job dipanggil %d kali, ingin 2 (job gagal tidak menghentikan putaran)", calls.Load())
	}
}
