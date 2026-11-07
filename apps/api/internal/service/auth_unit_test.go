package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

var t0 = time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)

// ------------------------------------------------------------------ token

func TestSessionToken_RoundTrip(t *testing.T) {
	tok, hash, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 || len(hash) != 32 {
		t.Fatalf("token %d karakter, hash %d byte", len(tok), len(hash))
	}
	got, ok := hashSessionToken(tok)
	if !ok || string(got[:]) != string(hash) {
		t.Fatal("hash token tidak cocok")
	}
	tok2, _, _ := newSessionToken()
	if tok == tok2 {
		t.Fatal("token tidak acak")
	}
	for _, bad := range []string{"", "pendek", tok + "x", strings.Repeat("!", 43), tok[:42] + "="} {
		if _, ok := hashSessionToken(bad); ok {
			t.Errorf("token %q seharusnya ditolak", bad)
		}
	}
}

// ------------------------------------------------------------------ limiter

func TestAttemptLimiter_WindowAndReset(t *testing.T) {
	clk := clock.NewFake(t0)
	l := newAttemptLimiter(clk, 3, 15*time.Minute, 100)
	for i := 0; i < 3; i++ {
		if r := l.Reserve("a"); !r.OK {
			t.Fatalf("ditolak pada reservasi ke-%d", i+1)
		}
	}
	r := l.Reserve("a")
	if r.OK || r.RetryAfter != 15*time.Minute || !r.FirstRejection {
		t.Fatalf("ingin ditolak 15 menit (penolakan pertama), dapat %+v", r)
	}
	if r := l.Reserve("a"); r.OK || r.FirstRejection {
		t.Fatalf("penolakan kedua tidak boleh ditandai pertama: %+v", r)
	}
	if r := l.Reserve("b"); !r.OK {
		t.Fatal("kunci lain ikut diblok")
	}
	clk.Advance(10 * time.Minute)
	if r := l.Reserve("a"); r.RetryAfter != 5*time.Minute {
		t.Fatalf("sisa tunggu %v", r.RetryAfter)
	}
	clk.Advance(5 * time.Minute)
	if r := l.Reserve("a"); !r.OK {
		t.Fatal("blok tidak berakhir setelah jendela habis")
	}
	l.Reserve("a")
	l.Reset("a")
	l.Reserve("a")
	l.Reserve("a")
	if r := l.Reserve("a"); !r.OK {
		t.Fatal("Reset tidak menghapus hitungan")
	}
	// Release mengembalikan satu slot.
	if r := l.Reserve("a"); r.OK {
		t.Fatal("limit 3 terlewati")
	}
	l.Release("a")
	if r := l.Reserve("a"); !r.OK {
		t.Fatal("Release tidak mengembalikan slot")
	}
}

// Regresi blocker QA: cek dan catat harus atomik. 200 goroutine bersamaan
// pada satu kunci hanya boleh mendapat tepat `limit` slot.
func TestAttemptLimiter_ReserveIsAtomicUnderBurst(t *testing.T) {
	l := newAttemptLimiter(clock.NewFake(t0), 5, time.Minute, 100)
	var wg sync.WaitGroup
	var granted, firstRejections atomic.Int64
	start := make(chan struct{})
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := l.Reserve("ops@mganik.test")
			if r.OK {
				granted.Add(1)
			}
			if r.FirstRejection {
				firstRejections.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if granted.Load() != 5 || firstRejections.Load() != 1 {
		t.Fatalf("slot %d (ingin 5), penolakan pertama %d (ingin 1)", granted.Load(), firstRejections.Load())
	}
}

func TestAttemptLimiter_BoundedMemory(t *testing.T) {
	clk := clock.NewFake(t0)
	l := newAttemptLimiter(clk, 1, time.Minute, 3)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		l.Reserve(k)
		clk.Advance(time.Second)
	}
	if len(l.entries) > 3 {
		t.Fatalf("limiter menyimpan %d kunci, batas 3", len(l.entries))
	}
	// Kunci terbaru tetap tercatat.
	if r := l.Reserve("e"); r.OK {
		t.Fatal("kunci terbaru hilang")
	}
}

