package iamtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/config"
	"mganik/api/internal/model"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// Alur utama end-to-end lewat router.New: login → /auth/me → logout.
func TestE2E_LoginMeLogout(t *testing.T) {
	e := newEnv(t)
	rec := e.login("admin@mganik.test", demoPassword, fromIP("203.0.113.10"),
		withHeader("User-Agent", "test-browser/1.0"), withHeader("Origin", "http://example.com"))
	me := decodeMe(t, rec)
	if me.User.Email != "admin@mganik.test" || me.User.ID != seed.UserID("admin@mganik.test") {
		t.Fatalf("user salah: %+v", me.User)
	}
	if me.Scope.Mode != model.ScopeAll || len(me.Scope.Locations) != 0 {
		t.Fatalf("admin harus scope ALL: %+v", me.Scope)
	}
	if !slices.Contains(me.Permissions, "user.manage") || !slices.IsSorted(me.Permissions) {
		t.Fatalf("permission admin: %v", me.Permissions)
	}
	if strings.Contains(rec.Body.String(), demoPassword) {
		t.Fatal("body login memuat password")
	}

	c := cookieNamed(rec, "__Host-session")
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" ||
		c.Domain != "" || c.MaxAge != int((12*time.Hour).Seconds()) {
		t.Fatalf("atribut cookie salah: %+v", c)
	}
	if strings.Contains(rec.Body.String(), c.Value) {
		t.Fatal("token sesi bocor di body")
	}
	// DB hanya menyimpan SHA-256 token.
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token bukan 32 byte base64url: %v", err)
	}
	sum := sha256.Sum256(raw)
	var sess model.Session
	if err := e.db.Where("token_hash = ?", sum[:]).First(&sess).Error; err != nil {
		t.Fatalf("sesi dengan hash token tidak ada: %v", err)
	}
	if !sess.ExpiresAt.Equal(t0.Add(12*time.Hour)) || !sess.IdleExpiresAt.Equal(t0.Add(8*time.Hour)) ||
		len(sess.IPHash) != 16 || sess.UserAgent == nil || *sess.UserAgent != "test-browser/1.0" {
		t.Fatalf("sesi tersimpan salah: %+v", sess)
	}
	if bytes.Contains(sess.IPHash, []byte("203.0.113")) {
		t.Fatal("IP tersimpan polos")
	}

	me2 := decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))
	if me2.User.ID != me.User.ID || len(me2.Permissions) != len(me.Permissions) {
		t.Fatal("/auth/me berbeda dengan body login")
	}

	out := e.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(c), fromIP("203.0.113.10"))
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout %d: %s", out.Code, out.Body)
	}
	if cl := cookieNamed(out, "__Host-session"); cl == nil || cl.MaxAge >= 0 || cl.Value != "" {
		t.Fatalf("logout tidak menghapus cookie: %+v", cl)
	}
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)), http.StatusUnauthorized, "session_expired")
	// Logout ulang dengan cookie basi tetap 204 (idempotent).
	if again := e.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(c)); again.Code != http.StatusNoContent {
		t.Fatalf("logout ulang %d", again.Code)
	}

	// Audit: login sukses + logout, dengan IP hash dan tanpa rahasia.
	var logs []model.AuditLog
	if err := e.db.Order("at, action").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, l := range logs {
		actions = append(actions, l.Action)
		if strings.Contains(string(l.After), demoPassword) || strings.Contains(string(l.After), c.Value) {
			t.Fatalf("audit memuat rahasia: %s", l.After)
		}
		if l.ActorType != model.ActorUser || l.ActorID == nil || *l.ActorID != me.User.ID || len(l.IPHash) != 16 {
			t.Fatalf("audit salah: %+v", l)
		}
	}
	if strings.Join(actions, ",") != service.AuditLoginSucceeded+","+service.AuditLogout {
		t.Fatalf("aksi audit %v", actions)
	}
}

