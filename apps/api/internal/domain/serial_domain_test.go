package domain_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"math"
	"strings"
	"testing"
	"time"

	"mganik/api/internal/domain"
)

func TestLifecycle_FromData(t *testing.T) {
	lc := domain.NewLifecycle([]domain.LifecycleTransition{
		{From: "", To: "GENERATED", BizStep: "encoding"},
		{From: "GENERATED", To: "COMMISSIONED", BizStep: "commissioning", Permission: "serial.commission"},
		{From: "IN_STOCK", To: "QUARANTINED", BizStep: "holding"},
		{From: "COMMISSIONED", To: "QUARANTINED", BizStep: "holding"},
		{From: "RETURNED", To: "QUARANTINED", BizStep: "inspecting"},
	})
	if !lc.CanTransition("GENERATED", "COMMISSIONED") {
		t.Fatal("GENERATED -> COMMISSIONED harus sah")
	}
	if lc.CanTransition("COMMISSIONED", "GENERATED") {
		t.Fatal("transisi balik tidak boleh sah")
	}
	if lc.CanTransition("QUARANTINED", "QUARANTINED") {
		t.Fatal("self-loop tidak boleh sah")
	}
	// Status awal (from kosong) tidak masuk AllowedFrom: tidak bisa dicapai lewat UPDATE.
	if got := lc.AllowedFrom("GENERATED"); len(got) != 0 {
		t.Fatalf("AllowedFrom(GENERATED) = %v, ingin kosong", got)
	}
	got := lc.AllowedFrom("QUARANTINED")
	if strings.Join(got, ",") != "COMMISSIONED,IN_STOCK,RETURNED" {
		t.Fatalf("AllowedFrom(QUARANTINED) = %v", got)
	}
	got[0] = "DIUBAH"
	if lc.AllowedFrom("QUARANTINED")[0] != "COMMISSIONED" {
		t.Fatal("AllowedFrom harus mengembalikan salinan")
	}
	tr, ok := lc.Lookup("RETURNED", "QUARANTINED")
	if !ok || tr.BizStep != "inspecting" {
		t.Fatalf("Lookup = %+v %v", tr, ok)
	}
}

func TestHaversine(t *testing.T) {
	jakarta := domain.GeoPoint{Lat: -6.2088, Lng: 106.8456}
	surabaya := domain.GeoPoint{Lat: -7.2575, Lng: 112.7521}
	d := domain.HaversineKm(jakarta, surabaya)
	if d < 650 || d > 680 {
		t.Fatalf("Jakarta-Surabaya %.1f km, ingin sekitar 663 km", d)
	}
	if domain.HaversineKm(jakarta, jakarta) != 0 {
		t.Fatal("jarak ke diri sendiri harus 0")
	}
	if (domain.GeoPoint{Lat: 91}).Valid() || (domain.GeoPoint{Lng: math.NaN()}).Valid() {
		t.Fatal("titik di luar rentang/NaN harus tidak valid")
	}
}

func TestImpossibleTravel_GPSvsIPThreshold(t *testing.T) {
	p := domain.ImpossibleTravelParams{MaxSpeedKmh: 900, MinDistanceKmGPS: 100, MinDistanceKmIP: 500}
	t0 := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	surabaya := domain.GeoPoint{Lat: -7.2575, Lng: 112.7521}
	semarang := domain.GeoPoint{Lat: -7.0051, Lng: 110.4381} // ±257 km
	prev := domain.ScanPoint{Point: surabaya, Source: domain.GeoGPS, At: t0}
	cur := domain.ScanPoint{Point: semarang, Source: domain.GeoGPS, At: t0.Add(10 * time.Minute)}

	if c := domain.ImpossibleTravel(prev, cur, p); !c.Hit || c.MinDistanceKm != 100 {
		t.Fatalf("GPS-GPS 257 km dalam 10 menit harus kena: %+v", c)
	}
	cur.Source = domain.GeoIP
	if c := domain.ImpossibleTravel(prev, cur, p); c.Hit || c.MinDistanceKm != 500 {
		t.Fatalf("GPS-IP 257 km tidak boleh kena (ambang IP 500 km): %+v", c)
	}
	cur.Source = domain.GeoGPS
	cur.At = t0.Add(5 * time.Hour) // 51 km/jam
	if c := domain.ImpossibleTravel(prev, cur, p); c.Hit {
		t.Fatalf("perjalanan wajar tidak boleh kena: %+v", c)
	}
	// Selisih waktu nol: kecepatan tak hingga, tetap butuh jarak minimum.
	cur.At = t0
	if c := domain.ImpossibleTravel(prev, cur, p); !c.Hit || !math.IsInf(c.SpeedKmh, 1) {
		t.Fatalf("selisih waktu nol harus kena: %+v", c)
	}
	// Selisih negatif (clock skew): nilai absolut, bukan tak hingga.
	cur.At = t0.Add(-5 * time.Hour)
	if c := domain.ImpossibleTravel(prev, cur, p); c.Hit || math.IsInf(c.SpeedKmh, 0) || c.SpeedKmh < 50 || c.SpeedKmh > 53 {
		t.Fatalf("selisih waktu negatif harus memakai nilai absolut: %+v", c)
	}
	cur.At = t0.Add(-10 * time.Minute)
	if c := domain.ImpossibleTravel(prev, cur, p); !c.Hit || math.IsInf(c.SpeedKmh, 0) {
		t.Fatalf("selisih negatif 10 menit tetap harus dinilai dari kecepatannya: %+v", c)
	}
	near := domain.ScanPoint{Point: domain.GeoPoint{Lat: -7.30, Lng: 112.75}, Source: domain.GeoGPS, At: t0}
	if c := domain.ImpossibleTravel(prev, near, p); c.Hit {
		t.Fatalf("jarak di bawah minimum tidak boleh kena: %+v", c)
	}
}

