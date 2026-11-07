package iamtest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/auth"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

func (e *env) resolver() *service.AuthResolver {
	return service.NewAuthResolver(repository.NewAuthRepository(e.db), repository.NewConsumerRepository(e.db),
		e.clock, service.NewCSRFGuard(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func deviceReq(token, cashier string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pos/sync", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if cashier != "" {
		r.Header.Set(service.CashierHeader, cashier)
	}
	return r
}

func TestResolverDB_Device(t *testing.T) {
	e := newEnv(t)
	r := e.resolver()
	ctx := context.Background()
	jaksel := seed.LocationID(seed.StoreJakselCode)

	p, err := r.Resolve(ctx, deviceReq(seed.DemoDeviceToken(), ""))
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != auth.KindDevice || p.ID != seed.PosDeviceID() || p.ScopeAll || len(p.LocationIDs) != 1 ||
		p.LocationIDs[0] != jaksel || !p.Can("pos.sync") || len(p.Permissions) != 1 || p.ActorID != uuid.Nil {
		t.Fatalf("principal device: %+v", p)
	}

	// Kasir ber-scope lokasi device diterima.
	kasir := seed.UserID("kasir.jaksel@mganik.test")
	p, err = r.Resolve(ctx, deviceReq(seed.DemoDeviceToken(), kasir.String()))
	if err != nil || p.ActorID != kasir {
		t.Fatalf("kasir jaksel: %v %+v", err, p)
	}
	// Role ALL yang punya pos.sync (ADMIN) juga diterima.
	if _, err := r.Resolve(ctx, deviceReq(seed.DemoDeviceToken(), seed.UserID("admin@mganik.test").String())); err != nil {
		t.Fatalf("admin: %v", err)
	}

	bandungCashier := e.createUser("kasir.bandung@mganik.test", "Kasir Bandung", "ACTIVE",
		roleSpec{code: "CASHIER", scope: model.ScopeSelected, locations: []string{seed.StoreBandungCode}})
	// Permission dan scope harus dari role yang sama: pos.sync di Bandung +
	// role tanpa pos.sync di Jaksel tetap ditolak.
	mixed := e.createUser("campur@mganik.test", "Campur", "ACTIVE",
		roleSpec{code: "CASHIER", scope: model.ScopeSelected, locations: []string{seed.StoreBandungCode}},
		roleSpec{code: "WAREHOUSE_STAFF", scope: model.ScopeSelected, locations: []string{seed.StoreJakselCode}})
	noLoc := e.createUser("kasir.tanpa.lokasi@mganik.test", "Kasir Tanpa Lokasi", "ACTIVE",
		roleSpec{code: "CASHIER", scope: model.ScopeSelected})
	disabled := e.createUser("kasir.nonaktif@mganik.test", "Kasir Nonaktif", "DISABLED",
		roleSpec{code: "CASHIER", scope: model.ScopeSelected, locations: []string{seed.StoreJakselCode}})

	for name, id := range map[string]uuid.UUID{
		"kasir lokasi lain":            bandungCashier,
		"permission & scope beda role": mixed,
		"SELECTED tanpa lokasi":        noLoc,
		"kasir nonaktif":               disabled,
		"gudang tanpa pos.sync":        seed.UserID("gudang@mganik.test"),
		"marketing ALL tanpa pos.sync": seed.UserID("marketing@mganik.test"),
		"user tidak ada":               uuid.New(),
	} {
		if _, err := r.Resolve(ctx, deviceReq(seed.DemoDeviceToken(), id.String())); !errors.Is(err, service.ErrCashierNotAllowed) {
			t.Errorf("%s: ingin cashier_not_allowed, dapat %v", name, err)
		}
	}

	// Device dicabut → invalid_token.
	if err := e.db.Exec(`UPDATE pos_devices SET status = 'REVOKED', revoked_at = ? WHERE id = ?`, t0, seed.PosDeviceID()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(ctx, deviceReq(seed.DemoDeviceToken(), "")); !errors.Is(err, service.ErrInvalidToken) {
		t.Fatalf("device dicabut: %v", err)
	}
}

// Satu query per request staff walau user punya banyak role dan lokasi.
func TestResolverDB_StaffSingleQuery(t *testing.T) {
	e := newEnv(t)
	e.createUser("banyak@mganik.test", "Banyak Role", "ACTIVE",
		roleSpec{code: "CASHIER", scope: model.ScopeSelected, locations: []string{seed.StoreJakselCode, seed.StoreBandungCode}},
		roleSpec{code: "WAREHOUSE_STAFF", scope: model.ScopeSelected, locations: []string{seed.CentralWHCode, seed.HubSurabayaCode, seed.StoreJakselCode}},
		roleSpec{code: "STORE_MANAGER", scope: model.ScopeSelected, locations: []string{seed.StoreBandungCode}},
	)
	c := e.mustLogin("banyak@mganik.test")

	var queries atomic.Int64
	count := func(*gorm.DB) { queries.Add(1) }
	if err := e.db.Callback().Query().Before("gorm:query").Register("iamtest:count", count); err != nil {
		t.Fatal(err)
	}
	// Raw().Scan lewat callback Row; Exec lewat callback Raw.
	if err := e.db.Callback().Row().Before("gorm:row").Register("iamtest:count_row", count); err != nil {
		t.Fatal(err)
	}
	if err := e.db.Callback().Raw().Before("gorm:raw").Register("iamtest:count_raw", count); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = e.db.Callback().Query().Remove("iamtest:count")
		_ = e.db.Callback().Row().Remove("iamtest:count_row")
		_ = e.db.Callback().Raw().Remove("iamtest:count_raw")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: service.StaffSessionCookie, Value: c.Value})
	p, err := e.resolver().Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if n := queries.Load(); n != 1 {
		t.Fatalf("resolve memakai %d query, ingin 1", n)
	}
	if p.ScopeAll || len(p.LocationIDs) != 4 {
		t.Fatalf("lokasi gabungan (unik) salah: %v", p.LocationIDs)
	}
	for _, perm := range []string{"pos.sync", "shipment.create", "pos.device.manage"} {
		if !p.Can(perm) {
			t.Fatalf("permission %s hilang", perm)
		}
	}
	if p.Can("user.manage") {
		t.Fatal("permission admin bocor")
	}
	if !p.CanAccessLocation(seed.LocationID(seed.HubSurabayaCode)) || p.CanAccessLocation(seed.LocationID(seed.PartnerJatimCode)) {
		t.Fatal("scope lokasi salah")
	}
	// Scope per permission dari query yang sama: permission satu role tidak
	// berlaku di lokasi role lain.
	loc := seed.LocationID
	cases := []struct {
		perm, loc string
		want      bool
	}{
		{"pos.sync", seed.StoreJakselCode, true},           // CASHIER
		{"pos.sync", seed.StoreBandungCode, true},          // CASHIER + STORE_MANAGER
		{"pos.sync", seed.CentralWHCode, false},            // hanya WAREHOUSE di sana
		{"shipment.create", seed.CentralWHCode, true},      // WAREHOUSE
		{"shipment.create", seed.StoreBandungCode, false},  // WAREHOUSE tidak di Bandung
		{"pos.device.manage", seed.StoreBandungCode, true}, // STORE_MANAGER
		{"pos.device.manage", seed.StoreJakselCode, false}, // STORE_MANAGER hanya Bandung
		{"user.manage", seed.StoreJakselCode, false},
	}
	for _, c := range cases {
		if got := p.CanAt(c.perm, loc(c.loc)); got != c.want {
			t.Errorf("CanAt(%s, %s) = %v, ingin %v", c.perm, c.loc, got, c.want)
		}
	}
	// Admin (role ALL): CanAt di lokasi mana pun.
	ac := e.mustLogin("admin@mganik.test")
	areq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	areq.AddCookie(&http.Cookie{Name: service.StaffSessionCookie, Value: ac.Value})
	admin, err := e.resolver().Resolve(context.Background(), areq)
	if err != nil || !admin.CanAt("inventory.adjust", loc(seed.PartnerSulselCode)) {
		t.Fatalf("admin ALL: %v", err)
	}
}

// Jalur paling sering dengan DB sungguhan: cookie staff valid, tanpa touch.
func BenchmarkResolverDB_StaffCookieHit(b *testing.B) {
	e := newEnv(b)
	cookie := e.mustLogin("gudang@mganik.test")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/inventory/balances", nil)
	req.AddCookie(&http.Cookie{Name: service.StaffSessionCookie, Value: cookie.Value})
	r := service.NewAuthResolver(repository.NewAuthRepository(e.db), repository.NewConsumerRepository(e.db),
		e.clock, service.NewCSRFGuard(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Resolve(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}
