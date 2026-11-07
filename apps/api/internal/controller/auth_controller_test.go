package controller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/service"
)

type fakeAuthSvc struct {
	loginErr     error
	meErr        error
	loggedOut    []string
	loginToken   string
	loginUserID  uuid.UUID
	loginExpires time.Time
}

func (f *fakeAuthSvc) Login(context.Context, service.LoginInput) (service.LoginResult, error) {
	return service.LoginResult{Token: f.loginToken, UserID: f.loginUserID, ExpiresAt: f.loginExpires}, f.loginErr
}
func (f *fakeAuthSvc) Logout(_ context.Context, token string, _ service.RequestMeta) error {
	f.loggedOut = append(f.loggedOut, token)
	return nil
}
func (f *fakeAuthSvc) Me(context.Context, uuid.UUID) (service.Me, error) {
	return service.Me{Email: "admin@mganik.test"}, f.meErr
}

func runLogin(t *testing.T, svc *fakeAuthSvc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	SetupValidator()
	r := gin.New()
	r.POST("/login", NewAuthController(svc, slog.New(slog.NewTextHandler(io.Discard, nil))).Login)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"email":"admin@mganik.test","password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// Profil gagal setelah sesi dibuat: tidak boleh ada cookie hidup di respons
// error, dan sesinya dicabut.
func TestLogin_ProfileFailureLeavesNoCookie(t *testing.T) {
	svc := &fakeAuthSvc{loginToken: "token-sesi", meErr: errors.New("db putus")}
	rec := runLogin(t, svc)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if sc := rec.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Fatalf("cookie terpasang pada respons gagal: %v", sc)
	}
	if len(svc.loggedOut) != 1 || svc.loggedOut[0] != "token-sesi" {
		t.Fatalf("sesi tidak dicabut: %v", svc.loggedOut)
	}
}

func TestLogin_SuccessSetsCookieAfterProfile(t *testing.T) {
	rec := runLogin(t, &fakeAuthSvc{loginToken: "token-sesi"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Set-Cookie"), service.StaffSessionCookie+"=token-sesi") {
		t.Fatalf("status %d, cookie %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
}

func TestLogin_ServerBusyHasRetryAfter(t *testing.T) {
	// Bentuk yang sama dengan server_busy dari AuthService (semaphore argon2 penuh).
	busy := &service.RetryAfterError{
		Err:   apperror.New(http.StatusServiceUnavailable, "server_busy", "Server sibuk", "Coba lagi."),
		After: time.Second,
	}
	svc := &fakeAuthSvc{loginErr: busy}
	rec := runLogin(t, svc)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("status %d retry %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
