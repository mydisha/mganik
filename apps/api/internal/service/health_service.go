// Package service berisi use case dan aturan bisnis. Service membuka
// transaksi, memanggil repository, dan mengembalikan error domain
// (apperror) yang dipetakan controller ke HTTP.
package service

import (
	"context"
	"net/http"
	"time"

	"mganik/api/internal/apperror"
)

// healthStore dipenuhi repository.HealthRepository. Interface didefinisikan
// di sisi pemakai supaya service bisa diuji dengan fake.
type healthStore interface {
	Ping(ctx context.Context) error
	SchemaVersion(ctx context.Context) (int64, error)
}

type Readiness struct {
	SchemaVersion int64
	CheckedAt     time.Time
}

type HealthService struct {
	store   healthStore
	timeout time.Duration
	now     func() time.Time
}

func NewHealthService(store healthStore) *HealthService {
	return &HealthService{store: store, timeout: 2 * time.Second, now: time.Now}
}

// Readiness memeriksa database bisa dihubungi dan migrasi sudah diterapkan.
func (s *HealthService) Readiness(ctx context.Context) (Readiness, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		return Readiness{}, apperror.Wrap(err, http.StatusServiceUnavailable, "database_unavailable",
			"Belum siap", "Database tidak bisa dihubungi.")
	}
	version, err := s.store.SchemaVersion(ctx)
	if err != nil {
		return Readiness{}, apperror.Wrap(err, http.StatusServiceUnavailable, "schema_not_ready",
			"Belum siap", "Migrasi database belum diterapkan.")
	}
	return Readiness{SchemaVersion: version, CheckedAt: s.now().UTC()}, nil
}