func TestTooManyAttempts_RoundsUpMinutes(t *testing.T) {
	for wait, want := range map[time.Duration]string{
		0:                "1 menit",
		30 * time.Second: "1 menit",
		61 * time.Second: "2 menit",
		15 * time.Minute: "15 menit",
	} {
		err := tooManyAttempts(wait, "percobaan masuk")
		var ra *RetryAfterError
		var ae *apperror.Error
		if !errors.As(err, &ra) || !errors.As(err, &ae) {
			t.Fatalf("tipe error salah: %T", err)
		}
		if ae.Status != http.StatusTooManyRequests || ae.Code != "too_many_attempts" || !strings.Contains(ae.Detail, want) {
			t.Errorf("%v: %+v", wait, ae)
		}
	}
}

// ------------------------------------------------------------------ CSRF

func TestCSRFGuard(t *testing.T) {
	g := NewCSRFGuard([]string{"http://localhost:5173"})
	req := func(method, ct, origin string, mut ...func(*http.Request)) *http.Request {
		r := httptest.NewRequest(method, "http://api.mganik.test/api/v1/x", nil)
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		for _, m := range mut {
			m(r)
		}
		return r
	}
	tests := []struct {
		name string
		r    *http.Request
		want error
	}{
		{"GET tidak diperiksa", req(http.MethodGet, "", "https://evil.test"), nil},
		{"POST JSON tanpa Origin", req(http.MethodPost, "application/json", ""), nil},
		{"POST JSON charset", req(http.MethodPost, "application/json; charset=utf-8", ""), nil},
		{"POST same-host", req(http.MethodPost, "application/json", "https://api.mganik.test"), nil},
		{"POST same-host huruf besar", req(http.MethodPost, "application/json", "HTTPS://API.MGANIK.TEST"), nil},
		{"POST origin allowlist", req(http.MethodPost, "application/json", "http://localhost:5173"), nil},
		{"POST origin asing", req(http.MethodPost, "application/json", "https://evil.test"), ErrCSRFRejected},
		{"POST origin beda port", req(http.MethodPost, "application/json", "https://api.mganik.test:8443"), ErrCSRFRejected},
		{"POST origin null", req(http.MethodPost, "application/json", "null"), ErrCSRFRejected},
		{"POST origin subdomain", req(http.MethodPost, "application/json", "https://evil.api.mganik.test"), ErrCSRFRejected},
		{"POST form", req(http.MethodPost, "application/x-www-form-urlencoded", ""), ErrUnsupportedMediaType},
		{"POST text/plain", req(http.MethodPost, "text/plain", ""), ErrUnsupportedMediaType},
		{"POST tanpa content-type", req(http.MethodPost, "", ""), ErrUnsupportedMediaType},
		{"DELETE tanpa content-type", req(http.MethodDelete, "", ""), ErrUnsupportedMediaType},
		{"PATCH JSON origin asing", req(http.MethodPatch, "application/json", "http://localhost:5174"), ErrCSRFRejected},
		{"PUT Sec-Fetch-Site cross-site tanpa Origin", req(http.MethodPut, "application/json", "", func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}), ErrCSRFRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := g.Check(tt.r); err != tt.want {
				t.Fatalf("dapat %v, ingin %v", err, tt.want)
			}
		})
	}
}

// ------------------------------------------------------------------ resolver (fake store)

type fakeStore struct {
	staff     map[[32]byte]*repository.StaffSession
	consumers map[[32]byte]*repository.ConsumerSessionAuth
	devices   map[[32]byte]*repository.DeviceAuth
	// cashier → lokasi yang diizinkan
	cashiers map[uuid.UUID]uuid.UUID
	touched  int
	queries  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		staff: map[[32]byte]*repository.StaffSession{}, consumers: map[[32]byte]*repository.ConsumerSessionAuth{},
		devices: map[[32]byte]*repository.DeviceAuth{}, cashiers: map[uuid.UUID]uuid.UUID{},
	}
}

