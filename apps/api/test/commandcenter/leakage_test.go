package commandcenter_test

import (
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/dto"
	"mganik/api/internal/seed"
)

var unitSeq int

// distributorUnit membuat satu unit berserial dengan custodian party.
func (e *env) distributorUnit(party uuid.UUID) uuid.UUID {
	e.t.Helper()
	unitSeq++
	id := newID()
	e.exec(`INSERT INTO serial_units (id, code, level_code, product_id, batch_id, status, custodian_party_id, commissioned_at)
	        VALUES (?, ?, 'UNIT', ?, ?, 'AT_PARTNER', ?, ?)`,
		id, fmt.Sprintf("9%015d", unitSeq), sku1ID, seed.BatchID(), party, testNow.Add(-90*24*time.Hour))
	return id
}

// scans mencatat n scan unit pada waktu at. region nil = lokasi tidak diketahui.
func (e *env) scans(unit uuid.UUID, n int, context string, region *string, at time.Time) {
	e.t.Helper()
	if region == nil {
		e.exec(`INSERT INTO scan_events (id, raw_code, serial_unit_id, context, geo_source, result, occurred_at)
		        SELECT gen_random_uuid(), 'KODE-UJI', ?, ?, 'NONE', 'GENUINE_REPEAT', ? FROM generate_series(1, ?)`,
			unit, context, at, n)
		return
	}
	e.exec(`INSERT INTO scan_events (id, raw_code, serial_unit_id, context, geo_source, lat, lng, region_code, result, occurred_at)
	        SELECT gen_random_uuid(), 'KODE-UJI', ?, ?, 'GPS', -6.2, 106.8, ?, 'GENUINE_REPEAT', ? FROM generate_series(1, ?)`,
		unit, context, *region, at, n)
}

// distributor membuat party berperan DISTRIBUTOR dengan cakupan opsional.
// coverage "" = tanpa wilayah resmi; regions dipakai untuk SELECTED.
func (e *env) distributor(name, coverage string, regions ...string) uuid.UUID {
	e.t.Helper()
	id := newID()
	from := testNow.AddDate(-1, 0, 0)
	e.exec(`INSERT INTO parties (id, kind, name) VALUES (?, 'ORG', ?)`, id, name)
	e.exec(`INSERT INTO party_roles (id, party_id, role_code, valid_from) VALUES (?, ?, 'DISTRIBUTOR', ?)`, newID(), id, from)
	switch coverage {
	case "NATIONAL":
		e.exec(`INSERT INTO partner_territories (id, party_id, coverage_mode, valid_from) VALUES (?, ?, 'NATIONAL', ?)`, newID(), id, from)
	case "SELECTED":
		ter := newID()
		e.exec(`INSERT INTO territories (id, code, name) VALUES (?, ?, ?)`, ter, "TER-"+ter.String()[24:], "Wilayah "+name)
		for _, r := range regions {
			e.exec(`INSERT INTO territory_regions (territory_id, region_code) VALUES (?, ?)`, ter, r)
		}
		e.exec(`INSERT INTO partner_territories (id, party_id, territory_id, coverage_mode, valid_from) VALUES (?, ?, ?, 'SELECTED', ?)`,
			newID(), id, ter, from)
	}
	return id
}

type leakWant struct {
	coverage                  *string
	total, located, unlocated int64
	out                       *int64
	ratio                     *float64
	small, above, alert       bool
	testBuy, testBuyHits      int64
}

