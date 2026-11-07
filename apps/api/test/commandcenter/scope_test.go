package commandcenter_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/auth"
	"mganik/api/internal/dto"
	"mganik/api/internal/pagination"
)

var ccGetPaths = []string{
	"/api/v1/cc/summary",
	"/api/v1/cc/stock",
	"/api/v1/cc/runout",
	"/api/v1/cc/anomalies",
	"/api/v1/cc/conflicts",
	"/api/v1/cc/distribution-leakage",
}

func TestAuth_UnauthenticatedAndForbidden(t *testing.T) {
	e := newEnv(t)
	anon := e.engine(nil)
	// Kasir punya inventory.read tetapi tidak cc.read.
	cashier := e.engine(&auth.Principal{Kind: auth.KindStaff, ID: newID(), Permissions: perms("inventory.read", "pos.sync"), ScopeAll: true})
	for _, p := range ccGetPaths {
		requireProblem(t, call(t, anon, http.MethodGet, p, nil), http.StatusUnauthorized, "unauthenticated")
		requireProblem(t, call(t, cashier, http.MethodGet, p, nil), http.StatusForbidden, "forbidden")
	}
	id := e.anomaly(anomalyFx{})
	body := map[string]any{"status": "ACK"}
	requireProblem(t, call(t, anon, http.MethodPatch, "/api/v1/cc/anomalies/"+id.String(), body), http.StatusUnauthorized, "unauthenticated")
	// cc.read saja tidak cukup untuk menindaklanjuti anomali.
	requireProblem(t, call(t, e.engine(jakselPrincipal()), http.MethodPatch, "/api/v1/cc/anomalies/"+id.String(), body),
		http.StatusForbidden, "forbidden")
	if n := e.count(`SELECT count(*) FROM anomalies WHERE id = ? AND status = 'OPEN'`, id); n != 1 {
		t.Fatal("anomali berubah walau ditolak")
	}
}

// TestScope_SelectedSeesOnlyOwnLocations: kepala toko Jakarta Selatan hanya
// melihat lokasinya di setiap endpoint.
func TestScope_SelectedSeesOnlyOwnLocations(t *testing.T) {
	e := newEnv(t)
	e.conflict(jakselID, sku1ID, "OPEN", testNow.Add(-time.Hour))
	e.conflict(bandungID, sku1ID, "OPEN", testNow.Add(-time.Hour))
	e.anomaly(anomalyFx{rule: "STOCK_DISCREPANCY", subjectType: "LOCATION", subjectID: &jakselID})
	e.anomaly(anomalyFx{rule: "STOCK_DISCREPANCY", subjectType: "LOCATION", subjectID: &bandungID})
	e.anomaly(anomalyFx{partyID: &sulselPartyID}) // anomali distribusi: hanya scope ALL
	h := e.engine(jakselPrincipal())

	var stock pagination.Page[dto.CCStockItem]
	getJSON(t, h, "/api/v1/cc/stock", &stock)
	if len(stock.Items) != 2 {
		t.Fatalf("stok %d baris, ingin 2 (dua SKU Jakarta Selatan)", len(stock.Items))
	}
	for _, it := range stock.Items {
		if it.LocationID != jakselID {
			t.Fatalf("stok lokasi lain bocor: %s", it.LocationCode)
		}
	}

	var runout dto.CCRunoutResponse
	getJSON(t, h, "/api/v1/cc/runout", &runout)
	if len(runout.Items) != 2 {
		t.Fatalf("runout %d baris, ingin 2", len(runout.Items))
	}
	for _, it := range runout.Items {
		if it.LocationID != jakselID {
			t.Fatalf("runout lokasi lain bocor: %s", it.LocationCode)
		}
	}

	var conflicts pagination.Page[dto.CCConflictItem]
	getJSON(t, h, "/api/v1/cc/conflicts", &conflicts)
	if len(conflicts.Items) != 1 || conflicts.Items[0].LocationID != jakselID {
		t.Fatalf("konflik salah: %+v", conflicts.Items)
	}

	var anomalies pagination.Page[dto.CCAnomalyItem]
	getJSON(t, h, "/api/v1/cc/anomalies", &anomalies)
	if len(anomalies.Items) != 1 || anomalies.Items[0].SubjectID == nil || *anomalies.Items[0].SubjectID != jakselID {
		t.Fatalf("anomali salah: %+v", anomalies.Items)
	}

	var leak dto.CCLeakageResponse
	getJSON(t, h, "/api/v1/cc/distribution-leakage", &leak)
	if len(leak.Items) != 0 {
		t.Fatalf("kepala toko tidak boleh melihat distributor: %+v", leak.Items)
	}

	var sum dto.CCSummaryResponse
	getJSON(t, h, "/api/v1/cc/summary", &sum)
	if sum.Stores.Total != 1 || sum.Conflicts.OpenTotal != 1 || sum.Anomalies.OpenTotal != 1 ||
		sum.Runout.CriticalItems != 1 || sum.Runout.WarningItems != 1 || sum.Runout.InsufficientDataItems != 0 ||
		sum.Anomalies.TopParty != nil {
		t.Fatalf("ringkasan scope salah: %+v", sum)
	}

	// Filter lokasi di luar scope ditolak eksplisit.
	for _, p := range []string{"/api/v1/cc/stock", "/api/v1/cc/runout", "/api/v1/cc/conflicts"} {
		requireProblem(t, call(t, h, http.MethodGet, p+"?location_id="+bandungID.String(), nil), http.StatusForbidden, "forbidden")
	}

	// Scope ALL melihat semuanya.
	var all pagination.Page[dto.CCConflictItem]
	getJSON(t, e.engine(opsPrincipal()), "/api/v1/cc/conflicts", &all)
	if len(all.Items) != 2 {
		t.Fatalf("scope ALL melihat %d konflik, ingin 2", len(all.Items))
	}
}