func TestMe_ScopeSelectedAndMultiRole(t *testing.T) {
	e := newEnv(t)

	// Akun demo SELECTED dengan dua lokasi.
	me := decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(e.mustLogin("gudang@mganik.test"))))
	var codes []string
	for _, l := range me.Scope.Locations {
		codes = append(codes, l.Code)
	}
	slices.Sort(codes)
	if me.Scope.Mode != model.ScopeSelected || strings.Join(codes, ",") != seed.CentralWHCode+","+seed.HubSurabayaCode {
		t.Fatalf("scope gudang salah: %+v", me.Scope)
	}

	// Role SELECTED tanpa lokasi: tidak ada akses lokasi, BUKAN semua lokasi.
	e.createUser("tanpa.lokasi@mganik.test", "Tanpa Lokasi", "ACTIVE", roleSpec{code: "WAREHOUSE_STAFF", scope: model.ScopeSelected})
	me = decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(e.mustLogin("tanpa.lokasi@mganik.test"))))
	if me.Scope.Mode != model.ScopeSelected || len(me.Scope.Locations) != 0 {
		t.Fatalf("SELECTED kosong harus tanpa lokasi: %+v", me.Scope)
	}

	// Dua role: SELECTED + ALL → ALL; permission gabungan kedua role.
	e.createUser("dua.role@mganik.test", "Dua Role", "ACTIVE",
		roleSpec{code: "CASHIER", scope: model.ScopeSelected, locations: []string{seed.StoreJakselCode}},
		roleSpec{code: "MARKETING_ANALYST", scope: model.ScopeAll})
	me = decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(e.mustLogin("dua.role@mganik.test"))))
	if me.Scope.Mode != model.ScopeAll || len(me.Roles) != 2 {
		t.Fatalf("multi role: %+v", me)
	}
	for _, p := range []string{"pos.sync", "analytics.read"} {
		if !slices.Contains(me.Permissions, p) {
			t.Fatalf("permission %s hilang: %v", p, me.Permissions)
		}
	}
	for _, r := range me.Roles {
		if r.Code == "CASHIER" && (r.ScopeMode != model.ScopeSelected || len(r.Locations) != 1 || r.Locations[0].Code != seed.StoreJakselCode) {
			t.Fatalf("role kasir: %+v", r)
		}
	}
}

