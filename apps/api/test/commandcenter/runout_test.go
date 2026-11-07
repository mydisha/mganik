package commandcenter_test

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/dto"
)

type runoutWant struct {
	level   string
	sold    int64
	avg     *float64
	doi     *float64
	runout  *string
	reasons []string
	policy  bool
}

// runoutFixture membuat satu toko uji berisi 12 SKU yang masing-masing
// mewakili satu kasus runout. Policy (bila ada): min_qty, lead 3, safety 2.
func runoutFixture(e *env) (uuid.UUID, map[string]runoutWant) {
	loc := e.location("TKO-UJI", "OWN_STORE", nil, ptr("31.71"))
	type fx struct {
		sku       string
		avail     int64
		policy    bool
		minQty    int64
		salesType string
		perDay    int64
		days      int
	}
	fixtures := []fx{
		{sku: "A-CRIT", avail: 2, policy: true, minQty: 1, salesType: "DIRECT_SALE", perDay: 1, days: 28},
		{sku: "B-WARN", avail: 4, policy: true, minQty: 1, salesType: "DIRECT_SALE", perDay: 1, days: 28},
		{sku: "C-OK", avail: 10, policy: true, minQty: 1, salesType: "DIRECT_SALE", perDay: 1, days: 28},
		{sku: "D-NEW", avail: 5, policy: true, minQty: 1},
		{sku: "E-HABIS", avail: 0, policy: true, minQty: 0, salesType: "DIRECT_SALE", perDay: 1, days: 14},
		{sku: "F-NOPOL", avail: 1},
		{sku: "G-NOPOL-OK", avail: 5, salesType: "DIRECT_SALE", perDay: 1, days: 28},
		{sku: "H-NOPOL-0", avail: 0, salesType: "DIRECT_SALE", perDay: 1, days: 28},
		{sku: "I-MIN", avail: 1, policy: true, minQty: 2},
		{sku: "K-COMMIT", avail: 4, policy: true, minQty: 1, salesType: "COMMIT_SALE", perDay: 2, days: 28},
		{sku: "L-ZERO", avail: 0, policy: true, minQty: 0},
	}
	ids := map[string]uuid.UUID{}
	for _, f := range fixtures {
		p := e.product(f.sku)
		ids[f.sku] = p
		e.balance(loc, p, f.avail, 0)
		if f.policy {
			e.policy(loc, p, f.minQty, 3, 2)
		}
		if f.days > 0 {
			e.dailySales(loc, p, f.salesType, f.perDay, f.days)
		}
	}
	// J: penjualan hanya di luar jendela 28 hari → data belum cukup.
	j := e.product("J-OLD")
	e.balance(loc, j, 3, 0)
	e.policy(loc, j, 1, 3, 2)
	e.saleAt(loc, j, 50, testNow.Add(-28*24*time.Hour-time.Minute))
	e.saleAt(loc, j, 50, testNow.Add(-40*24*time.Hour))
	// C: penjualan tepat "sekarang" dan di masa depan (jam salah) tidak dihitung.
	e.saleAt(loc, ids["C-OK"], 100, testNow)
	e.saleAt(loc, ids["C-OK"], 100, testNow.Add(time.Hour))

	f := func(v float64) *float64 { return &v }
	s := func(v string) *string { return &v }
	return loc, map[string]runoutWant{
		"A-CRIT": {level: "CRITICAL", sold: 28, avg: f(1), doi: f(2), runout: s("2026-11-09"), reasons: []string{"BELOW_LEAD_TIME"}, policy: true},
		"B-WARN": {level: "WARNING", sold: 28, avg: f(1), doi: f(4), runout: s("2026-11-11"), reasons: []string{"BELOW_SAFETY_DAYS"}, policy: true},
		"C-OK":   {level: "OK", sold: 28, avg: f(1), doi: f(10), runout: s("2026-11-17"), reasons: []string{}, policy: true},
		"D-NEW":  {level: "INSUFFICIENT_DATA", reasons: []string{"NO_SALES_HISTORY"}, policy: true},
		// Riwayat 14 hari: rata-rata dibagi 14 hari, bukan 28.
		"E-HABIS":    {level: "CRITICAL", sold: 14, avg: f(1), doi: f(0), runout: s("2026-11-07"), reasons: []string{"STOCK_OUT", "BELOW_LEAD_TIME"}, policy: true},
		"F-NOPOL":    {level: "INSUFFICIENT_DATA", reasons: []string{"NO_SALES_HISTORY", "NO_POLICY"}},
		"G-NOPOL-OK": {level: "OK", sold: 28, avg: f(1), doi: f(5), runout: s("2026-11-12"), reasons: []string{"NO_POLICY"}},
		"H-NOPOL-0":  {level: "CRITICAL", sold: 28, avg: f(1), doi: f(0), runout: s("2026-11-07"), reasons: []string{"STOCK_OUT", "NO_POLICY"}},
		"I-MIN":      {level: "WARNING", reasons: []string{"BELOW_MIN_QTY", "NO_SALES_HISTORY"}, policy: true},
		"J-OLD":      {level: "INSUFFICIENT_DATA", reasons: []string{"NO_SALES_HISTORY"}, policy: true},
		"K-COMMIT":   {level: "CRITICAL", sold: 56, avg: f(2), doi: f(2), runout: s("2026-11-09"), reasons: []string{"BELOW_LEAD_TIME"}, policy: true},
		"L-ZERO":     {level: "INSUFFICIENT_DATA", reasons: []string{"NO_SALES_HISTORY"}, policy: true},
	}
}