func key(b []byte) (k [32]byte) { copy(k[:], b); return k }

func (f *fakeStore) StaffSessionByHash(_ context.Context, h []byte) (*repository.StaffSession, error) {
	f.queries++
	return f.staff[key(h)], nil
}
func (f *fakeStore) TouchSession(_ context.Context, id uuid.UUID, now, idle time.Time) error {
	f.touched++
	for _, s := range f.staff {
		if s.SessionID == id {
			s.LastSeenAt, s.IdleExpiresAt = now, idle
		}
	}
	return nil
}
func (f *fakeStore) DeviceByTokenHash(_ context.Context, h []byte) (*repository.DeviceAuth, error) {
	f.queries++
	return f.devices[key(h)], nil
}
func (f *fakeStore) CashierAllowedAtLocation(_ context.Context, userID, loc uuid.UUID) (bool, error) {
	f.queries++
	allowed, ok := f.cashiers[userID]
	return ok && allowed == loc, nil
}
func (f *fakeStore) ConsumerSessionByHash(_ context.Context, h []byte) (*repository.ConsumerSessionAuth, error) {
	f.queries++
	return f.consumers[key(h)], nil
}
func (f *fakeStore) TouchConsumerSession(_ context.Context, id uuid.UUID, now, idle time.Time) error {
	f.touched++
	return nil
}

