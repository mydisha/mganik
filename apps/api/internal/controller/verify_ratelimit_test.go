package controller

import (
	"errors"
	"testing"
	"time"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
)

func TestPublicRateLimiter_FixedWindowPerKey(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	l := NewPublicRateLimiter(2, time.Minute, clk)
	for i := range 2 {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request ke-%d ditolak", i+1)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait != time.Minute {
		t.Fatalf("request ke-3: ok=%v wait=%s", ok, wait)
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("kunci lain ikut dibatasi")
	}
	clk.Advance(30 * time.Second)
	if _, wait := l.Allow("a"); wait != 30*time.Second {
		t.Fatalf("sisa tunggu %s", wait)
	}
	clk.Advance(30 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("jendela baru harus mengizinkan lagi")
	}
}

// Memori dibatasi: kunci baru ditolak saat penuh (fail closed), dan kunci
// kedaluwarsa dibersihkan setiap jendela.
func TestPublicRateLimiter_BoundedMemory(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	l := NewPublicRateLimiter(5, time.Minute, clk)
	l.maxKeys = 3
	for _, k := range []string{"a", "b", "c"} {
		if ok, _ := l.Allow(k); !ok {
			t.Fatalf("kunci %s ditolak", k)
		}
	}
	if ok, _ := l.Allow("d"); ok {
		t.Fatal("kunci ke-4 harus ditolak saat penuh")
	}
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("kunci lama tetap dilayani")
	}
	clk.Advance(time.Minute)
	if ok, _ := l.Allow("d"); !ok {
		t.Fatal("setelah pembersihan kunci baru harus diterima")
	}
	if len(l.buckets) != 1 {
		t.Fatalf("bucket tersisa %d, ingin 1", len(l.buckets))
	}
}

func TestNormalizeSerialCodes(t *testing.T) {
	codes, err := normalizeSerialCodes("codes", []string{"abcd-efgh-jkmn-pqrs", "ABCDEFGHJKMNPQRT"})
	if err != nil || codes[0] != "ABCDEFGHJKMNPQRS" {
		t.Fatalf("%v %v", codes, err)
	}
	_, err = normalizeSerialCodes("codes", []string{"ABCDEFGHJKMNPQRS", "bukan"})
	var ae *apperror.Error
	if !errors.As(err, &ae) || ae.Code != "validation_failed" || len(ae.Fields) != 1 ||
		ae.Fields[0].Field != "codes[1]" || ae.Fields[0].Code != "invalid_format" {
		t.Fatalf("format salah: %+v", err)
	}
	// Duplikat setelah normalisasi (huruf kecil = kode yang sama).
	_, err = normalizeSerialCodes("codes", []string{"ABCDEFGHJKMNPQRS", "ABCDEFGHJKMNPQRT", "abcdefghjkmnpqrs"})
	if !errors.As(err, &ae) || ae.Status != 422 || ae.Code != "duplicate_code" || len(ae.Fields) != 1 ||
		ae.Fields[0].Field != "codes[2]" || ae.Fields[0].Code != "duplicate_code" {
		t.Fatalf("duplikat: %+v", err)
	}
}

func TestRateLimitKeyForIP(t *testing.T) {
	cases := map[string]string{
		"192.0.2.10":              "192.0.2.10",
		"::ffff:192.0.2.10":       "192.0.2.10",
		"2001:db8:1:2:aaaa::1":    "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::9999": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":         "2001:db8:1:3::/64",
		"bukan-ip":                "raw:bukan-ip",
	}
	for in, want := range cases {
		if got := RateLimitKeyForIP(in); got != want {
			t.Errorf("RateLimitKeyForIP(%q) = %q, ingin %q", in, got, want)
		}
	}
}
