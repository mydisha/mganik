package router_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mganik/api/internal/apperror"
	"mganik/api/internal/config"
	"mganik/api/internal/router"
)

// Route Fase 3 terpasang di router utama: route staff menolak anonim, dan
// webhook ditolak seluruhnya kalau WEBHOOK_SECRET kosong (key kosong bisa
// dipalsukan siapa pun).
func TestRouter_InventoryRoutesRegistered(t *testing.T) {
	engine, err := router.New(router.Deps{
		Config: config.Config{AppEnv: config.EnvTest, MaxBodyBytes: 1 << 20},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodGet, "/api/v1/inventory/balances", 401, "unauthenticated"},
		{http.MethodPost, "/api/v1/inventory/adjustments", 401, "unauthenticated"},
		{http.MethodPost, "/api/v1/inventory/quarantine", 401, "unauthenticated"},
		{http.MethodPost, "/api/v1/orders", 401, "unauthenticated"},
		{http.MethodGet, "/api/v1/orders/0193b6d2-0000-7000-8000-000000000000", 401, "unauthenticated"},
		{http.MethodPost, "/api/v1/orders/x/cancel", 401, "unauthenticated"},
		{http.MethodPost, "/api/v1/orders/x/fulfill", 401, "unauthenticated"},
		{http.MethodPost, "/api/v1/webhooks/payments/mock", 503, "webhook_not_configured"},
		{http.MethodPost, "/api/v1/channels/marketplace/orders", 503, "webhook_not_configured"},
	} {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
		var p apperror.Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != tc.status || p.Code != tc.code {
			t.Errorf("%s %s: %d %s, ingin %d %s", tc.method, tc.path, rec.Code, p.Code, tc.status, tc.code)
		}
	}
}