func newTestResolver(f *fakeStore, clk clock.Clock) *AuthResolver {
	return NewAuthResolver(f, f, clk, NewCSRFGuard(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func (f *fakeStore) addStaff(t *testing.T, s repository.StaffSession) string {
	t.Helper()
	tok, h, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	f.staff[key(h)] = &s
	return tok
}

func activeStaff(user uuid.UUID) repository.StaffSession {
	return repository.StaffSession{
		SessionID: uuid.New(), UserID: user, IdleExpiresAt: t0.Add(StaffIdleTimeout), ExpiresAt: t0.Add(StaffAbsoluteTimeout),
		LastSeenAt: t0, UserStatus: "ACTIVE", PermissionGrants: model.StringArray{"inventory.read@*"},
	}
}

func staffReq(method, path, token string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: StaffSessionCookie, Value: token})
	if method != http.MethodGet {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestResolver_Anonymous(t *testing.T) {
	f := newFakeStore()
	p, err := newTestResolver(f, clock.NewFake(t0)).Resolve(context.Background(), httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil))
	if p != nil || err != nil || f.queries != 0 {
		t.Fatalf("tanpa kredensial harus anonim tanpa query: %v %v q=%d", p, err, f.queries)
	}
}

func TestResolver_StaffScope(t *testing.T) {
	locA, locB := uuid.New(), uuid.New()
	tests := []struct {
		name     string
		mod      func(*repository.StaffSession)
		scopeAll bool
		locs     []uuid.UUID
	}{
		{"ALL", func(s *repository.StaffSession) { s.ScopeAll = true }, true, nil},
		{"ALL mengabaikan lokasi role lain", func(s *repository.StaffSession) {
			s.ScopeAll = true
			s.LocationIDs = model.StringArray{locA.String()}
		}, true, nil},
		{"SELECTED dua lokasi", func(s *repository.StaffSession) {
			s.LocationIDs = model.StringArray{locA.String(), locB.String()}
		}, false, []uuid.UUID{locA, locB}},
		{"SELECTED tanpa lokasi = tidak ada akses", func(s *repository.StaffSession) {}, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeStore()
			user := uuid.New()
			s := activeStaff(user)
			tt.mod(&s)
			tok := f.addStaff(t, s)
			p, err := newTestResolver(f, clock.NewFake(t0)).Resolve(context.Background(), staffReq(http.MethodGet, "/api/v1/auth/me", tok))
			if err != nil {
				t.Fatal(err)
			}
			if p.Kind != auth.KindStaff || p.ID != user || p.ActorID != user || !p.Can("inventory.read") || p.Can("inventory.adjust") {
				t.Fatalf("principal salah: %+v", p)
			}
			if p.ScopeAll != tt.scopeAll || len(p.LocationIDs) != len(tt.locs) {
				t.Fatalf("scope %v %v, ingin %v %v", p.ScopeAll, p.LocationIDs, tt.scopeAll, tt.locs)
			}
			for _, l := range tt.locs {
				if !p.CanAccessLocation(l) {
					t.Fatalf("lokasi %s tidak bisa diakses", l)
				}
			}
			if !tt.scopeAll && p.CanAccessLocation(uuid.New()) {
				t.Fatal("SELECTED bisa mengakses lokasi lain")
			}
			if f.queries != 1 {
				t.Fatalf("resolve staff memakai %d query, ingin 1", f.queries)
			}
		})
	}
}

// Scope per permission: role ALL (cc.read) + role SELECTED di A
// (inventory.adjust) + role SELECTED tanpa lokasi (order.read).
func TestResolver_PermissionScopesPerRole(t *testing.T) {
	locA, locB := uuid.New(), uuid.New()
	f := newFakeStore()
	s := activeStaff(uuid.New())
	s.ScopeAll = true
	s.LocationIDs = model.StringArray{locA.String()}
	s.PermissionGrants = model.StringArray{"cc.read@*", "inventory.adjust@" + locA.String(), "order.read@"}
	tok := f.addStaff(t, s)
	p, err := newTestResolver(f, clock.NewFake(t0)).Resolve(context.Background(), staffReq(http.MethodGet, "/api/v1/auth/me", tok))
	if err != nil {
		t.Fatal(err)
	}
	for perm, want := range map[string]bool{"cc.read": true, "inventory.adjust": true, "order.read": true, "user.manage": false} {
		if p.Can(perm) != want {
			t.Errorf("Can(%s) = %v", perm, !want)
		}
	}
	if !p.CanAt("cc.read", locB) || !p.CanAt("inventory.adjust", locA) {
		t.Fatal("akses sah ditolak")
	}
	if p.CanAt("inventory.adjust", locB) {
		t.Fatal("inventory.adjust (role SELECTED A) berlaku di B lewat role ALL")
	}
	if p.CanAt("order.read", locA) {
		t.Fatal("role SELECTED tanpa lokasi memberi lokasi")
	}
}

func TestParsePermissionGrants_RejectsGarbage(t *testing.T) {
	for _, g := range []string{"tanpa-pemisah", "@*", "perm@bukan-uuid"} {
		if _, _, err := parsePermissionGrants([]string{g}); err == nil {
			t.Errorf("%q seharusnya ditolak", g)
		}
	}
}

func TestResolver_StaffExpiryAndTouch(t *testing.T) {
	user := uuid.New()
	tests := []struct {
		name    string
		mod     func(*repository.StaffSession)
		advance time.Duration
		wantErr bool
		touched int
	}{
		{"baru dipakai tidak update DB", nil, time.Minute, false, 0},
		{"lewat 5 menit diperpanjang", nil, SessionTouchInterval, false, 1},
		{"idle lewat", nil, StaffIdleTimeout, true, 0},
		{"tepat sebelum idle", nil, StaffIdleTimeout - time.Second, false, 1},
		{"absolut lewat walau idle diperpanjang", func(s *repository.StaffSession) {
			s.IdleExpiresAt = t0.Add(13 * time.Hour)
			s.ExpiresAt = t0.Add(StaffAbsoluteTimeout)
		}, StaffAbsoluteTimeout, true, 0},
		{"dicabut", func(s *repository.StaffSession) { now := t0; s.RevokedAt = &now }, 0, true, 0},
		{"user nonaktif", func(s *repository.StaffSession) { s.UserStatus = "DISABLED" }, 0, true, 0},
		{"user diarsip", func(s *repository.StaffSession) { now := t0; s.UserArchivedAt = &now }, 0, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeStore()
			s := activeStaff(user)
			if tt.mod != nil {
				tt.mod(&s)
			}
			tok := f.addStaff(t, s)
			clk := clock.NewFake(t0)
			clk.Advance(tt.advance)
			p, err := newTestResolver(f, clk).Resolve(context.Background(), staffReq(http.MethodGet, "/api/v1/auth/me", tok))
			if tt.wantErr {
				if !errors.Is(err, ErrSessionExpired) || p != nil {
					t.Fatalf("ingin session_expired, dapat %v %v", p, err)
				}
				return
			}
			if err != nil || p == nil {
				t.Fatalf("ingin sah, dapat %v", err)
			}
			if f.touched != tt.touched {
				t.Fatalf("touch %d kali, ingin %d", f.touched, tt.touched)
			}
		})
	}
}