// Urutan paling kritis dulu: level, DOI naik, lalu SKU.
var runoutOrder = []string{
	"E-HABIS", "H-NOPOL-0", "A-CRIT", "K-COMMIT", // CRITICAL: DOI 0, 0, 2, 2
	"B-WARN", "I-MIN", // WARNING: DOI 4, tanpa DOI
	"D-NEW", "F-NOPOL", "J-OLD", "L-ZERO", // INSUFFICIENT_DATA
	"G-NOPOL-OK", "C-OK", // OK: DOI 5, 10
}

func eqFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) < 1e-9
}

func eqStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func TestRunout_LevelsDOIAndOrder(t *testing.T) {
	e := newEnv(t)
	loc, want := runoutFixture(e)
	h := e.engine(opsPrincipal())

	var got dto.CCRunoutResponse
	getJSON(t, h, q("/api/v1/cc/runout", "location_id", loc.String()), &got)
	if got.WindowDays != 28 || !got.GeneratedAt.Equal(testNow) || got.NextCursor != nil {
		t.Fatalf("meta salah: window %d, generated %s, next %v", got.WindowDays, got.GeneratedAt, got.NextCursor)
	}
	var order []string
	for _, it := range got.Items {
		order = append(order, it.SKU)
		w, ok := want[it.SKU]
		if !ok {
			t.Errorf("SKU tak terduga %s", it.SKU)
			continue
		}
		if it.Level != w.level || it.SoldQty != w.sold || !eqFloat(it.AvgDailySales, w.avg) ||
			!eqFloat(it.DaysOfInventory, w.doi) || !eqStr(it.RunoutDate, w.runout) ||
			!slices.Equal(it.Reasons, w.reasons) || it.HasPolicy != w.policy {
			t.Errorf("%s: dapat level=%s sold=%d avg=%v doi=%v runout=%v reasons=%v policy=%v\n ingin %+v",
				it.SKU, it.Level, it.SoldQty, deref(it.AvgDailySales), deref(it.DaysOfInventory), derefS(it.RunoutDate),
				it.Reasons, it.HasPolicy, w)
		}
		if it.IsEstimate || it.LocationType != "OWN_STORE" || it.LocationCode != "TKO-UJI" {
			t.Errorf("%s: metadata lokasi salah: %+v", it.SKU, it)
		}
		if !w.policy && (it.LeadTimeDays != nil || it.MinQty != nil) {
			t.Errorf("%s: tanpa policy tapi parameter terisi", it.SKU)
		}
	}
	if !slices.Equal(order, runoutOrder) {
		t.Fatalf("urutan salah:\n dapat %v\n ingin %v", order, runoutOrder)
	}

	// Filter level + produk.
	var crit dto.CCRunoutResponse
	getJSON(t, h, q("/api/v1/cc/runout", "location_id", loc.String(), "level", "CRITICAL"), &crit)
	if len(crit.Items) != 4 {
		t.Fatalf("CRITICAL %d item, ingin 4", len(crit.Items))
	}
	var none dto.CCRunoutResponse
	getJSON(t, h, q("/api/v1/cc/runout", "location_id", loc.String(), "location_type", "HUB"), &none)
	if none.Items == nil || len(none.Items) != 0 || none.NextCursor != nil {
		t.Fatalf("filter kosong harus items [] dan next_cursor null: %+v", none)
	}
}

// TestRunout_KeysetPagesAcrossLevels: halaman berurutan (limit 3) melewati
// batas level dan DOI yang sama tanpa duplikat/lompatan.
func TestRunout_KeysetPagesAcrossLevels(t *testing.T) {
	e := newEnv(t)
	loc, _ := runoutFixture(e)
	h := e.engine(opsPrincipal())
	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		path := q("/api/v1/cc/runout", "location_id", loc.String(), "limit", "3")
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var got dto.CCRunoutResponse
		getJSON(t, h, path, &got)
		for _, it := range got.Items {
			seen = append(seen, it.SKU)
		}
		if got.NextCursor == nil {
			break
		}
		if len(got.Items) != 3 {
			t.Fatalf("halaman tengah %d item", len(got.Items))
		}
		cursor = *got.NextCursor
	}
	if !slices.Equal(seen, runoutOrder) {
		t.Fatalf("gabungan halaman salah:\n dapat %v\n ingin %v", seen, runoutOrder)
	}
}

// TestRunout_DemoSeed: data demo menampilkan setiap level (Kasus 4 = CRITICAL).
func TestRunout_DemoSeed(t *testing.T) {
	e := newEnv(t)
	var got dto.CCRunoutResponse
	getJSON(t, e.engine(opsPrincipal()), "/api/v1/cc/runout", &got)
	levels := map[string]string{}
	for _, it := range got.Items {
		levels[it.LocationCode+"/"+it.SKU] = it.Level
	}
	wantLevels := map[string]string{
		"TKO-JKS/DEMO-HRB-SCH10": "CRITICAL",
		"TKO-JKS/DEMO-TEH-BAG20": "WARNING",
		"TKO-BDG/DEMO-HRB-SCH10": "OK",
		"TKO-BDG/DEMO-TEH-BAG20": "INSUFFICIENT_DATA",
		"GDP-BKS/DEMO-HRB-SCH10": "INSUFFICIENT_DATA",
		"GDP-BKS/DEMO-TEH-BAG20": "INSUFFICIENT_DATA",
	}
	for k, w := range wantLevels {
		if levels[k] != w {
			t.Errorf("%s: level %q, ingin %q", k, levels[k], w)
		}
	}
	if len(got.Items) != len(wantLevels) {
		t.Errorf("%d item, ingin %d: %v", len(got.Items), len(wantLevels), levels)
	}
	if got.Items[0].LocationCode != "TKO-JKS" || got.Items[0].AvailableQty != 1 {
		t.Errorf("item pertama harus unit terakhir Toko Jakarta Selatan: %+v", got.Items[0])
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefS(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
