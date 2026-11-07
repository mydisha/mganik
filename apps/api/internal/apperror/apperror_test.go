package apperror_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
)

// serve menjalankan handler yang menulis err lewat apperror.Write.
func serve(t *testing.T, err error) (*httptest.ResponseRecorder, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	r := gin.New()
	r.GET("/x", func(c *gin.Context) {
		c.Set(apperror.RequestIDKey, "req-12345678")
		apperror.Write(c, log, err)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	return rec, logs.String()
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) apperror.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type %q", ct)
	}
	var p apperror.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body bukan JSON: %v (%s)", err, rec.Body)
	}
	return p
}

func TestWrite_DomainError(t *testing.T) {
	cause := errors.New("pq: detail internal tabel serial_units")
	err := apperror.Wrap(cause, http.StatusConflict, "out_of_stock", "Stok habis", "Stok tidak cukup.")
	rec, logs := serve(t, fmt.Errorf("lapisan service: %w", err))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d", rec.Code)
	}
	p := decode(t, rec)
	if p.Code != "out_of_stock" || p.Status != http.StatusConflict || p.Title != "Stok habis" ||
		p.Detail != "Stok tidak cukup." || p.RequestID != "req-12345678" || p.Type != "about:blank" {
		t.Fatalf("problem salah: %+v", p)
	}
	// Penyebab internal hanya di log, tidak pernah ke klien.
	if strings.Contains(rec.Body.String(), "serial_units") {
		t.Fatal("penyebab internal bocor ke respons")
	}
	if !strings.Contains(logs, "serial_units") || !strings.Contains(logs, "req-12345678") {
		t.Fatalf("penyebab tidak tercatat di log: %s", logs)
	}
}

func TestWrite_UnknownErrorIsInternal(t *testing.T) {
	rec, logs := serve(t, errors.New("ERROR: duplicate key value violates unique constraint \"users_email_unique\""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	p := decode(t, rec)
	if p.Code != "internal_error" || p.RequestID != "req-12345678" {
		t.Fatalf("problem salah: %+v", p)
	}
	if strings.Contains(rec.Body.String(), "users_email_unique") || strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("pesan error asli bocor ke klien: %s", rec.Body)
	}
	if !strings.Contains(logs, "users_email_unique") {
		t.Fatal("error asli tidak dicatat di log")
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("akar")
	err := apperror.Wrap(cause, http.StatusBadRequest, "bad", "Buruk", "Detail")
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "akar") {
		t.Fatalf("rantai error hilang: %v", err)
	}
	if apperror.ErrNotFound.Status != http.StatusNotFound || apperror.ErrMethodNotAllowed.Status != http.StatusMethodNotAllowed {
		t.Fatal("status error umum salah")
	}
}