func TestResolver_TouchNeverExtendsPastAbsolute(t *testing.T) {
	f := newFakeStore()
	s := activeStaff(uuid.New())
	tok := f.addStaff(t, s)
	clk := clock.NewFake(t0)
	r := newTestResolver(f, clk)
	// Dipakai terus tiap 4 jam: idle tidak pernah habis, absolut tetap berlaku.
	for i := 0; i < 3; i++ {
		clk.Advance(4 * time.Hour)
		p, err := r.Resolve(context.Background(), staffReq(http.MethodGet, "/api/v1/auth/me", tok))
		if i < 2 && (err != nil || p == nil) {
			t.Fatalf("jam ke-%d: %v", (i+1)*4, err)
		}
		if i == 2 && !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("jam ke-12 seharusnya kedaluwarsa absolut, dapat %v", err)
		}
	}
	for _, st := range f.staff {
		if st.IdleExpiresAt.After(st.ExpiresAt) {
			t.Fatalf("idle %v melewati absolut %v", st.IdleExpiresAt, st.ExpiresAt)
		}
	}
}

func TestResolver_InvalidStaffCookie(t *testing.T) {
	f := newFakeStore()
	r := newTestResolver(f, clock.NewFake(t0))
	tok, _, _ := newSessionToken() // tidak tersimpan
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/inventory/balances"} {
		if _, err := r.Resolve(context.Background(), staffReq(http.MethodGet, path, tok)); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("%s: ingin session_expired, dapat %v", path, err)
		}
	}
	// Token cacat ditolak tanpa query DB.
	q := f.queries
	if _, err := r.Resolve(context.Background(), staffReq(http.MethodGet, "/api/v1/auth/me", "cacat")); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("token cacat: %v", err)
	}
	if f.queries != q {
		t.Fatal("token cacat tetap meng-query DB")
	}
	// Login/logout tetap bisa dipakai dengan cookie basi (anonim).
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/logout"} {
		p, err := r.Resolve(context.Background(), staffReq(http.MethodPost, path, tok))
		if p != nil || err != nil {
			t.Fatalf("%s: cookie basi harus anonim, dapat %v %v", path, p, err)
		}
	}
}

func TestResolver_CSRFOnCookieRequests(t *testing.T) {
	f := newFakeStore()
	tok := f.addStaff(t, activeStaff(uuid.New()))
	r := newTestResolver(f, clock.NewFake(t0))

	evil := staffReq(http.MethodPost, "/api/v1/inventory/adjustments", tok)
	evil.Header.Set("Origin", "https://evil.test")
	if _, err := r.Resolve(context.Background(), evil); !errors.Is(err, ErrCSRFRejected) {
		t.Fatalf("origin asing: %v", err)
	}
	form := staffReq(http.MethodPost, "/api/v1/inventory/adjustments", tok)
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := r.Resolve(context.Background(), form); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("form: %v", err)
	}
	if f.queries != 0 {
		t.Fatal("CSRF harus ditolak sebelum menyentuh DB")
	}
	ok := staffReq(http.MethodPost, "/api/v1/inventory/adjustments", tok)
	ok.Header.Set("Origin", "http://example.com") // host httptest
	if p, err := r.Resolve(context.Background(), ok); err != nil || p == nil {
		t.Fatalf("same-origin ditolak: %v", err)
	}
}