func TestLeakage_IndexPerDistributor(t *testing.T) {
	e := newEnv(t)
	recent := testNow.Add(-2 * time.Hour)
	jkt, sby, sda, mks, bdg := ptr("31.74"), ptr("35.78"), ptr("35.15"), ptr("73.71"), ptr("32.73")

	// Distributor Jatim (seed, SELECTED Surabaya/Sidoarjo/Malang): bocor ke Jakarta.
	uj := e.distributorUnit(jatimPartyID)
	e.scans(uj, 20, "CONSUMER_VERIFY", sby, recent)
	e.scans(uj, 1, "LOYALTY_CLAIM", sda, recent)
	e.scans(uj, 3, "CONSUMER_VERIFY", jkt, recent)
	e.scans(uj, 1, "TEST_BUY", jkt, recent)
	e.scans(uj, 2, "CONSUMER_VERIFY", nil, recent)                     // tanpa wilayah: tidak dinilai
	e.scans(uj, 4, "POS_CHECKOUT", jkt, recent)                        // bukan sinyal konsumen
	e.scans(uj, 5, "CONSUMER_VERIFY", jkt, testNow.AddDate(0, 0, -40)) // di luar jendela 30 hari

	// Distributor Sulsel (seed): 5 scan, semuanya luar wilayah → sampel kecil.
	us := e.distributorUnit(sulselPartyID)
	e.scans(us, 5, "CONSUMER_VERIFY", jkt, testNow.Add(-3*24*time.Hour))

	// NATIONAL: tidak pernah di luar wilayah.
	nas := e.distributor("Distributor Nasional", "NATIONAL")
	un := e.distributorUnit(nas)
	e.scans(un, 22, "CONSUMER_VERIFY", jkt, recent)
	e.scans(un, 3, "CONSUMER_VERIFY", mks, recent)

	// Wilayah tingkat provinsi (32 = Jawa Barat) mencakup kota di bawahnya.
	prov := e.distributor("Distributor Jabar", "SELECTED", "32")
	up := e.distributorUnit(prov)
	e.scans(up, 18, "CONSUMER_VERIFY", bdg, recent)
	e.scans(up, 2, "CONSUMER_VERIFY", jkt, recent)

	// Tanpa wilayah resmi: tidak dinilai, bukan dianggap nasional.
	noter := e.distributor("Distributor Tanpa Wilayah", "")
	e.scans(e.distributorUnit(noter), 3, "CONSUMER_VERIFY", jkt, recent)

	// Satu hit TEST_BUY langsung memicu alert walau sampel kecil.
	tb := e.distributor("Distributor Test Buy", "SELECTED", "73.71")
	e.scans(e.distributorUnit(tb), 1, "TEST_BUY", jkt, recent)

	// Unit milik pemilik brand (bukan distributor) tidak ikut.
	e.scans(seed.DemoSerialID("UNIT", 0), 10, "CONSUMER_VERIFY", jkt, recent)

	h := e.engine(opsPrincipal())
	var got dto.CCLeakageResponse
	getJSON(t, h, "/api/v1/cc/distribution-leakage", &got)
	if got.WindowDays != 30 || got.MinScans != 20 || got.BaselineRatio != 0.05 ||
		!got.To.Equal(testNow) || !got.From.Equal(testNow.AddDate(0, 0, -30)) {
		t.Fatalf("meta salah: %+v", got)
	}

	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }
	sel, nat := ptr("SELECTED"), ptr("NATIONAL")
	want := map[uuid.UUID]leakWant{
		tb:            {coverage: sel, total: 1, located: 1, out: i(1), ratio: f(1), small: true, alert: true, testBuy: 1, testBuyHits: 1},
		jatimPartyID:  {coverage: sel, total: 27, located: 25, unlocated: 2, out: i(4), ratio: f(0.16), above: true, alert: true, testBuy: 1, testBuyHits: 1},
		prov:          {coverage: sel, total: 20, located: 20, out: i(2), ratio: f(0.1), above: true, alert: true},
		sulselPartyID: {coverage: sel, total: 5, located: 5, out: i(5), ratio: f(1), small: true},
		nas:           {coverage: nat, total: 25, located: 25, out: i(0), ratio: f(0)},
		noter:         {total: 3, located: 3, small: true},
	}
	wantOrder := []uuid.UUID{tb, jatimPartyID, prov, sulselPartyID, nas, noter}
	var order []uuid.UUID
	for _, it := range got.Items {
		order = append(order, it.PartyID)
		w, ok := want[it.PartyID]
		if !ok {
			t.Errorf("party tak terduga: %s", it.PartyName)
			continue
		}
		if !eqStr(it.CoverageMode, w.coverage) || it.TotalScans != w.total || it.LocatedScans != w.located ||
			it.UnlocatedScans != w.unlocated || !eqInt(it.OutOfTerritory, w.out) || !eqRatio(it.LeakRatio, w.ratio) ||
			it.SampleTooSmall != w.small || it.AboveBaseline != w.above || it.Alert != w.alert ||
			it.TestBuyScans != w.testBuy || it.TestBuyHits != w.testBuyHits {
			t.Errorf("%s: dapat %+v (ratio %v, out %v)\n ingin %+v", it.PartyName, it, deref(it.LeakRatio), derefI(it.OutOfTerritory), w)
		}
	}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("urutan salah (alert dulu, rasio tertinggi):\n dapat %v\n ingin %v", order, wantOrder)
	}

	// Keyset limit 2 → 3 halaman, urutan sama.
	var seen []uuid.UUID
	cursor := ""
	for range 5 {
		path := "/api/v1/cc/distribution-leakage?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var p dto.CCLeakageResponse
		getJSON(t, h, path, &p)
		for _, it := range p.Items {
			seen = append(seen, it.PartyID)
		}
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	if !slices.Equal(seen, wantOrder) {
		t.Fatalf("halaman salah: %v", seen)
	}

	// Jendela 2 hari: scan Sulsel (3 hari lalu) keluar jendela.
	var short dto.CCLeakageResponse
	getJSON(t, h, "/api/v1/cc/distribution-leakage?days=2&party_id="+sulselPartyID.String(), &short)
	if len(short.Items) != 1 || short.Items[0].TotalScans != 0 || short.Items[0].LeakRatio != nil || short.Items[0].OutOfTerritory != nil {
		t.Fatalf("jendela 2 hari salah: %+v", short.Items)
	}

	// Ambang dari anomaly_rules: min 30 → Jatim jadi sampel kecil, hanya alert karena TEST_BUY.
	e.exec(`UPDATE anomaly_rules SET params = jsonb_set(params, '{leak_index_min_scans}', '30') WHERE code = 'OUT_OF_TERRITORY'`)
	var jt dto.CCLeakageResponse
	getJSON(t, h, "/api/v1/cc/distribution-leakage?party_id="+jatimPartyID.String(), &jt)
	if jt.MinScans != 30 || len(jt.Items) != 1 || !jt.Items[0].SampleTooSmall || jt.Items[0].AboveBaseline || !jt.Items[0].Alert {
		t.Fatalf("ambang dari aturan tidak dipakai: %+v", jt)
	}
	// Prov tanpa TEST_BUY: sampel kecil → tidak alert.
	getJSON(t, h, "/api/v1/cc/distribution-leakage?party_id="+prov.String(), &jt)
	if jt.Items[0].Alert || !jt.Items[0].SampleTooSmall {
		t.Fatalf("sampel kecil tidak boleh alert tanpa test-buy: %+v", jt.Items[0])
	}
}