// TestScope_SelectedWithoutLocationsSeesNothing: SELECTED dengan daftar kosong
// bukan berarti semua lokasi.
func TestScope_SelectedWithoutLocationsSeesNothing(t *testing.T) {
	e := newEnv(t)
	e.conflict(jakselID, sku1ID, "OPEN", testNow)
	h := e.engine(&auth.Principal{Kind: auth.KindStaff, ID: newID(), Permissions: perms("cc.read")})
	for _, p := range []string{"/api/v1/cc/stock", "/api/v1/cc/runout", "/api/v1/cc/conflicts", "/api/v1/cc/anomalies", "/api/v1/cc/distribution-leakage"} {
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		getJSON(t, h, p, &page)
		if page.Items == nil || len(page.Items) != 0 {
			t.Fatalf("%s: ingin items [], dapat %d", p, len(page.Items))
		}
	}
	var sum dto.CCSummaryResponse
	getJSON(t, h, "/api/v1/cc/summary", &sum)
	if sum.Stores.Total != 0 || sum.Conflicts.OpenTotal != 0 || sum.Runout.CriticalItems != 0 {
		t.Fatalf("ringkasan harus nol: %+v", sum)
	}
}

func TestValidation_QueryParams(t *testing.T) {
	e := newEnv(t)
	h := e.engine(opsPrincipal())
	cases := []struct {
		path, field, code string
	}{
		{"/api/v1/cc/stock?limit=0", "limit", "out_of_range"},
		{"/api/v1/cc/stock?limit=201", "limit", "out_of_range"},
		{"/api/v1/cc/stock?limit=abc", "limit", "invalid_type"},
		{"/api/v1/cc/stock?cursor=rusak", "cursor", "invalid_cursor"},
		{"/api/v1/cc/stock?location_id=bukan-uuid", "location_id", "uuid"},
		{"/api/v1/cc/stock?location_type=hub", "location_type", "format"},
		{"/api/v1/cc/runout?level=MERAH", "level", "oneof"},
		{"/api/v1/cc/anomalies?status=DONE", "status", "oneof"},
		{"/api/v1/cc/anomalies?severity=low", "severity", "oneof"},
		{"/api/v1/cc/anomalies?rule=out%20of", "rule", "format"},
		{"/api/v1/cc/anomalies?party_id=00000000-0000-0000-0000-000000000000", "party_id", "uuid"},
		{"/api/v1/cc/anomalies?status=OPEN&status=ACK", "status", "duplicate"},
		{"/api/v1/cc/conflicts?kind=X", "kind", "oneof"},
		{"/api/v1/cc/distribution-leakage?days=0", "days", "out_of_range"},
		{"/api/v1/cc/distribution-leakage?days=91", "days", "out_of_range"},
		{"/api/v1/cc/distribution-leakage?days=1x", "days", "invalid_type"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			p := requireProblem(t, call(t, h, http.MethodGet, tc.path, nil), http.StatusUnprocessableEntity, "validation_failed")
			found := false
			for _, f := range p.Errors {
				if f.Field == tc.field && f.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("ingin error %s/%s, dapat %+v", tc.field, tc.code, p.Errors)
			}
		})
	}

	// Cursor milik endpoint lain ditolak.
	var page pagination.Page[dto.CCConflictItem]
	e.conflict(jakselID, sku1ID, "OPEN", testNow)
	e.conflict(jakselID, sku2ID, "OPEN", testNow)
	getJSON(t, h, "/api/v1/cc/conflicts?limit=1", &page)
	if page.NextCursor == nil {
		t.Fatal("ingin next_cursor")
	}
	requireProblem(t, call(t, h, http.MethodGet, "/api/v1/cc/anomalies?cursor="+*page.NextCursor, nil),
		http.StatusUnprocessableEntity, "validation_failed")

	// Tipe lokasi tak dikenal (format valid) = hasil kosong, bukan error.
	var empty pagination.Page[dto.CCStockItem]
	getJSON(t, h, "/api/v1/cc/stock?location_type=DARK_STORE", &empty)
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("ingin kosong: %+v", empty)
	}
	// ID produk yang tidak ada = hasil kosong.
	getJSON(t, h, "/api/v1/cc/stock?product_id="+uuid.Must(uuid.NewV7()).String(), &empty)
	if len(empty.Items) != 0 {
		t.Fatalf("ingin kosong: %+v", empty)
	}
}
