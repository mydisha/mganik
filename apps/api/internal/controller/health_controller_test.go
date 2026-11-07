package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/middleware"
	"mganik/api/internal/service"
)

type fakeReadiness struct {
	r   service.Readiness
	err error
}

func (f fakeReadiness) Readiness(context.Context) (service.Readiness, error) { return f.r, f.err }

func newTestEngine(svc readinessChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.Recovery(log))
	ctl := NewHealthController(svc, log)
	r.GET("/healthz", ctl.Live)
	r.GET("/readyz", ctl.Ready)
	return r
}

func TestReady_OK(t *testing.T) {
	at := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)
	engine := newTestEngine(fakeReadiness{r: service.Readiness{SchemaVersion: 1, CheckedAt: at}})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var body struct {
		Status        string `json:"status"`
		SchemaVersion int64  `json:"schema_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ready" || body.SchemaVersion != 1 {
		t.Fatalf("body salah: %+v", body)
	}
}

func TestReady_NotReadyWritesProblem(t *testing.T) {
	notReady := apperror.New(http.StatusServiceUnavailable, "database_unavailable", "Belum siap", "Database tidak bisa dihubungi.")
	engine := newTestEngine(fakeReadiness{err: notReady})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Header.Set("X-Request-ID", "req-test-0001")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type %q", ct)
	}
	var p apperror.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != "database_unavailable" || p.Status != 503 || p.RequestID != "req-test-0001" {
		t.Fatalf("problem salah: %+v", p)
	}
}

func TestReady_UnexpectedErrorHidesInternals(t *testing.T) {
	engine := newTestEngine(fakeReadiness{err: io.ErrUnexpectedEOF})

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	var p apperror.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != "internal_error" || p.Detail == io.ErrUnexpectedEOF.Error() {
		t.Fatalf("detail internal bocor ke klien: %+v", p)
	}
	if p.RequestID == "" {
		t.Fatal("request id kosong")
	}
}

func TestLive(t *testing.T) {
	engine := newTestEngine(fakeReadiness{})
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}
