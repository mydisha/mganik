package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
)

const (
	// SweepBatch: jumlah order kedaluwarsa per transaksi sweeper.
	SweepBatch = 100
	// DispatchBatch: maksimum event outbox per putaran.
	DispatchBatch = 200
)

// Sweeper melepas reservasi order yang lewat batas bayar.
type Sweeper struct {
	orders *service.OrderService
	batch  int
}

func NewSweeper(orders *service.OrderService, batch int) *Sweeper {
	if batch <= 0 {
		batch = SweepBatch
	}
	return &Sweeper{orders: orders, batch: batch}
}

// RunOnce memproses order kedaluwarsa sampai habis. Aman dijalankan paralel
// (order diklaim dengan SKIP LOCKED).
func (s *Sweeper) RunOnce(ctx context.Context) (int, error) {
	total := 0
	for {
		n, err := s.orders.ExpireDueOrders(ctx, s.batch)
		total += n
		if err != nil || n < s.batch {
			return total, err
		}
	}
}

// Deps adalah dependency Runner.
type Deps struct {
	DB     *gorm.DB
	Log    *slog.Logger
	Clock  clock.Clock
	Config config.InventoryConfig
}

// Runner menjalankan sweeper dan dispatcher secara berkala sampai Stop.
type Runner struct {
	Sweeper    *Sweeper
	Dispatcher *Dispatcher
	log        *slog.Logger
	interval   time.Duration

	mu     sync.Mutex
	jobs   []job
	cancel context.CancelFunc
	done   chan struct{}
}

// Job adalah pekerjaan berkala yang dijalankan setiap putaran worker (misal
// pelepasan klaim loyalty jatuh tempo, pembersihan sesi/OTP kedaluwarsa).
// Job wajib idempotent dan aman dijalankan beberapa instance (SKIP LOCKED).
type Job func(ctx context.Context) error

type job struct {
	name string
	fn   Job
}

// RegisterJob mendaftarkan job berkala. Aman dipanggil sebelum atau sesudah Start.
func (r *Runner) RegisterJob(name string, fn Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs = append(r.jobs, job{name: name, fn: fn})
}

func (r *Runner) snapshotJobs() []job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]job(nil), r.jobs...)
}

// New merangkai service domain stok/order dan mendaftarkan handler
// reservation.preempted.v1 (realokasi order setelah penjualan fakta POS).
func New(d Deps) *Runner {
	clk := d.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	inv := service.NewInventoryService(d.DB, clk)
	orders := service.NewOrderService(d.DB, clk, inv, service.NewIdempotencyService(d.DB, clk), d.Config.ReservationTTL)
	disp := NewDispatcher(d.DB, clk, d.Log)
	if d.Config.OutboxMaxAttempts > 0 {
		disp.MaxAttempts = d.Config.OutboxMaxAttempts
	}
	disp.Register(service.EventReservationPreempted, func(ctx context.Context, tx *gorm.DB, ev model.OutboxEvent) error {
		return orders.HandleReservationPreempted(ctx, tx, ev.Payload)
	})
	interval := d.Config.WorkerInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Runner{Sweeper: NewSweeper(orders, SweepBatch), Dispatcher: disp, log: d.Log, interval: interval}
}

// Tick menjalankan satu putaran: sweeper, job terdaftar, lalu dispatcher.
// Kegagalan satu langkah dicatat dan tidak menghentikan langkah lain.
func (r *Runner) Tick(ctx context.Context) {
	if n, err := r.Sweeper.RunOnce(ctx); err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "sweeper reservasi gagal", "error", err)
		}
	} else if n > 0 {
		r.log.InfoContext(ctx, "order kedaluwarsa dilepas", "jumlah", n)
	}
	for _, j := range r.snapshotJobs() {
		if err := j.fn(ctx); err != nil && ctx.Err() == nil {
			r.log.ErrorContext(ctx, "job worker gagal", "job", j.name, "error", err)
		}
	}
	if _, err := r.Dispatcher.RunOnce(ctx, DispatchBatch); err != nil && ctx.Err() == nil {
		r.log.ErrorContext(ctx, "dispatcher outbox gagal", "error", err)
	}
}

// Start menjalankan loop di goroutine sendiri. Berhenti saat ctx selesai atau Stop.
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done != nil {
		return
	}
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		r.log.Info("worker berjalan", "interval", r.interval)
		t := time.NewTicker(r.interval)
		defer t.Stop()
		for {
			r.Tick(ctx)
			select {
			case <-ctx.Done():
				r.log.Info("worker berhenti")
				return
			case <-t.C:
			}
		}
	}(r.done)
}

// Stop menghentikan loop dan menunggu putaran yang sedang berjalan selesai.
func (r *Runner) Stop() {
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}