func TestResolver_Device(t *testing.T) {
	loc, otherLoc := uuid.New(), uuid.New()
	cashier, foreignCashier := uuid.New(), uuid.New()
	f := newFakeStore()
	h := hashDeviceToken("token-aktif")
	devID := uuid.New()
	f.devices[h] = &repository.DeviceAuth{ID: devID, LocationID: loc, Status: "ACTIVE"}
	now := t0
	f.devices[hashDeviceToken("token-dicabut")] = &repository.DeviceAuth{ID: uuid.New(), LocationID: loc, Status: "REVOKED", RevokedAt: &now}
	f.cashiers[cashier] = loc
	f.cashiers[foreignCashier] = otherLoc
	// Cookie staff dari origin asing ikut terkirim: Bearer tetap menang dan
	// tidak terkena CSRF.
	staffTok := f.addStaff(t, activeStaff(uuid.New()))
	r := newTestResolver(f, clock.NewFake(t0))

	req := func(authz, cashierHdr string) *http.Request {
		q := httptest.NewRequest(http.MethodPost, "/api/v1/pos/sync", nil)
		q.Header.Set("Authorization", authz)
		q.Header.Set("Content-Type", "text/plain")
		q.Header.Set("Origin", "https://evil.test")
		q.AddCookie(&http.Cookie{Name: StaffSessionCookie, Value: staffTok})
		if cashierHdr != "" {
			q.Header.Set(CashierHeader, cashierHdr)
		}
		return q
	}

	p, err := r.Resolve(context.Background(), req("Bearer token-aktif", ""))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.KindDevice || p.ID != devID || p.ActorID != uuid.Nil || p.ScopeAll ||
		len(p.LocationIDs) != 1 || p.LocationIDs[0] != loc || !p.Can("pos.sync") || p.Can("inventory.adjust") {
		t.Fatalf("principal device salah: %+v", p)
	}
	if !p.CanAt("pos.sync", loc) || p.CanAt("pos.sync", otherLoc) {
		t.Fatal("pos.sync device harus hanya di lokasinya")
	}
	p, err = r.Resolve(context.Background(), req("bearer token-aktif", cashier.String()))
	if err != nil || p.ActorID != cashier {
		t.Fatalf("kasir sah: %v %+v", err, p)
	}

	cases := []struct {
		name, authz, cashier string
		want                 error
	}{
		{"token dicabut", "Bearer token-dicabut", "", ErrInvalidToken},
		{"token tidak dikenal", "Bearer ngawur", "", ErrInvalidToken},
		{"skema Basic", "Basic dXNlcjpwYXNz", "", ErrInvalidToken},
		{"Bearer kosong", "Bearer ", "", ErrInvalidToken},
		{"token terlalu panjang", "Bearer " + strings.Repeat("a", maxDeviceTokenLen+1), "", ErrInvalidToken},
		{"kasir lokasi lain", "Bearer token-aktif", foreignCashier.String(), ErrCashierNotAllowed},
		{"kasir tidak dikenal", "Bearer token-aktif", uuid.NewString(), ErrCashierNotAllowed},
		{"kasir bukan UUID", "Bearer token-aktif", "kasir-1", ErrCashierNotAllowed},
		{"kasir nil UUID", "Bearer token-aktif", uuid.Nil.String(), ErrCashierNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := r.Resolve(context.Background(), req(c.authz, c.cashier))
			if !errors.Is(err, c.want) || p != nil {
				t.Fatalf("ingin %v, dapat %v %v", c.want, p, err)
			}
		})
	}
}