func TestClampRiskAndEffects(t *testing.T) {
	for in, want := range map[int]int{-5: 0, 0: 0, 55: 55, 100: 100, 160: 100} {
		if got := domain.ClampRisk(in); got != want {
			t.Errorf("ClampRisk(%d) = %d, ingin %d", in, got, want)
		}
	}
	if got := domain.StrongestEffect(); got != domain.EffectNone {
		t.Fatalf("tanpa efek = %s", got)
	}
	if got := domain.StrongestEffect(domain.EffectNone, domain.EffectSuspicious, domain.EffectRecalled); got != domain.EffectRecalled {
		t.Fatalf("RECALLED harus menang atas SUSPICIOUS, dapat %s", got)
	}
	if got := domain.StrongestEffect(domain.EffectNone, domain.EffectNone); got != domain.EffectNone {
		t.Fatalf("OUT_OF_TERRITORY (NONE) tidak boleh mengubah hasil, dapat %s", got)
	}
}

func TestParseRuleParams_KeepsDefaults(t *testing.T) {
	p := domain.ImpossibleTravelParams{MaxSpeedKmh: 900, MinDistanceKmGPS: 100, MinDistanceKmIP: 500}
	if err := domain.ParseRuleParams([]byte(`{"max_speed_kmh": 500}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.MaxSpeedKmh != 500 || p.MinDistanceKmGPS != 100 || p.MinDistanceKmIP != 500 {
		t.Fatalf("params = %+v", p)
	}
	if err := domain.ParseRuleParams([]byte(`{"max_speed_kmh": "cepat"}`), &p); err == nil {
		t.Fatal("params bertipe salah harus error")
	}
}

func TestSerialCode_GenerateAndNormalize(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		code, err := domain.GenerateSerialCode(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if !domain.ValidSerialCode(code) || seen[code] {
			t.Fatalf("kode %q tidak valid atau duplikat", code)
		}
		seen[code] = true
		pin, err := domain.GenerateClaimPIN(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := domain.NormalizeClaimPIN(pin); !ok {
			t.Fatalf("PIN %q tidak valid", pin)
		}
	}
	if _, err := domain.GenerateSerialCode(bytes.NewReader([]byte{1, 2})); err == nil {
		t.Fatal("reader yang habis harus error")
	}

	cases := []struct {
		in, want string
		ok       bool
	}{
		{"  abcd-efgh-jkmn-pqrs ", "ABCDEFGHJKMNPQRS", true},
		{"ABCD EFGH IJKL MNOP", "ABCDEFGH1JK1MN0P", true}, // I/L -> 1, O -> 0
		{"ABCDEFGHJKMNPQRSU", "", false},                  // U bukan Crockford, terlalu panjang
		{"ABCDEFGHJKMNPQR", "", false},                    // 15 karakter
		{"ÄBCDEFGHJKMNPQRS", "", false},                   // unicode
		{"", "", false},
		{strings.Repeat("A", 1000), "", false},
	}
	for _, c := range cases {
		got, ok := domain.NormalizeSerialCode(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("NormalizeSerialCode(%q) = %q,%v ingin %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestKeyedHasher_MatchesCryptoHMAC(t *testing.T) {
	keys := [][]byte{{}, []byte("k"), bytes.Repeat([]byte{7}, 64), bytes.Repeat([]byte{9}, 100)}
	msgs := []string{"", "a", strings.Repeat("x", 32), strings.Repeat("y", 128), strings.Repeat("z", 129), strings.Repeat("ü", 300)}
	for _, k := range keys {
		h := domain.NewKeyedHasher(k)
		for _, m := range msgs {
			mac := hmac.New(sha256.New, k)
			mac.Write([]byte(m))
			want := mac.Sum(nil)
			got := h.SumString(m)
			if !bytes.Equal(got[:], want) {
				t.Fatalf("key %d byte, msg %d byte: hasil berbeda dengan crypto/hmac", len(k), len(m))
			}
		}
	}
}

func TestKeyedHasher_NoAllocations(t *testing.T) {
	h := domain.NewKeyedHasher([]byte("kunci-uji"))
	id := "0123456789abcdef0123456789abcdef"
	allocs := testing.AllocsPerRun(1000, func() { _ = h.SumString(id) })
	if allocs != 0 {
		t.Fatalf("SumString mengalokasi %.0f kali per panggilan, ingin 0", allocs)
	}
}

func TestDeriveKey_DomainSeparated(t *testing.T) {
	a := domain.DeriveKey([]byte("pepper"), "a")
	b := domain.DeriveKey([]byte("pepper"), "b")
	if bytes.Equal(a, b) || len(a) != 32 {
		t.Fatal("sub-kunci dengan label berbeda harus berbeda")
	}
}

// Jalur panas verifikasi: fingerprint cookie per request.
func BenchmarkKeyedHasher_SumString(b *testing.B) {
	h := domain.NewKeyedHasher([]byte("kunci-benchmark"))
	id := "0123456789abcdef0123456789abcdef"
	b.ReportAllocs()
	for b.Loop() {
		_ = h.SumString(id)
	}
}

// Pembanding: hmac.New per request.
func BenchmarkCryptoHMAC_PerRequest(b *testing.B) {
	key := []byte("kunci-benchmark")
	id := []byte("0123456789abcdef0123456789abcdef")
	b.ReportAllocs()
	for b.Loop() {
		mac := hmac.New(sha256.New, key)
		mac.Write(id)
		_ = mac.Sum(nil)
	}
}
