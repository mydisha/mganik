// Package worker menjalankan pekerjaan latar: sweeper reservasi kedaluwarsa
// dan dispatcher outbox. Semua klaim pekerjaan memakai SKIP LOCKED, jadi aman
// dijalankan lebih dari satu instance.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"mganik/api/internal/clock"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// Handler memproses satu event outbox di dalam tx yang sama dengan penanda
// published_at: efek handler dan status event commit bersama (efek di DB
// terjadi tepat sekali). Error = event dijadwalkan ulang dengan backoff.
type Handler func(ctx context.Context, tx *gorm.DB, ev model.OutboxEvent) error

const (
	DefaultBaseBackoff = 2 * time.Second
	DefaultMaxBackoff  = 10 * time.Minute
	DefaultMaxAttempts = 10
	maxLastErrorLen    = 1000
)

// Dispatcher mengambil event outbox satu per satu (FOR UPDATE SKIP LOCKED)
// dan menjalankan handler sesuai event_type. Event tanpa handler dianggap
// terbit (di produksi diteruskan ke broker).
type Dispatcher struct {
	db          *gorm.DB
	clock       clock.Clock
	log         *slog.Logger
	repo        repository.OutboxRepository
	mu          sync.RWMutex
	handlers    map[string]Handler
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// MaxAttempts: setelah gagal sebanyak ini event ditandai mati (dead-letter).
	MaxAttempts int
	// deadLettered menghitung event yang dimatikan proses ini (observabilitas).
	deadLettered atomic.Int64
}

func NewDispatcher(db *gorm.DB, clk clock.Clock, log *slog.Logger) *Dispatcher {
	if clk == nil {
		clk = clock.Real{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		db: db, clock: clk, log: log, handlers: map[string]Handler{},
		BaseBackoff: DefaultBaseBackoff, MaxBackoff: DefaultMaxBackoff, MaxAttempts: DefaultMaxAttempts,
	}
}

// DeadLettered: jumlah event yang dimatikan dispatcher ini sejak start.
func (d *Dispatcher) DeadLettered() int64 { return d.deadLettered.Load() }

// CountDead: jumlah seluruh event dead-letter di database.
func (d *Dispatcher) CountDead(ctx context.Context) (int64, error) {
	return d.repo.CountDead(d.db.WithContext(ctx))
}

// Register memasang handler untuk satu event_type (menimpa yang lama).
func (d *Dispatcher) Register(eventType string, h Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[eventType] = h
}

func (d *Dispatcher) handler(eventType string) Handler {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.handlers[eventType]
}

// Backoff mengembalikan jeda sebelum percobaan berikutnya setelah attempts
// kegagalan sebelumnya: base × 2^attempts, dibatasi MaxBackoff.
func (d *Dispatcher) Backoff(attempts int) time.Duration {
	b := d.BaseBackoff
	for i := 0; i < attempts && b < d.MaxBackoff; i++ {
		b *= 2
	}
	return min(b, d.MaxBackoff)
}

// ProcessNext memproses satu event. processed=false kalau antrean kosong.
func (d *Dispatcher) ProcessNext(ctx context.Context) (bool, error) {
	processed := false
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		processed = false
		now := d.clock.Now()
		ev, err := d.repo.ClaimNext(tx, now)
		if err != nil || ev == nil {
			return err
		}
		processed = true
		h := d.handler(ev.EventType)
		if h == nil {
			return d.repo.MarkPublished(tx, ev.ID, now)
		}
		if herr := runInSavepoint(tx, func() error { return h(ctx, tx, *ev) }); herr != nil {
			next := now.Add(d.Backoff(ev.Attempts))
			maxAttempts := d.MaxAttempts
			if maxAttempts <= 0 {
				maxAttempts = DefaultMaxAttempts
			}
			dead, err := d.repo.MarkFailed(tx, ev.ID, truncateUTF8(herr.Error(), maxLastErrorLen), next, now, maxAttempts)
			if err != nil {
				return err
			}
			if dead {
				d.deadLettered.Add(1)
				d.log.ErrorContext(ctx, "event outbox dimatikan (dead-letter) setelah batas percobaan",
					"event_type", ev.EventType, "event_id", ev.ID, "attempts", ev.Attempts+1, "error", herr)
				return nil
			}
			d.log.WarnContext(ctx, "handler outbox gagal, dijadwalkan ulang",
				"event_type", ev.EventType, "event_id", ev.ID, "attempts", ev.Attempts+1, "next_attempt_at", next, "error", herr)
			return nil
		}
		return d.repo.MarkPublished(tx, ev.ID, now)
	})
	return processed, err
}

// RunOnce memproses event sampai antrean kosong atau max event.
func (d *Dispatcher) RunOnce(ctx context.Context, max int) (int, error) {
	n := 0
	for n < max {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		ok, err := d.ProcessNext(ctx)
		if err != nil {
			return n, err
		}
		if !ok {
			break
		}
		n++
	}
	return n, nil
}

// runInSavepoint membatalkan efek handler yang gagal tanpa membatalkan tx
// (penanda gagal tetap bisa ditulis).
func runInSavepoint(tx *gorm.DB, fn func() error) error {
	const name = "outbox_handler"
	if err := tx.SavePoint(name).Error; err != nil {
		return err
	}
	if err := fn(); err != nil {
		if rbErr := tx.RollbackTo(name).Error; rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback savepoint: %w", rbErr))
		}
		return err
	}
	return tx.Exec("RELEASE SAVEPOINT " + name).Error
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
