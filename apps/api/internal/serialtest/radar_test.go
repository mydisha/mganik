package serialtest

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

func (f *fx) scan(code, anon string, gps *domain.GeoPoint) service.VerifyResult {
	f.t.Helper()
	res, err := f.verify.Verify(f.ctx, service.VerifyInput{RawCode: code, GPS: gps, AnonID: anon, ClientIP: "203.0.113.10"})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *fx) anomaly(rule, dedupe string) (model.Anomaly, bool) {
	f.t.Helper()
	var rows []model.Anomaly
	if err := f.db.Where("rule_code = ? AND dedupe_key = ?", rule, dedupe).Find(&rows).Error; err != nil {
		f.t.Fatal(err)
	}
	if len(rows) == 0 {
		return model.Anomaly{}, false
	}
	return rows[0], true
}

func (f *fx) serialID(code string) string {
	f.t.Helper()
	var id string
	if err := f.db.Raw(`SELECT id::text FROM serial_units WHERE code = ?`, code).Scan(&id).Error; err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestVerify_GenuineFirstThenRepeat(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	code := unit(0).Code

	first := f.scan(code, "anon-1", &geoMakassar)
	if first.Result != domain.ResultGenuineFirst || first.FirstVerifiedCity == nil || *first.FirstVerifiedCity != "Kota Makassar" ||
		first.ProductName == nil || first.FirstVerifiedAt == nil || !first.FirstVerifiedAt.Equal(t0) {
		t.Fatalf("scan pertama %+v", first)
	}
	if len(first.RuleHits) != 0 {
		t.Fatalf("scan di wilayah sendiri tidak boleh kena aturan: %v", first.RuleHits)
	}
	var se model.ScanEvent
	if err := f.db.Where("raw_code = ?", code).Take(&se).Error; err != nil {
		t.Fatal(err)
	}
	if se.GeoSource != domain.GeoGPS || se.RegionCode == nil || *se.RegionCode != "73.71" ||
		len(se.ActorFingerprint) != 32 || len(se.IPHash) != 32 || se.Result != domain.ResultGenuineFirst {
		t.Fatalf("scan_event %+v", se)
	}

	f.clk.Advance(time.Hour)
	repeat := f.scan(code, "anon-2", &geoGowa)
	if repeat.Result != domain.ResultGenuineRepeat || repeat.FirstVerifiedAt == nil || !repeat.FirstVerifiedAt.Equal(t0) ||
		*repeat.FirstVerifiedCity != "Kota Makassar" {
		t.Fatalf("scan ulang %+v", repeat)
	}
	if n := f.count(`SELECT verify_count FROM serial_units WHERE code = ?`, code); n != 2 {
		t.Fatalf("verify_count %d", n)
	}
}

// OUT_OF_TERRITORY: anomali internal untuk distributor, konsumen tetap GENUINE.
func TestVerify_OutOfTerritoryIsInternalOnly(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	code := unit(1).Code
	dedupe := "OUT_OF_TERRITORY:" + f.serialID(code) + ":31.74"

	res := f.scan(code, "anon-1", &geoJakBar)
	if res.Result != domain.ResultGenuineFirst {
		t.Fatalf("konsumen harus tetap GENUINE, dapat %s", res.Result)
	}
	if !slices.Contains(res.RuleHits, domain.RuleOutOfTerritory) {
		t.Fatalf("rule_hits %v", res.RuleHits)
	}
	a, ok := f.anomaly(domain.RuleOutOfTerritory, dedupe)
	if !ok || a.PartyID == nil || *a.PartyID != partySul || a.HitCount != 1 || a.RegionCode == nil || *a.RegionCode != "31.74" {
		t.Fatalf("anomali %+v %v", a, ok)
	}
	// Dedupe: scan kedua dari wilayah yang sama menaikkan hit_count.
	f.clk.Advance(48 * time.Hour)
	f.scan(code, "anon-2", &geoJakBar)
	a2, _ := f.anomaly(domain.RuleOutOfTerritory, dedupe)
	if a2.HitCount != 2 || !a2.LastSeenAt.After(a.LastSeenAt) || a2.ID != a.ID {
		t.Fatalf("dedupe gagal: %+v", a2)
	}
	if n := f.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'OUT_OF_TERRITORY'`); n != 1 {
		t.Fatalf("anomali luar wilayah %d baris, ingin 1", n)
	}
	// Negatif: unit lain dipindai di wilayah resmi (Makassar).
	f.scan(unit(2).Code, "anon-3", &geoMakassar)
	if _, ok := f.anomaly(domain.RuleOutOfTerritory, "OUT_OF_TERRITORY:"+f.serialID(unit(2).Code)+":73.71"); ok {
		t.Fatal("scan di wilayah resmi tidak boleh membuat anomali")
	}
	// Titik jauh dari centroid mana pun: wilayah tidak diketahui, aturan dilewati.
	far := f.scan(unit(3).Code, "anon-4", &geoMidOcean)
	if slices.Contains(far.RuleHits, domain.RuleOutOfTerritory) {
		t.Fatal("wilayah tidak diketahui tidak boleh dianggap luar wilayah")
	}
}

// Custodian NATIONAL (pemilik brand) tidak pernah luar wilayah.
func TestVerify_NationalCoverageNeverOutOfTerritory(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locJaksel, nil) // toko sendiri: IN_STOCK, custodian perusahaan
	res := f.scan(unit(0).Code, "anon-1", &geoMedan)
	if res.Result != domain.ResultGenuineFirst || len(res.RuleHits) != 0 {
		t.Fatalf("hasil %+v", res)
	}
}

// Impossible travel: GPS→GPS 257 km dalam 10 menit kena (ambang 100 km),
// GPS→IP jarak yang sama tidak kena (ambang 500 km), GPS→IP 2000 km kena.
func TestVerify_ImpossibleTravel_GPSvsIP(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locJatim, nil)

	a := unit(0).Code
	f.scan(a, "anon-a1", &geoSurabaya)
	f.clk.Advance(10 * time.Minute)
	res := f.scan(a, "anon-a2", &geoSemarang)
	if res.Result != domain.ResultSuspicious || !slices.Contains(res.RuleHits, domain.RuleImpossibleTravel) {
		t.Fatalf("GPS-GPS 257 km/10 menit: %+v", res)
	}
	an, ok := f.anomaly(domain.RuleImpossibleTravel, "IMPOSSIBLE_TRAVEL:"+f.serialID(a))
	if !ok || an.Severity != "HIGH" {
		t.Fatalf("anomali %+v", an)
	}

	b := unit(1).Code
	f.scan(b, "anon-b1", &geoSurabaya)
	f.clk.Advance(10 * time.Minute)
	resIP, err := f.verify.Verify(f.ctx, service.VerifyInput{RawCode: b, AnonID: "anon-b2", ClientIP: "203.0.113.20", IPGeoOverride: &geoSemarang})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(resIP.RuleHits, domain.RuleImpossibleTravel) || resIP.Result == domain.ResultSuspicious {
		t.Fatalf("GPS-IP 257 km tidak boleh kena: %+v", resIP)
	}
	var src string
	if err := f.db.Raw(`SELECT geo_source FROM scan_events WHERE raw_code = ? ORDER BY occurred_at DESC LIMIT 1`, b).Scan(&src).Error; err != nil || src != domain.GeoIP {
		t.Fatalf("geo_source %q", src)
	}

	c := unit(2).Code
	f.scan(c, "anon-c1", &geoSurabaya)
	f.clk.Advance(10 * time.Minute)
	resFar, err := f.verify.Verify(f.ctx, service.VerifyInput{RawCode: c, AnonID: "anon-c2", ClientIP: "203.0.113.30", IPGeoOverride: &geoMedan})
	if err != nil {
		t.Fatal(err)
	}
	if resFar.Result != domain.ResultSuspicious || !slices.Contains(resFar.RuleHits, domain.RuleImpossibleTravel) {
		t.Fatalf("GPS-IP 2000 km/10 menit harus kena: %+v", resFar)
	}

	// Skenario QA (grief): SATU perangkat memalsukan GPS jauh (Surabaya →
	// Medan 10 menit). Anomali tetap dicatat untuk review, tetapi konsumen
	// tidak melihat SUSPICIOUS karena tidak ada perangkat kedua.
	e := unit(4).Code
	f.scan(e, "satu-hp", &geoSurabaya)
	f.clk.Advance(10 * time.Minute)
	spoof := f.scan(e, "satu-hp", &geoMedan)
	if spoof.Result == domain.ResultSuspicious || !slices.Contains(spoof.RuleHits, domain.RuleImpossibleTravel) {
		t.Fatalf("GPS palsu satu perangkat: %+v", spoof)
	}
	if a, ok := f.anomaly(domain.RuleImpossibleTravel, "IMPOSSIBLE_TRAVEL:"+f.serialID(e)); !ok ||
		f.count(`SELECT count(*) FROM anomalies WHERE id = ? AND evidence->>'different_device' = 'false'`, a.ID) != 1 {
		t.Fatalf("anomali internal perangkat sama tidak tercatat: %+v", a)
	}

	// Perjalanan wajar (Surabaya → Semarang dalam 6 jam) tidak kena.
	d := unit(3).Code
	f.scan(d, "anon-d1", &geoSurabaya)
	f.clk.Advance(6 * time.Hour)
	if r := f.scan(d, "anon-d2", &geoSemarang); slices.Contains(r.RuleHits, domain.RuleImpossibleTravel) {
		t.Fatalf("perjalanan wajar kena: %+v", r)
	}

	// Clock skew: scan berikutnya tercatat 6 jam LEBIH AWAL (jam server
	// mundur). Selisih negatif memakai nilai absolut, bukan kecepatan tak hingga.
	g := unit(6).Code
	f.scan(g, "anon-g1", &geoSurabaya)
	f.clk.Advance(-6 * time.Hour)
	if r := f.scan(g, "anon-g2", &geoSemarang); slices.Contains(r.RuleHits, domain.RuleImpossibleTravel) {
		t.Fatalf("selisih waktu negatif dianggap teleportasi: %+v", r)
	}
}

func (f *fx) scanFrom(code, anon, ip string, gps *domain.GeoPoint) service.VerifyResult {
	f.t.Helper()
	res, err := f.verify.Verify(f.ctx, service.VerifyInput{RawCode: code, GPS: gps, AnonID: anon, ClientIP: ip})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// netIP: alamat di /24 berbeda (hash IP memakai prefix /24).
func netIP(i int) string { return fmt.Sprintf("198.18.%d.7", i) }

func TestVerify_ScanVelocity(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)

	// Skenario QA (grief): satu orang, satu jaringan, 6 cookie baru → unit
	// asli TIDAK boleh menjadi SUSPICIOUS.
	grief := unit(5).Code
	for i := 1; i <= 6; i++ {
		if r := f.scanFrom(grief, fmt.Sprintf("cookie-baru-%d", i), "203.0.113.10", nil); r.Result == domain.ResultSuspicious ||
			slices.Contains(r.RuleHits, domain.RuleScanVelocity) {
			t.Fatalf("cookie ke-%d dari satu IP memicu velocity: %+v", i, r)
		}
	}

	// Kloning sungguhan: 6 perangkat dari 6 jaringan berbeda.
	code := unit(0).Code
	for i := 1; i <= 5; i++ {
		if r := f.scanFrom(code, fmt.Sprintf("anon-%d", i), netIP(i), nil); r.Result == domain.ResultSuspicious {
			t.Fatalf("fingerprint ke-%d sudah SUSPICIOUS", i)
		}
	}
	sixth := f.scanFrom(code, "anon-6", netIP(6), nil)
	if sixth.Result != domain.ResultSuspicious || !slices.Contains(sixth.RuleHits, domain.RuleScanVelocity) {
		t.Fatalf("fingerprint & jaringan ke-6 (> 5): %+v", sixth)
	}
	// Di luar jendela 24 jam hitungan mulai lagi.
	f.clk.Advance(25 * time.Hour)
	if r := f.scanFrom(code, "anon-7", netIP(7), nil); slices.Contains(r.RuleHits, domain.RuleScanVelocity) {
		t.Fatalf("di luar jendela masih kena: %+v", r)
	}
	// Satu perangkat memindai berulang kali tidak dihitung sebagai banyak pemindai.
	other := unit(1).Code
	for range 10 {
		if r := f.scan(other, "anon-sama", nil); slices.Contains(r.RuleHits, domain.RuleScanVelocity) {
			t.Fatal("fingerprint yang sama tidak boleh memicu velocity")
		}
	}
	// Ambang dari data: max_fingerprints 1 → pemindai kedua langsung kena.
	f.exec(`UPDATE anomaly_rules SET params = jsonb_set(params, '{max_fingerprints}', '1') WHERE code = 'SCAN_VELOCITY'`)
	third := unit(2).Code
	f.scanFrom(third, "x-1", netIP(21), nil)
	if r := f.scanFrom(third, "x-2", netIP(22), nil); r.Result != domain.ResultSuspicious {
		t.Fatalf("params dari anomaly_rules tidak dipakai: %+v", r)
	}
}

func TestVerify_NonSellableScan(t *testing.T) {
	f := newFx(t)
	// Stok gudang pusat yang belum pernah dikirim.
	central := f.scan(unit(0).Code, "anon-1", nil)
	if central.Result != domain.ResultSuspicious || !slices.Contains(central.RuleHits, domain.RuleNonSellableScan) {
		t.Fatalf("stok pusat belum dikirim: %+v", central)
	}
	if central.ProductName != nil {
		t.Fatal("SUSPICIOUS tidak boleh membocorkan detail produk")
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE code = ? AND first_verified_at IS NULL AND verify_count = 1`, unit(0).Code); n != 1 {
		t.Fatal("scan SUSPICIOUS tidak boleh mengisi first_verified_at")
	}
	// Label GENERATED bocor dari vendor: anomali menunjuk job cetak.
	gen := codesOf(f.mint(domain.LevelUnit, 1))[0]
	r := f.scan(gen, "anon-2", nil)
	if r.Result != domain.ResultSuspicious {
		t.Fatalf("GENERATED: %+v", r)
	}
	dedupe := "NON_SELLABLE_SCAN:" + f.serialID(gen)
	a, ok := f.anomaly(domain.RuleNonSellableScan, dedupe)
	if !ok || a.Severity != "HIGH" || f.count(`SELECT count(*) FROM anomalies WHERE id = ? AND evidence->>'status' = 'GENERATED'
		AND evidence->>'code_batch_id' IS NOT NULL`, a.ID) != 1 {
		t.Fatalf("anomali %+v", a)
	}
	f.scan(gen, "anon-3", nil)
	if a2, _ := f.anomaly(domain.RuleNonSellableScan, dedupe); a2.HitCount != 2 {
		t.Fatalf("hit_count %d, ingin 2", a2.HitCount)
	}
	// Aturan dimatikan lewat data → tidak dievaluasi.
	f.exec(`UPDATE anomaly_rules SET enabled = false WHERE code = 'NON_SELLABLE_SCAN'`)
	if r := f.scan(unit(1).Code, "anon-4", nil); r.Result != domain.ResultGenuineFirst {
		t.Fatalf("aturan nonaktif tetap dievaluasi: %+v", r)
	}

	// Negatif: stok toko sendiri yang sudah dikirim.
	g := newFx(t)
	g.shipPalletTo(locJaksel, nil)
	if r := g.scan(unit(0).Code, "anon-1", &geoStoreJaks); r.Result != domain.ResultGenuineFirst {
		t.Fatalf("stok toko: %+v", r)
	}
}

