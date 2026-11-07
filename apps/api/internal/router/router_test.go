package router_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"mganik/api/internal/apperror"
	"mganik/api/internal/config"
	"mganik/api/internal/router"
)

func newEngine(t *testing.T) http.Handler {
	t.Helper()
	// DB nil: route yang diuji di sini tidak menyentuh database.
	engine, err := router.New(router.Deps{
		Config: config.Config{AppEnv: config.EnvTest, MaxBodyBytes: 1 << 20},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestRouter_NotFoundAndMethodNotAllowed(t *testing.T) {
	engine := newEngine(t)
	tests := []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/tidak-ada", http.StatusNotFound, "not_found"},
		{http.MethodGet, "/api/v1/apa-saja", http.StatusNotFound, "not_found"},
		{http.MethodPost, "/healthz", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodDelete, "/readyz", http.StatusMethodNotAllowed, "method_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("X-Request-ID", "req-abcdefgh")
			engine.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status %d, ingin %d", rec.Code, tt.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Fatalf("content-type %q", ct)
			}
			var p apperror.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != tt.code || p.Status != tt.status || p.RequestID != "req-abcdefgh" {
				t.Fatalf("problem salah: %+v", p)
			}
			// Middleware keamanan tetap terpasang pada respons error.
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("security header hilang")
			}
		})
	}
}

func TestRouter_LiveDoesNotNeedDatabase(t *testing.T) {
	rec := httptest.NewRecorder()
	newEngine(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestRouter_RejectsInvalidTrustedProxies(t *testing.T) {
	_, err := router.New(router.Deps{
		Config: config.Config{AppEnv: config.EnvTest, TrustedProxies: []string{"bukan-cidr"}},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err == nil {
		t.Fatal("TRUSTED_PROXIES tidak valid seharusnya ditolak")
	}
}