// TestLeakage_TerritoryValidAtScanTime: wilayah resmi yang sudah berakhir
// tidak dipakai menilai scan setelahnya.
func TestLeakage_TerritoryValidAtScanTime(t *testing.T) {
	e := newEnv(t)
	d := e.distributor("Distributor Pindah", "SELECTED", "35.78")
	// Wilayah lama berakhir 10 hari lalu; wilayah baru (Jakarta Barat) berlaku sejak itu.
	e.exec(`UPDATE partner_territories SET valid_to = ? WHERE party_id = ?`, testNow.AddDate(0, 0, -10), d)
	ter := newID()
	e.exec(`INSERT INTO territories (id, code, name) VALUES (?, 'TER-BARU', 'Jakarta Barat')`, ter)
	e.exec(`INSERT INTO territory_regions (territory_id, region_code) VALUES (?, '31.74')`, ter)
	e.exec(`INSERT INTO partner_territories (id, party_id, territory_id, coverage_mode, valid_from) VALUES (?, ?, ?, 'SELECTED', ?)`,
		newID(), d, ter, testNow.AddDate(0, 0, -10))
	u := e.distributorUnit(d)
	e.scans(u, 4, "CONSUMER_VERIFY", ptr("35.78"), testNow.AddDate(0, 0, -20)) // dalam wilayah lama
	e.scans(u, 3, "CONSUMER_VERIFY", ptr("35.78"), testNow.AddDate(0, 0, -5))  // luar wilayah baru
	e.scans(u, 2, "CONSUMER_VERIFY", ptr("31.74"), testNow.AddDate(0, 0, -5))  // dalam wilayah baru

	var got dto.CCLeakageResponse
	getJSON(t, e.engine(opsPrincipal()), "/api/v1/cc/distribution-leakage?party_id="+d.String(), &got)
	if len(got.Items) != 1 {
		t.Fatalf("%d item", len(got.Items))
	}
	it := got.Items[0]
	if it.TotalScans != 9 || !eqInt(it.OutOfTerritory, ptr(int64(3))) || !eqRatio(it.LeakRatio, ptr(3.0/9.0)) {
		t.Fatalf("penilaian per waktu scan salah: %+v (out %v ratio %v)", it, derefI(it.OutOfTerritory), deref(it.LeakRatio))
	}
}

func eqInt(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// eqRatio: rasio dibulatkan 4 desimal di respons.
func eqRatio(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return math.Abs(*a-*b) < 1e-4
}

func derefI(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