func TestVerify_UnknownCodeAndMalformed(t *testing.T) {
	f := newFx(t)
	unknown, _ := domain.GenerateSerialCode(cryptoRand{})
	raw := " " + unknown[:4] + "-" + unknown[4:8] + "-" + unknown[8:] + " "
	res := f.scan(raw, "anon-1", &geoMakassar)
	if res.Result != domain.ResultNotFound || res.ProductName != nil || res.FirstVerifiedAt != nil {
		t.Fatalf("NOT_FOUND membocorkan data: %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM scan_events WHERE raw_code = ? AND serial_unit_id IS NULL AND result = 'NOT_FOUND'
		AND rule_hits = ARRAY['UNKNOWN_CODE']`, raw); n != 1 {
		t.Fatal("raw_code harus disimpan apa adanya")
	}
	day := t0.Format("2006-01-02")
	unknown2, _ := domain.GenerateSerialCode(cryptoRand{})
	f.scan(unknown2, "anon-2", &geoMakassar)
	a, ok := f.anomaly(domain.RuleUnknownCode, "UNKNOWN_CODE:73.71:"+day)
	if !ok || a.HitCount != 2 {
		t.Fatalf("hotspot per wilayah/hari %+v", a)
	}
	f.clk.Advance(24 * time.Hour)
	f.scan(unknown, "anon-3", &geoMakassar)
	if _, ok := f.anomaly(domain.RuleUnknownCode, "UNKNOWN_CODE:73.71:"+t0.Add(24*time.Hour).Format("2006-01-02")); !ok {
		t.Fatal("hari baru harus membuat anomali hotspot baru")
	}

	// Format salah: NOT_FOUND, tetap direkam (raw dipotong 64 karakter) dan
	// ikut hotspot UNKNOWN_CODE; input kosong / hanya NUL tidak direkam.
	before := f.count(`SELECT count(*) FROM scan_events`)
	long := strings.Repeat("Ä", 100)
	bad := []string{"bukan-kode", "ABCDEFGHJKMNPQRSU", "💥💥💥", "ABCDEFGHJKMNPQR\x00", long}
	for _, b := range append(bad, "", "\x00") {
		if r := f.scan(b, "anon-x", &geoMakassar); r.Result != domain.ResultNotFound || r.ProductName != nil {
			t.Fatalf("%q: %+v", b, r)
		}
	}
	if after := f.count(`SELECT count(*) FROM scan_events`); after != before+int64(len(bad)) {
		t.Fatalf("scan format salah tercatat %d, ingin %d", after-before, len(bad))
	}
	if n := f.count(`SELECT count(*) FROM scan_events WHERE raw_code = ? AND serial_unit_id IS NULL AND result = 'NOT_FOUND'
		AND region_code = '73.71' AND rule_hits = ARRAY['UNKNOWN_CODE']`, strings.Repeat("Ä", 64)); n != 1 {
		t.Fatal("raw_code panjang harus dipotong ke 64 karakter")
	}
	if n := f.count(`SELECT count(*) FROM scan_events WHERE raw_code = 'ABCDEFGHJKMNPQR'`); n != 1 {
		t.Fatal("NUL harus dibuang dari raw_code")
	}
}

// Kode berformat salah direkam tanpa lookup ke serial_units: dengan tabel
// serial_units disembunyikan (rename), format salah tetap sukses; kode
// berformat benar gagal (bukti jalur itu memang membaca serial_units).
func TestVerify_MalformedCodeNeverReadsSerialUnits(t *testing.T) {
	db := testutil.NewDB(t)
	if err := db.Exec(`ALTER TABLE serial_units RENAME TO serial_units_hidden`).Error; err != nil {
		t.Fatal(err)
	}
	svc := service.NewVerifyService(db, nil, testPepper, nil)
	res, err := svc.Verify(t.Context(), service.VerifyInput{RawCode: "' OR 1=1 --", AnonID: "a", ClientIP: "203.0.113.5"})
	if err != nil || res.Result != domain.ResultNotFound {
		t.Fatalf("format salah: %+v %v", res, err)
	}
	var n int64
	if err := db.Raw(`SELECT count(*) FROM scan_events WHERE raw_code = ? AND serial_unit_id IS NULL`, "' OR 1=1 --").Scan(&n).Error; err != nil || n != 1 {
		t.Fatalf("scan format salah tidak direkam: %d %v", n, err)
	}
	valid, _ := domain.GenerateSerialCode(cryptoRand{})
	if _, err := svc.Verify(t.Context(), service.VerifyInput{RawCode: valid, AnonID: "a"}); err == nil {
		t.Fatal("kode berformat benar seharusnya membaca serial_units (yang disembunyikan)")
	}
}

// Scan bersamaan pada unit yang sama: tepat satu GENUINE_FIRST.
func TestVerify_ConcurrentFirstScan(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	code := unit(4).Code
	const n = 10
	results := make([]string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := f.verify.Verify(f.ctx, service.VerifyInput{RawCode: code, AnonID: "same-device", ClientIP: "203.0.113.9"})
			if err != nil {
				results[i] = "error:" + err.Error()
				return
			}
			results[i] = r.Result
		}(i)
	}
	close(start)
	wg.Wait()
	first := 0
	for _, r := range results {
		switch r {
		case domain.ResultGenuineFirst:
			first++
		case domain.ResultGenuineRepeat:
		default:
			t.Fatalf("hasil %s", r)
		}
	}
	if first != 1 {
		t.Fatalf("GENUINE_FIRST %d kali, ingin 1", first)
	}
	if v := f.count(`SELECT verify_count FROM serial_units WHERE code = ?`, code); v != n {
		t.Fatalf("verify_count %d, ingin %d", v, n)
	}
}
