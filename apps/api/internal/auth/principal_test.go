package auth

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

var (
	locA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	locB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
	locC = uuid.MustParse("00000000-0000-0000-0000-00000000000c")
)

func TestNilPrincipalDeniesEverything(t *testing.T) {
	var p *Principal
	if p.Can("cc.read") || p.CanAccessLocation(locA) || p.CanAt("cc.read", locA) {
		t.Fatal("principal nil tidak boleh punya akses")
	}
}

func TestCanAndCanAccessLocation(t *testing.T) {
	p := &Principal{
		Permissions: map[string]struct{}{"inventory.read": {}},
		LocationIDs: []uuid.UUID{locA},
	}
	if !p.Can("inventory.read") || p.Can("inventory.adjust") {
		t.Fatal("Can salah")
	}
	if !p.CanAccessLocation(locA) || p.CanAccessLocation(locB) {
		t.Fatal("CanAccessLocation SELECTED salah")
	}
	all := &Principal{ScopeAll: true}
	if !all.CanAccessLocation(locC) {
		t.Fatal("ScopeAll harus semua lokasi")
	}
}

// Daftar lokasi kosong TIDAK berarti semua lokasi.
func TestEmptyLocationsMeanNone(t *testing.T) {
	p := &Principal{
		Permissions:      map[string]struct{}{"inventory.read": {}},
		PermissionScopes: map[string]PermScope{"inventory.read": {}},
	}
	if p.CanAccessLocation(locA) || p.CanAt("inventory.read", locA) {
		t.Fatal("SELECTED tanpa lokasi memberi akses")
	}
}

// Role ALL (cc.read) + role SELECTED di A (inventory.adjust): adjust hanya di A,
// walau ScopeAll gabungan true dan Can+CanAccessLocation lama mengizinkan B.
func TestCanAt_AllRolePlusSelectedRole(t *testing.T) {
	p := &Principal{
		Permissions: map[string]struct{}{"cc.read": {}, "inventory.adjust": {}},
		ScopeAll:    true,
		LocationIDs: nil,
		PermissionScopes: map[string]PermScope{
			"cc.read":          {All: true},
			"inventory.adjust": {LocationIDs: []uuid.UUID{locA}},
		},
	}
	if !p.CanAt("cc.read", locB) || !p.CanAt("inventory.adjust", locA) {
		t.Fatal("akses sah ditolak")
	}
	if p.CanAt("inventory.adjust", locB) {
		t.Fatal("permission role SELECTED bocor ke lokasi lain lewat role ALL")
	}
	// Perilaku lama tetap (kontrak tidak diubah).
	if !(p.Can("inventory.adjust") && p.CanAccessLocation(locB)) {
		t.Fatal("Can/CanAccessLocation berubah perilaku")
	}
}

// Dua role SELECTED berbeda lokasi: perm masing-masing hanya di lokasinya.
func TestCanAt_TwoSelectedRoles(t *testing.T) {
	p := &Principal{
		Permissions: map[string]struct{}{"pos.sync": {}, "shipment.receive": {}, "inventory.read": {}},
		LocationIDs: []uuid.UUID{locA, locB},
		PermissionScopes: map[string]PermScope{
			"pos.sync":         {LocationIDs: []uuid.UUID{locA}},
			"shipment.receive": {LocationIDs: []uuid.UUID{locB}},
			"inventory.read":   {LocationIDs: []uuid.UUID{locA, locB}},
		},
	}
	cases := []struct {
		perm string
		loc  uuid.UUID
		want bool
	}{
		{"pos.sync", locA, true}, {"pos.sync", locB, false},
		{"shipment.receive", locB, true}, {"shipment.receive", locA, false},
		{"inventory.read", locA, true}, {"inventory.read", locB, true}, {"inventory.read", locC, false},
		{"order.cancel", locA, false},
	}
	for _, c := range cases {
		if got := p.CanAt(c.perm, c.loc); got != c.want {
			t.Errorf("CanAt(%s, %s) = %v, ingin %v", c.perm, c.loc, got, c.want)
		}
	}
}

// Principal manual tanpa PermissionScopes: CanAt fail closed.
func TestCanAt_FailsClosedWithoutScopes(t *testing.T) {
	p := &Principal{Permissions: map[string]struct{}{"cc.read": {}}, ScopeAll: true}
	if p.CanAt("cc.read", locA) {
		t.Fatal("CanAt tanpa PermissionScopes harus menolak")
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if FromContext(ctx) != nil {
		t.Fatal("context kosong harus anonim")
	}
	p := &Principal{Kind: KindStaff, ID: locA}
	if got := FromContext(WithPrincipal(ctx, p)); got != p {
		t.Fatal("principal tidak kembali dari context")
	}
}