func TestResolver_ConsumerOnlyOnPublicPaths(t *testing.T) {
	f := newFakeStore()
	tok, h, _ := newSessionToken()
	cid := uuid.New()
	f.consumers[key(h)] = &repository.ConsumerSessionAuth{
		SessionID: uuid.New(), ConsumerID: cid, IdleExpiresAt: t0.Add(ConsumerIdleTimeout),
		ExpiresAt: t0.Add(ConsumerAbsoluteTimeout), LastSeenAt: t0, ConsumerStatus: "ACTIVE",
	}
	clk := clock.NewFake(t0)
	r := newTestResolver(f, clk)
	req := func(path string) *http.Request {
		q := httptest.NewRequest(http.MethodGet, path, nil)
		q.AddCookie(&http.Cookie{Name: ConsumerSessionCookie, Value: tok})
		return q
	}
	p, err := r.Resolve(context.Background(), req("/api/v1/public/consumer/me"))
	if err != nil || p == nil || p.Kind != auth.KindConsumer || p.ID != cid || p.ActorID != uuid.Nil || len(p.Permissions) != 0 {
		t.Fatalf("principal konsumen salah: %+v %v", p, err)
	}
	// Cookie konsumen tidak berlaku di endpoint staff.
	if p, err := r.Resolve(context.Background(), req("/api/v1/auth/me")); p != nil || err != nil {
		t.Fatalf("cookie konsumen dipakai di luar /public: %v %v", p, err)
	}
	// Sesi konsumen basi di endpoint publik = anonim (verifikasi publik tetap jalan).
	clk.Advance(ConsumerIdleTimeout)
	if p, err := r.Resolve(context.Background(), req("/api/v1/public/verify")); p != nil || err != nil {
		t.Fatalf("sesi konsumen basi harus anonim: %v %v", p, err)
	}
	// CSRF tetap berlaku untuk cookie konsumen.
	post := req("/api/v1/public/claims")
	post.Method = http.MethodPost
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("Origin", "https://evil.test")
	if _, err := r.Resolve(context.Background(), post); !errors.Is(err, ErrCSRFRejected) {
		t.Fatalf("CSRF konsumen: %v", err)
	}
}

func TestResolver_BlockedConsumerIsAnonymous(t *testing.T) {
	f := newFakeStore()
	tok, h, _ := newSessionToken()
	f.consumers[key(h)] = &repository.ConsumerSessionAuth{
		SessionID: uuid.New(), ConsumerID: uuid.New(), IdleExpiresAt: t0.Add(time.Hour),
		ExpiresAt: t0.Add(time.Hour), LastSeenAt: t0, ConsumerStatus: "BLOCKED",
	}
	q := httptest.NewRequest(http.MethodGet, "/api/v1/public/consumer/me", nil)
	q.AddCookie(&http.Cookie{Name: ConsumerSessionCookie, Value: tok})
	if p, err := newTestResolver(f, clock.NewFake(t0)).Resolve(context.Background(), q); p != nil || err != nil {
		t.Fatalf("konsumen diblok harus anonim: %v %v", p, err)
	}
}

// Jalur paling sering: cookie staff valid, tanpa touch. Store palsu, jadi
// yang diukur adalah biaya resolver sendiri (parsing, hash, principal).
func BenchmarkResolver_StaffCookieHit(b *testing.B) {
	f := newFakeStore()
	tok, h, _ := newSessionToken()
	s := activeStaff(uuid.New())
	locA, locB := uuid.NewString(), uuid.NewString()
	s.PermissionGrants = model.StringArray{"cc.read@" + locA, "inventory.read@" + locA, "inventory.read@" + locB,
		"order.read@" + locB, "serial.trace@" + locA}
	s.LocationIDs = model.StringArray{locA, locB}
	f.staff[key(h)] = &s
	r := newTestResolver(f, clock.NewFake(t0))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/balances", nil)
	req.AddCookie(&http.Cookie{Name: StaffSessionCookie, Value: tok})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Resolve(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}
