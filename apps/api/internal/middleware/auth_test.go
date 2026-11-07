package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
)

type fakeResolver struct {
	p   *auth.Principal
	err error
}

func (f fakeResolver) Resolve(context.Context, *http.Request) (*auth.Principal, error) {
	return f.p, f.err
}

func staff(perms ...string) *auth.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return &auth.Principal{Kind: auth.KindStaff, ID: uuid.New(), Permissions: set}
}

func newAuthEngine(resolver auth.Resolver) *gin.Engine {
	gin.SetMode(gin.TestMode)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := gin.New()
	r.Use(RequestID(), Authenticate(resolver, log))
	r.GET("/open", func(c *gin.Context) {
		if auth.FromContext(c.Request.Context()) == nil {
			c.String(http.StatusOK, "anonim")
			return
		}
		c.String(http.StatusOK, "masuk")
	})
	r.GET("/stok", RequirePermission(log, "inventory.read"), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.GET("/kasir", RequireKind(log, auth.KindDevice), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	return r
}

func do(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRequirePermission(t *testing.T) {
	tests := []struct {
		name     string
		resolver auth.Resolver
		path     string
		want     int
		wantCode string
	}{
		{name: "tanpa resolver tetap anonim", resolver: nil, path: "/open", want: 200},
		{name: "anonim ke route terlindungi 401", resolver: fakeResolver{}, path: "/stok", want: 401, wantCode: "unauthenticated"},
		{name: "tanpa permission 403", resolver: fakeResolver{p: staff("order.read")}, path: "/stok", want: 403, wantCode: "forbidden"},
		{name: "dengan permission lolos", resolver: fakeResolver{p: staff("inventory.read")}, path: "/stok", want: 204},
		{name: "kind salah 403", resolver: fakeResolver{p: staff("pos.sync")}, path: "/kasir", want: 403, wantCode: "forbidden"},
		{name: "kredensial rusak ditolak sebelum handler", resolver: fakeResolver{err: apperror.New(401, "session_expired", "Sesi berakhir", "Masuk lagi.")}, path: "/open", want: 401, wantCode: "session_expired"},
		{name: "error sistem resolver jadi 500 tanpa detail", resolver: fakeResolver{err: errors.New("db mati")}, path: "/open", want: 500, wantCode: "internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, newAuthEngine(tt.resolver), tt.path)
			if rec.Code != tt.want {
				t.Fatalf("status %d, ingin %d (body %s)", rec.Code, tt.want, rec.Body)
			}
			if tt.wantCode != "" && !strings.Contains(rec.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Fatalf("body tidak memuat code %s: %s", tt.wantCode, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "db mati") {
				t.Fatalf("detail internal bocor: %s", rec.Body)
			}
		})
	}
}

func TestPrincipalLocationScope(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	selected := &auth.Principal{LocationIDs: []uuid.UUID{a}}
	all := &auth.Principal{ScopeAll: true}
	empty := &auth.Principal{}
	var nilP *auth.Principal

	if !selected.CanAccessLocation(a) || selected.CanAccessLocation(b) {
		t.Fatal("scope SELECTED salah")
	}
	if !all.CanAccessLocation(b) {
		t.Fatal("scope ALL harus mengizinkan semua lokasi")
	}
	if empty.CanAccessLocation(a) {
		t.Fatal("daftar lokasi kosong tanpa ScopeAll tidak boleh berarti semua lokasi")
	}
	if nilP.CanAccessLocation(a) || nilP.Can("cc.read") {
		t.Fatal("principal nil harus menolak semuanya")
	}
}