func TestLogin_FailuresAreGenericAndAudited(t *testing.T) {
	e := newEnv(t)
	wrong := expectProblem(t, e.login("admin@mganik.test", "password-salah-sekali"), http.StatusUnauthorized, "invalid_credentials")
	unknown := expectProblem(t, e.login("tidak.ada@mganik.test", "password-salah-sekali"), http.StatusUnauthorized, "invalid_credentials")
	if wrong.Detail != "Email atau password salah." || wrong.Detail != unknown.Detail || wrong.Title != unknown.Title {
		t.Fatalf("pesan gagal tidak generik: %q vs %q", wrong.Detail, unknown.Detail)
	}

	// Email tanpa membedakan huruf besar/kecil dan spasi tepi.
	decodeMe(t, e.login("  ADMIN@MGANIK.TEST ", demoPassword))

	// User nonaktif: password benar → 403 account_disabled; password salah
	// tetap 401 generik (status tidak bocor tanpa password benar).
	e.createUser("nonaktif@mganik.test", "Nonaktif", "DISABLED", roleSpec{code: "ADMIN", scope: model.ScopeAll})
	expectProblem(t, e.login("nonaktif@mganik.test", demoPassword), http.StatusForbidden, "account_disabled")
	expectProblem(t, e.login("nonaktif@mganik.test", "salah-salah-salah"), http.StatusUnauthorized, "invalid_credentials")

	// User terkunci (locked_until di masa depan) diperlakukan sama.
	locked := e.createUser("terkunci@mganik.test", "Terkunci", "ACTIVE", roleSpec{code: "ADMIN", scope: model.ScopeAll})
	if err := e.db.Exec(`UPDATE users SET locked_until = ? WHERE id = ?`, t0.Add(time.Hour), locked).Error; err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.login("terkunci@mganik.test", demoPassword), http.StatusForbidden, "account_disabled")

	// User diarsip tidak bisa login (seperti email tidak dikenal).
	arch := e.createUser("arsip@mganik.test", "Arsip", "ACTIVE", roleSpec{code: "ADMIN", scope: model.ScopeAll})
	if err := e.db.Exec(`UPDATE users SET archived_at = ? WHERE id = ?`, t0, arch).Error; err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.login("arsip@mganik.test", demoPassword), http.StatusUnauthorized, "invalid_credentials")

	// Audit gagal: user dikenal punya actor_id, email tak dikenal NULL.
	type row struct {
		ActorID *uuid.UUID
		After   string
	}
	var rows []row
	if err := e.db.Raw(`SELECT actor_id, after::text AS after FROM audit_logs WHERE action = ? ORDER BY at, id`,
		service.AuditLoginFailed).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("audit gagal %d baris, ingin 6", len(rows))
	}
	if rows[0].ActorID == nil || *rows[0].ActorID != seed.UserID("admin@mganik.test") || rows[1].ActorID != nil {
		t.Fatalf("actor audit salah: %+v", rows[:2])
	}
	if !strings.Contains(rows[2].After, "account_disabled") || !strings.Contains(rows[0].After, "invalid_credentials") {
		t.Fatalf("alasan audit: %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(r.After, "salah") || strings.Contains(r.After, demoPassword) {
			t.Fatalf("audit memuat password: %s", r.After)
		}
	}
}

func TestLogin_InputValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name   string
		body   any
		status int
		code   string
	}{
		{"body kosong", "", http.StatusBadRequest, "invalid_request"},
		{"bukan JSON", "{email", http.StatusBadRequest, "invalid_request"},
		{"field asing", map[string]string{"email": "a@b.c", "password": "x", "role": "ADMIN"}, http.StatusBadRequest, "invalid_request"},
		{"email kosong", map[string]string{"email": "", "password": "x"}, http.StatusUnprocessableEntity, "validation_failed"},
		{"email spasi saja", map[string]string{"email": "   ", "password": "x"}, http.StatusUnprocessableEntity, "validation_failed"},
		{"password kosong", map[string]string{"email": "admin@mganik.test", "password": ""}, http.StatusUnprocessableEntity, "validation_failed"},
		{"email 255 karakter", map[string]string{"email": strings.Repeat("a", 245) + "@mganik.test", "password": "x"}, http.StatusUnprocessableEntity, "validation_failed"},
		{"password 1025 karakter", map[string]string{"email": "admin@mganik.test", "password": strings.Repeat("p", 1025)}, http.StatusUnprocessableEntity, "validation_failed"},
		{"tipe salah", map[string]any{"email": 1, "password": "x"}, http.StatusUnprocessableEntity, "validation_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectProblem(t, e.do(http.MethodPost, "/api/v1/auth/login", c.body), c.status, c.code)
		})
	}
	// Batas atas yang masih diterima (254 karakter / 1024 karakter) → gagal generik, bukan 422.
	expectProblem(t, e.login(strings.Repeat("a", 242)+"@mganik.test", strings.Repeat("p", 1024)), http.StatusUnauthorized, "invalid_credentials")
}

func TestLogin_UnicodeEmailAndName(t *testing.T) {
	e := newEnv(t)
	e.createUser("rené.żółć@contoh.test", "Siti Nurhaliza 李小龙 ✓", "ACTIVE", roleSpec{code: "MARKETING_ANALYST", scope: model.ScopeAll})
	me := decodeMe(t, e.login("RENÉ.ŻÓŁĆ@contoh.test", demoPassword))
	if me.User.Name != "Siti Nurhaliza 李小龙 ✓" || me.User.Email != "rené.żółć@contoh.test" {
		t.Fatalf("unicode rusak: %+v", me.User)
	}
}

