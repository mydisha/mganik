package commandcenter_test

import (
	"net/http"
	"regexp"
	"slices"
	"testing"
	"time"

	"mganik/api/internal/dto"
	"mganik/api/internal/pagination"
)

func TestStock_VisibilityFiltersAndKeyset(t *testing.T) {
	e := newEnv(t)
	// Stok di gudang mitra = estimasi.
	e.balance(partnerSulsel, sku1ID, 30, 0)
	e.exec(`UPDATE inventory_balances SET reserved_qty = 1, quarantine_qty = 2, physical_qty = physical_qty + 3
	         WHERE location_id = ? AND product_id = ?`, bandungID, sku1ID)
	h := e.engine(opsPrincipal())

	var page pagination.Page[dto.CCStockItem]
	getJSON(t, h, "/api/v1/cc/stock?limit=200", &page)
	var keys []string
	byKey := map[string]dto.CCStockItem{}
	for _, it := range page.Items {
		k := it.LocationCode + "/" + it.SKU
		keys = append(keys, k)
		byKey[k] = it
	}
	wantKeys := []string{
		"GDP-BKS/DEMO-HRB-SCH10", "GDP-BKS/DEMO-TEH-BAG20",
		"MTR-SULSEL/DEMO-HRB-SCH10",
		"TKO-BDG/DEMO-HRB-SCH10", "TKO-BDG/DEMO-TEH-BAG20",
		"TKO-JKS/DEMO-HRB-SCH10", "TKO-JKS/DEMO-TEH-BAG20",
	}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("urutan/isi stok salah:\n dapat %v\n ingin %v", keys, wantKeys)
	}
	if p := byKey["MTR-SULSEL/DEMO-HRB-SCH10"]; !p.IsEstimate || p.LocationType != "PARTNER_WH" || p.AvailableQty != 30 {
		t.Fatalf("stok mitra harus estimasi: %+v", p)
	}
	b := byKey["TKO-BDG/DEMO-HRB-SCH10"]
	if b.IsEstimate || b.PhysicalQty != 9 || b.ReservedQty != 1 || b.QuarantineQty != 2 || b.AvailableQty != 6 ||
		b.SyncState != "OFFLINE" || b.LocationType != "OWN_STORE" {
		t.Fatalf("bucket stok toko salah: %+v", b)
	}
	if j := byKey["TKO-JKS/DEMO-HRB-SCH10"]; j.AvailableQty != 1 {
		t.Fatalf("Kasus 4: stok Jakarta Selatan SKU1 %d, ingin 1", j.AvailableQty)
	}

	// Keyset: limit 3 → 3 + 3 + 1, sama dengan daftar penuh.
	var seen []string
	cursor := ""
	for range 5 {
		path := "/api/v1/cc/stock?limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var p pagination.Page[dto.CCStockItem]
		getJSON(t, h, path, &p)
		for _, it := range p.Items {
			seen = append(seen, it.LocationCode+"/"+it.SKU)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	if !slices.Equal(seen, wantKeys) {
		t.Fatalf("halaman stok salah: %v", seen)
	}

	// Filter.
	var f pagination.Page[dto.CCStockItem]
	getJSON(t, h, "/api/v1/cc/stock?location_type=OWN_STORE&product_id="+sku2ID.String(), &f)
	if len(f.Items) != 2 || f.Items[0].SKU != "DEMO-TEH-BAG20" || f.Items[1].SKU != "DEMO-TEH-BAG20" {
		t.Fatalf("filter tipe+produk salah: %+v", f.Items)
	}
	getJSON(t, h, "/api/v1/cc/stock?location_id="+centralID.String(), &f)
	if len(f.Items) != 2 || f.Items[0].LocationID != centralID {
		t.Fatalf("filter lokasi salah: %+v", f.Items)
	}
}

func TestSummary_Facts(t *testing.T) {
	e := newEnv(t)
	h := e.engine(opsPrincipal())

	var sum dto.CCSummaryResponse
	getJSON(t, h, "/api/v1/cc/summary", &sum)
	if !sum.GeneratedAt.Equal(testNow) || sum.Runout.WindowDays != 28 {
		t.Fatalf("meta salah: %+v", sum)
	}
	// Data demo: Jaksel SKU1 CRITICAL (habis 2026-11-08), Jaksel SKU2 WARNING,
	// 3 item tanpa riwayat, dua toko masih OFFLINE (belum heartbeat).
	r := sum.Runout
	if r.CriticalItems != 1 || r.CriticalLocations != 1 || r.WarningItems != 1 || r.WarningLocations != 1 ||
		r.InsufficientDataItems != 3 || r.EarliestRunoutDate == nil || *r.EarliestRunoutDate != "2026-11-08" {
		t.Fatalf("ringkasan runout salah: %+v (earliest %v)", r, derefS(r.EarliestRunoutDate))
	}
	if sum.Stores.Total != 2 || sum.Stores.Offline != 2 || sum.Stock.OutOfStockItems != 0 ||
		sum.Conflicts.OpenTotal != 0 || sum.Anomalies.OpenTotal != 0 || sum.Anomalies.TopParty != nil {
		t.Fatalf("ringkasan awal salah: %+v", sum)
	}
	for _, sev := range []string{"LOW", "MEDIUM", "HIGH", "CRITICAL"} {
		if v, ok := sum.Anomalies.OpenBySeverity[sev]; !ok || v != 0 {
			t.Fatalf("severity %s harus ada dan 0: %v", sev, sum.Anomalies.OpenBySeverity)
		}
	}

	// Stok habis, konflik, anomali, toko online.
	e.exec(`UPDATE inventory_balances SET physical_qty = 0 WHERE location_id = ? AND product_id = ?`, bandungID, sku2ID)
	e.balance(partnerSulsel, sku1ID, 0, 0) // stok mitra 0 tidak dihitung "habis" (estimasi)
	e.conflict(jakselID, sku1ID, "OPEN", testNow)
	e.conflict(jakselID, sku1ID, "RESOLVED", testNow)
	e.anomaly(anomalyFx{severity: "HIGH", partyID: &sulselPartyID})
	e.anomaly(anomalyFx{severity: "MEDIUM", partyID: &sulselPartyID})
	e.anomaly(anomalyFx{severity: "CRITICAL", partyID: &jatimPartyID})
	e.anomaly(anomalyFx{severity: "LOW", status: "RESOLVED", partyID: &jatimPartyID})
	e.exec(`UPDATE locations SET sync_state = 'ONLINE', last_heartbeat_at = ? WHERE id = ?`, testNow.Add(-30*time.Second), jakselID)

	getJSON(t, h, "/api/v1/cc/summary", &sum)
	a := sum.Anomalies
	if sum.Stock.OutOfStockItems != 1 || sum.Stock.OutOfStockLocations != 1 || sum.Conflicts.OpenTotal != 1 ||
		sum.Stores.Offline != 1 || a.OpenTotal != 3 || a.OpenBySeverity["HIGH"] != 1 || a.OpenBySeverity["MEDIUM"] != 1 ||
		a.OpenBySeverity["CRITICAL"] != 1 || a.OpenBySeverity["LOW"] != 0 {
		t.Fatalf("ringkasan salah: %+v", sum)
	}
	if a.TopParty == nil || a.TopParty.PartyID != sulselPartyID || a.TopParty.OpenCount != 2 {
		t.Fatalf("top party salah: %+v", a.TopParty)
	}
}

// TestSummary_RunoutOwnLocationsOnly: stok mitra yang kritis tampil di
// /cc/runout (ditandai estimasi) tetapi tidak ikut kalimat ringkasan.
func TestSummary_RunoutOwnLocationsOnly(t *testing.T) {
	e := newEnv(t)
	e.balance(partnerSulsel, sku2ID, 1, 0)
	e.policy(partnerSulsel, sku2ID, 1, 3, 2)
	e.dailySales(partnerSulsel, sku2ID, "DIRECT_SALE", 2, 28)
	h := e.engine(opsPrincipal())

	var runout dto.CCRunoutResponse
	getJSON(t, h, "/api/v1/cc/runout?level=CRITICAL", &runout)
	partnerSeen := false
	for _, it := range runout.Items {
		if it.LocationID == partnerSulsel {
			partnerSeen = it.IsEstimate
		}
	}
	if !partnerSeen || len(runout.Items) != 2 {
		t.Fatalf("runout mitra harus tampil sebagai estimasi: %+v", runout.Items)
	}
	var sum dto.CCSummaryResponse
	getJSON(t, h, "/api/v1/cc/summary", &sum)
	if sum.Runout.CriticalItems != 1 || sum.Runout.CriticalLocations != 1 {
		t.Fatalf("ringkasan tidak boleh menghitung lokasi mitra: %+v", sum.Runout)
	}
}

// TestResponses_TimestampsInUTC: proses berjalan di WIB (TestMain), semua
// timestamp respons tetap berakhiran Z.
func TestResponses_TimestampsInUTC(t *testing.T) {
	e := newEnv(t)
	// Prasyarat: driver mengembalikan timestamptz dalam zona proses (WIB), jadi
	// tanpa konversi eksplisit respons akan berisi +07:00.
	var probe time.Time
	if err := e.db.Raw(`SELECT CAST(? AS timestamptz)`, testNow).Row().Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if _, off := probe.Zone(); off == 0 {
		t.Fatalf("prasyarat test tidak terpenuhi: driver sudah mengembalikan UTC (%s)", probe)
	}
	e.exec(`UPDATE locations SET last_heartbeat_at = ? WHERE id = ?`, testNow, jakselID)
	e.conflict(jakselID, sku1ID, "RESOLVED", testNow.Add(-time.Hour))
	id := e.anomaly(anomalyFx{partyID: &jatimPartyID})
	e.scans(e.distributorUnit(jatimPartyID), 1, "CONSUMER_VERIFY", ptr("35.78"), testNow.Add(-time.Hour))
	h := e.engine(opsPrincipal())
	decodeAnomaly(t, patchAnomaly(t, e, id, map[string]any{"status": "RESOLVED", "note": "selesai"}))

	timestamp := regexp.MustCompile(`"\d{4}-\d{2}-\d{2}T[0-9:.]+([^"]*)"`)
	for _, p := range append(ccGetPaths, "/api/v1/cc/stock?location_id="+jakselID.String()) {
		r := call(t, h, http.MethodGet, p, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %d", p, r.Code)
		}
		found := 0
		for _, m := range timestamp.FindAllSubmatch(r.Body, -1) {
			found++
			if string(m[1]) != "Z" {
				t.Fatalf("%s: timestamp bukan UTC: %s", p, m[0])
			}
		}
		if found == 0 && p != "/api/v1/cc/runout" {
			t.Fatalf("%s: tidak ada timestamp yang diperiksa: %s", p, r.Body)
		}
	}
}
