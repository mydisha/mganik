package iamtest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/router"
	"mganik/api/internal/seed"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

var (
	t0 = time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	// Parameter argon2 ringan untuk fixture (hash dummy login tetap OWASP).
	fastParams = domain.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
)

const (
	demoPassword = config.DefaultDemoPassword
	testPIIKey   = "kunci-pii-test-iam-minimal-32-byte-panjang"
)

// syncBuffer: tujuan log yang aman dipakai bersamaan.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type env struct {
	t      testing.TB
	db     *gorm.DB
	clock  *clock.Fake
	engine http.Handler
	logs   *syncBuffer
	cfg    config.Config
}

type envOpt func(*config.Config)

// newEnv: database baru + data demo + router lengkap dengan jam palsu.
func newEnv(t testing.TB, opts ...envOpt) *env {
	t.Helper()
	db := testutil.NewDB(t)
	if _, err := seed.Run(context.Background(), db, seed.Options{
		Enabled: true, Pepper: []byte(config.DemoClaimPepper), DemoPassword: demoPassword,
		PasswordParams: fastParams, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		AppEnv: config.EnvTest, MaxBodyBytes: 1 << 20,
		PIIEncKey: []byte(testPIIKey), OTPMockCode: config.DemoOTPCode,
	}
	for _, o := range opts {
		o(&cfg)
	}
	logs := &syncBuffer{}
	clk := clock.NewFake(t0)
	engine, err := router.New(router.Deps{
		Config: cfg, DB: db, Clock: clk,
		Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, db: db, clock: clk, engine: engine, logs: logs, cfg: cfg}
}

type reqOpt func(*http.Request)

func withCookie(c *http.Cookie) reqOpt {
	return func(r *http.Request) {
		if c != nil {
			r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
}

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func fromIP(ip string) reqOpt { return func(r *http.Request) { r.RemoteAddr = ip + ":40000" } }

// do mengirim request. Body non-nil dikirim sebagai JSON (Content-Type
// application/json) kecuali opsi menimpanya.
func (e *env) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rd = strings.NewReader(b)
		default:
			raw, err := json.Marshal(b)
			if err != nil {
				e.t.Fatal(err)
			}
			rd = bytes.NewReader(raw)
		}
	}
	req := httptest.NewRequest(method, path, rd)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.engine.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) apperror.Problem {
	t.Helper()
	var p apperror.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body bukan problem+json: %s", rec.Body)
	}
	return p
}

func expectProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) apperror.Problem {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, ingin %d: %s", rec.Code, status, rec.Body)
	}
	p := problem(t, rec)
	if p.Code != code {
		t.Fatalf("code %q, ingin %q: %s", p.Code, code, rec.Body)
	}
	return p
}

func (e *env) login(email, password string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"email": email, "password": password}, opts...)
}

func (e *env) mustLogin(email string) *http.Cookie {
	e.t.Helper()
	rec := e.login(email, demoPassword)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", email, rec.Code, rec.Body)
	}
	c := cookieNamed(rec, "__Host-session")
	if c == nil {
		e.t.Fatal("cookie sesi tidak dipasang")
	}
	return c
}

type roleSpec struct {
	code, scope string
	locations   []string // kode lokasi demo
}

// createUser membuat user dengan password demo (argon2 ringan) dan role.
func (e *env) createUser(email, name, status string, roles ...roleSpec) uuid.UUID {
	e.t.Helper()
	hash, err := domain.HashPassword(demoPassword, fastParams)
	if err != nil {
		e.t.Fatal(err)
	}
	u := model.User{ID: uuid.Must(uuid.NewV7()), Email: email, Name: name, PasswordHash: hash, Status: status}
	if err := e.db.Create(&u).Error; err != nil {
		e.t.Fatal(err)
	}
	for _, r := range roles {
		ur := model.UserRole{ID: uuid.Must(uuid.NewV7()), UserID: u.ID, RoleCode: r.code, ScopeMode: r.scope}
		if err := e.db.Create(&ur).Error; err != nil {
			e.t.Fatal(err)
		}
		for _, code := range r.locations {
			if err := e.db.Create(&model.UserRoleLocation{UserRoleID: ur.ID, LocationID: seed.LocationID(code)}).Error; err != nil {
				e.t.Fatal(err)
			}
		}
	}
	return u.ID
}

type meBody struct {
	User struct {
		ID    uuid.UUID `json:"id"`
		Email string    `json:"email"`
		Name  string    `json:"name"`
	} `json:"user"`
	Roles []struct {
		Code      string `json:"code"`
		ScopeMode string `json:"scope_mode"`
		Locations []struct {
			ID   uuid.UUID `json:"id"`
			Code string    `json:"code"`
		} `json:"locations"`
	} `json:"roles"`
	Permissions []string `json:"permissions"`
	Scope       struct {
		Mode      string `json:"mode"`
		Locations []struct {
			ID   uuid.UUID `json:"id"`
			Code string    `json:"code"`
			Name string    `json:"name"`
		} `json:"locations"`
	} `json:"scope"`
}

func decodeMe(t *testing.T, rec *httptest.ResponseRecorder) meBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var me meBody
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	return me
}

func countRows(t *testing.T, db *gorm.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(q, args...).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}