func TestLogin_RateLimitPerEmailAndIP(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		expectProblem(t, e.login("ops@mganik.test", "salah", fromIP("198.51.100.1")), http.StatusUnauthorized, "invalid_credentials")
	}
	// Percobaan ke-6, bahkan dengan password benar dan IP lain, ditolak.
	rec := e.login("ops@mganik.test", demoPassword, fromIP("198.51.100.2"))
	p := expectProblem(t, rec, http.StatusTooManyRequests, "too_many_attempts")
	if rec.Header().Get("Retry-After") != "900" || !strings.Contains(p.Detail, "15 menit") {
		t.Fatalf("retry-after %q, detail %q", rec.Header().Get("Retry-After"), p.Detail)
	}
	// Email lain tidak ikut terblok.
	decodeMe(t, e.login("admin@mganik.test", demoPassword, fromIP("198.51.100.2")))

	e.clock.Advance(10 * time.Minute)
	p = expectProblem(t, e.login("ops@mganik.test", demoPassword), http.StatusTooManyRequests, "too_many_attempts")
	if !strings.Contains(p.Detail, "5 menit") {
		t.Fatalf("sisa menit: %q", p.Detail)
	}
	e.clock.Advance(5 * time.Minute)
	decodeMe(t, e.login("ops@mganik.test", demoPassword))

	// Per IP: 20 kegagalan dari satu IP dengan email berbeda-beda.
	for i := 0; i < 20; i++ {
		expectProblem(t, e.login("acak"+string(rune('a'+i))+"@mganik.test", "salah", fromIP("192.0.2.99")),
			http.StatusUnauthorized, "invalid_credentials")
	}
	expectProblem(t, e.login("marketing@mganik.test", demoPassword, fromIP("192.0.2.99")), http.StatusTooManyRequests, "too_many_attempts")
	decodeMe(t, e.login("marketing@mganik.test", demoPassword, fromIP("192.0.2.100")))
}

func TestLogin_ReplacesPreviousSession(t *testing.T) {
	e := newEnv(t)
	first := e.mustLogin("admin@mganik.test")
	rec := e.login("admin@mganik.test", demoPassword, withCookie(first))
	second := cookieNamed(rec, "__Host-session")
	if rec.Code != http.StatusOK || second == nil || second.Value == first.Value {
		t.Fatalf("login ulang: %d", rec.Code)
	}
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(first)), http.StatusUnauthorized, "session_expired")
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(second)))
}

func TestSession_IdleTimeout(t *testing.T) {
	e := newEnv(t)
	c := e.mustLogin("ops@mganik.test")
	e.clock.Advance(8*time.Hour - time.Second)
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))

	c2 := e.mustLogin("marketing@mganik.test")
	e.clock.Advance(8 * time.Hour)
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c2)), http.StatusUnauthorized, "session_expired")
}

func TestSession_AbsoluteTimeoutAndThrottledTouch(t *testing.T) {
	e := newEnv(t)
	c := e.mustLogin("ops@mganik.test")
	lastSeen := func() time.Time {
		var s model.Session
		if err := e.db.Where("user_id = ?", seed.UserID("ops@mganik.test")).First(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s.LastSeenAt
	}

	// Dalam 5 menit: tidak ada UPDATE ke sessions.
	e.clock.Advance(4 * time.Minute)
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))
	if !lastSeen().Equal(t0) {
		t.Fatalf("sesi disentuh sebelum 5 menit: %v", lastSeen())
	}
	e.clock.Advance(2 * time.Minute)
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))
	if !lastSeen().Equal(t0.Add(6 * time.Minute)) {
		t.Fatalf("sesi tidak diperpanjang setelah 5 menit: %v", lastSeen())
	}

	// Dipakai tiap 7 jam (di bawah idle 8 jam) tetap mati di jam ke-12.
	e.clock.Advance(7 * time.Hour) // t0 + 7j6m
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))
	e.clock.Advance(4*time.Hour + 53*time.Minute) // t0 + 11j59m
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))
	e.clock.Advance(time.Minute) // t0 + 12j
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)), http.StatusUnauthorized, "session_expired")
	var s model.Session
	if err := e.db.Where("user_id = ?", seed.UserID("ops@mganik.test")).First(&s).Error; err != nil {
		t.Fatal(err)
	}
	if s.IdleExpiresAt.After(s.ExpiresAt) {
		t.Fatal("idle melewati batas absolut")
	}
}

