package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"mganik/api/internal/apperror"
)

type fakeHealthStore struct {
	pingErr    error
	version    int64
	versionErr error
}

func (f fakeHealthStore) Ping(context.Context) error { return f.pingErr }
func (f fakeHealthStore) SchemaVersion(context.Context) (int64, error) {
	return f.version, f.versionErr
}

func TestReadiness(t *testing.T) {
	fixed := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		store    fakeHealthStore
		wantCode string
		wantVer  int64
	}{
		{name: "siap", store: fakeHealthStore{version: 1}, wantVer: 1},
		{name: "database mati", store: fakeHealthStore{pingErr: errors.New("connection refused")}, wantCode: "database_unavailable"},
		{name: "migrasi belum jalan", store: fakeHealthStore{versionErr: errors.New("no rows")}, wantCode: "schema_not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewHealthService(tt.store)
			svc.now = func() time.Time { return fixed }

			got, err := svc.Readiness(context.Background())
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("error tidak diharapkan: %v", err)
				}
				if got.SchemaVersion != tt.wantVer || !got.CheckedAt.Equal(fixed) {
					t.Fatalf("hasil salah: %+v", got)
				}
				return
			}
			var appErr *apperror.Error
			if !errors.As(err, &appErr) {
				t.Fatalf("ingin *apperror.Error, dapat %T (%v)", err, err)
			}
			if appErr.Code != tt.wantCode || appErr.Status != http.StatusServiceUnavailable {
				t.Fatalf("ingin %s/503, dapat %s/%d", tt.wantCode, appErr.Code, appErr.Status)
			}
			if !errors.Is(err, tt.store.pingErr) && !errors.Is(err, tt.store.versionErr) {
				t.Fatalf("penyebab asli hilang dari rantai error: %v", err)
			}
		})
	}
}
