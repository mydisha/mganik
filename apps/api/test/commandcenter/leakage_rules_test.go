package commandcenter_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/dto"
)

// unitEvent mencatat event serial ber-party (mis. diterima distributor).
func (e *env) unitEvent(unit, party uuid.UUID, at time.Time) {
	e.t.Helper()
	e.exec(`INSERT INTO unit_events (id, serial_unit_id, biz_step, party_id, actor_type, occurred_at)
	        VALUES (?, ?, 'receiving', ?, 'SYSTEM', ?)`, newID(), unit, party, at)
}

func leakageOf(t *testing.T, e *env, party uuid.UUID) dto.CCLeakageItem {
	t.Helper()
	var got dto.CCLeakageResponse
	getJSON(t, e.engine(opsPrincipal()), "/api/v1/cc/distribution-leakage?party_id="+party.String(), &got)
	if len(got.Items) != 1 {
		t.Fatalf("party %s: %d item", party, len(got.Items))
	}
	return got.Items[0]
}

// TestLeakage_CustodianAtScanTime: unit milik A sejak −20 hari dipindah sah ke
// B sejak −2 hari. Scan di wilayah A pada −10 hari dihitung untuk A (dalam
// wilayah), bukan menjadi kebocoran B.
func TestLeakage_CustodianAtScanTime(t *testing.T) {
	e := newEnv(t)
	a := e.distributor("Distributor A", "SELECTED", "35.78")
	b := e.distributor("Distributor B", "SELECTED", "73.71")
	u := e.distributorUnit(b) // custodian sekarang = B
	e.unitEvent(u, a, testNow.AddDate(0, 0, -20))
	e.unitEvent(u, b, testNow.AddDate(0, 0, -2))
	e.scans(u, 25, "CONSUMER_VERIFY", ptr("35.78"), testNow.AddDate(0, 0, -10))
	e.scans(u, 3, "CONSUMER_VERIFY", ptr("35.78"), testNow.AddDate(0, 0, -1)) // sudah milik B: luar wilayah B

	ia := leakageOf(t, e, a)
	if ia.TotalScans != 25 || !eqInt(ia.OutOfTerritory, ptr(int64(0))) || !eqRatio(ia.LeakRatio, ptr(0.0)) || ia.Alert {
		t.Fatalf("A salah: %+v", ia)
	}
	ib := leakageOf(t, e, b)
	if ib.TotalScans != 3 || !eqInt(ib.OutOfTerritory, ptr(int64(3))) || ib.Alert || ib.AboveBaseline || !ib.SampleTooSmall {
		t.Fatalf("B tidak boleh alert karena scan lama milik A: %+v", ib)
	}
}

// TestLeakage_CoarseRegionAndRounding: scan berwilayah provinsi (35) terhadap
// wilayah resmi kota (35.78) tidak bisa dinilai, bukan luar wilayah; provinsi
// lain (73) tetap luar wilayah. above_baseline memakai rasio yang ditampilkan.
func TestLeakage_CoarseRegionAndRounding(t *testing.T) {
	e := newEnv(t)
	d := e.distributor("Distributor Kota", "SELECTED", "35.78")
	u := e.distributorUnit(d)
	recent := testNow.Add(-time.Hour)
	e.scans(u, 5, "CONSUMER_VERIFY", ptr("35"), recent)
	e.scans(u, 2, "CONSUMER_VERIFY", ptr("35.78"), recent)
	e.scans(u, 1, "CONSUMER_VERIFY", ptr("73"), recent)
	e.scans(u, 1, "CONSUMER_VERIFY", nil, recent)

	// Baseline sama dengan rasio yang ditampilkan (1/3 → 0.3333): tidak di atas baseline.
	e.exec(`UPDATE anomaly_rules SET params = params || '{"leak_index_min_scans": 1, "leak_index_baseline_ratio": 0.3333}'
	         WHERE code = 'OUT_OF_TERRITORY'`)
	it := leakageOf(t, e, d)
	if it.TotalScans != 9 || it.LocatedScans != 8 || it.UnlocatedScans != 1 || it.JudgedScans != 3 ||
		it.UnjudgedScans != 5 || !eqInt(it.OutOfTerritory, ptr(int64(1))) || it.LeakRatio == nil || *it.LeakRatio != 0.3333 {
		t.Fatalf("penilaian wilayah kasar salah: %+v (ratio %v)", it, deref(it.LeakRatio))
	}
	if it.AboveBaseline || it.Alert || it.SampleTooSmall {
		t.Fatalf("rasio tampil 0.3333 = baseline 0.3333 tidak boleh di atas baseline: %+v", it)
	}
	e.exec(`UPDATE anomaly_rules SET params = params || '{"leak_index_baseline_ratio": 0.3332}' WHERE code = 'OUT_OF_TERRITORY'`)
	if it = leakageOf(t, e, d); !it.AboveBaseline || !it.Alert {
		t.Fatalf("0.3333 > 0.3332 harus di atas baseline: %+v", it)
	}
}

// TestLeakage_RoleEndedMidWindow: role distributor berakhir 5 hari lalu; ia
// tetap tampil, hanya scan selama role aktif yang dihitung.
func TestLeakage_RoleEndedMidWindow(t *testing.T) {
	e := newEnv(t)
	d := e.distributor("Distributor Berhenti", "SELECTED", "35.78")
	e.exec(`UPDATE party_roles SET valid_to = ? WHERE party_id = ?`, testNow.AddDate(0, 0, -5), d)
	u := e.distributorUnit(d)
	e.scans(u, 4, "CONSUMER_VERIFY", ptr("31.74"), testNow.AddDate(0, 0, -10))
	e.scans(u, 6, "CONSUMER_VERIFY", ptr("31.74"), testNow.AddDate(0, 0, -2))
	it := leakageOf(t, e, d)
	if it.TotalScans != 4 || !eqInt(it.OutOfTerritory, ptr(int64(4))) {
		t.Fatalf("hanya scan saat role aktif yang dihitung: %+v", it)
	}
	// Role yang berakhir sebelum jendela: tidak tampil.
	var got dto.CCLeakageResponse
	getJSON(t, e.engine(opsPrincipal()), "/api/v1/cc/distribution-leakage?days=3&party_id="+d.String(), &got)
	if len(got.Items) != 0 {
		t.Fatalf("role berakhir sebelum jendela 3 hari tetap tampil: %+v", got.Items)
	}
}