func TestSession_DisabledUserLosesSession(t *testing.T) {
	e := newEnv(t)
	c := e.mustLogin("marketing@mganik.test")
	if err := e.db.Exec(`UPDATE users SET status = 'DISABLED' WHERE id = ?`, seed.UserID("marketing@mganik.test")).Error; err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)), http.StatusUnauthorized, "session_expired")
}

func TestMe_RequiresStaff(t *testing.T) {
	e := newEnv(t)
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil), http.StatusUnauthorized, "unauthenticated")
	// Token device tidak boleh memakai endpoint staff.
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withHeader("Authorization", "Bearer "+seed.DemoDeviceToken())),
		http.StatusForbidden, "forbidden")
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withHeader("Authorization", "Bearer palsu")),
		http.StatusUnauthorized, "invalid_token")
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(&http.Cookie{Name: "__Host-session", Value: "cacat"})),
		http.StatusUnauthorized, "session_expired")
}

func TestCSRF_E2E(t *testing.T) {
	e := newEnv(t)
	c := e.mustLogin("admin@mganik.test")

	expectProblem(t, e.login("admin@mganik.test", demoPassword, withHeader("Origin", "https://evil.test")),
		http.StatusForbidden, "csrf_rejected")
	expectProblem(t, e.do(http.MethodPost, "/api/v1/auth/login", `{"email":"admin@mganik.test","password":"x"}`,
		withHeader("Content-Type", "text/plain")), http.StatusUnsupportedMediaType, "unsupported_media_type")
	expectProblem(t, e.do(http.MethodPost, "/api/v1/auth/login", "email=admin%40mganik.test&password=x",
		withHeader("Content-Type", "application/x-www-form-urlencoded")), http.StatusUnsupportedMediaType, "unsupported_media_type")

	// Logout lintas situs ditolak dan sesi tetap hidup.
	expectProblem(t, e.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(c), withHeader("Origin", "https://evil.test")),
		http.StatusForbidden, "csrf_rejected")
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))
	// Logout tanpa Content-Type JSON juga ditolak.
	expectProblem(t, e.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(c), withHeader("Content-Type", "")),
		http.StatusUnsupportedMediaType, "unsupported_media_type")
	decodeMe(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)))
}

func TestCSRF_AllowedOrigins(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AllowedOrigins = []string{"http://localhost:5173"} })
	decodeMe(t, e.login("admin@mganik.test", demoPassword, withHeader("Origin", "http://localhost:5173")))
	expectProblem(t, e.login("admin@mganik.test", demoPassword, withHeader("Origin", "http://localhost:5174")),
		http.StatusForbidden, "csrf_rejected")
}

// Body login sukses = bentuk /auth/me (kontrak frontend).
func TestLogin_ResponseShape(t *testing.T) {
	e := newEnv(t)
	rec := e.login("kasir.jaksel@mganik.test", demoPassword)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"user", "roles", "permissions", "scope"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("field %s hilang: %s", k, rec.Body)
		}
	}
	if len(raw) != 4 {
		t.Fatalf("field tak terduga: %s", rec.Body)
	}
}
