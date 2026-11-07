package domain

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEncodeCrockford(t *testing.T) {
	// 10 byte = 80 bit = tepat 16 karakter.
	got, err := EncodeCrockford(bytes.Repeat([]byte{0xFF}, 10), SerialCodeLen)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.Repeat("Z", 16) {
		t.Fatalf("got %q", got)
	}
	zero, _ := EncodeCrockford(make([]byte, 10), SerialCodeLen)
	if zero != strings.Repeat("0", 16) || !ValidSerialCode(zero) {
		t.Fatalf("got %q", zero)
	}
	// 7 byte = 56 bit cukup untuk PIN 10 karakter (50 bit).
	pin, err := EncodeCrockford([]byte{1, 2, 3, 4, 5, 6, 7}, ClaimPINLen)
	if err != nil || len(pin) != ClaimPINLen {
		t.Fatalf("pin %q err %v", pin, err)
	}
	if _, err := EncodeCrockford(make([]byte, 9), SerialCodeLen); err == nil {
		t.Fatal("9 byte (72 bit) seharusnya kurang untuk 16 karakter")
	}
	if _, err := EncodeCrockford(nil, 0); err == nil {
		t.Fatal("n = 0 seharusnya ditolak")
	}
}

func TestValidSerialCode(t *testing.T) {
	for code, want := range map[string]bool{
		"0123456789ABCDEF":  true,
		"ZZZZZZZZZZZZZZZZ":  true,
		"0123456789ABCDEI":  false, // I bukan alfabet Crockford
		"0123456789ABCDEU":  false, // U juga bukan
		"0123456789abcdef":  false, // huruf kecil dinormalisasi sebelum validasi
		"0123456789ABCDE":   false,
		"0123456789ABCDEFG": false,
		"":                  false,
		"０123456789ABCDEF":  false, // digit fullwidth unicode
	} {
		if got := ValidSerialCode(code); got != want {
			t.Errorf("ValidSerialCode(%q) = %v, ingin %v", code, got, want)
		}
	}
}

func TestClaimSecretHash(t *testing.T) {
	pepper := []byte(strings.Repeat("k", 32))
	id := uuid.MustParse("0192f3a0-0000-7000-8000-000000000001")

	// Definisi eksplisit: HMAC-SHA256(pepper, 16 byte UUID ‖ PIN).
	mac := hmac.New(sha256.New, pepper)
	mac.Write(id[:])
	mac.Write([]byte("ABCDEFGH12"))
	want := mac.Sum(nil)

	got := ClaimSecretHash(pepper, id, "ABCDEFGH12")
	if !bytes.Equal(got, want) || len(got) != 32 {
		t.Fatalf("hash tidak sesuai definisi")
	}
	if !VerifyClaimPIN(pepper, id, "ABCDEFGH12", got) {
		t.Fatal("PIN benar ditolak")
	}
	if VerifyClaimPIN(pepper, id, "ABCDEFGH13", got) {
		t.Fatal("PIN salah diterima")
	}
	if VerifyClaimPIN([]byte("pepper-lain-pepper-lain-pepper-xx"), id, "ABCDEFGH12", got) {
		t.Fatal("pepper berbeda seharusnya menghasilkan hash berbeda")
	}
	other := uuid.MustParse("0192f3a0-0000-7000-8000-000000000002")
	if VerifyClaimPIN(pepper, other, "ABCDEFGH12", got) {
		t.Fatal("PIN yang sama untuk unit lain tidak boleh cocok")
	}
}

// Parameter kecil supaya test cepat; format dan verifikasi sama dengan OWASP.
var fastParams = Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func TestPasswordHashRoundTrip(t *testing.T) {
	for _, pw := range []string{"mganik-demo-2026", "kata sandi dengan spasi", "páss-wörd-ünïcode-密码", ""} {
		h, err := HashPassword(pw, fastParams)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(h, "$argon2id$v=19$m=64,t=1,p=1$") {
			t.Fatalf("format PHC salah: %s", h)
		}
		ok, err := VerifyPassword(pw, h)
		if err != nil || !ok {
			t.Fatalf("password %q tidak terverifikasi: %v", pw, err)
		}
		ok, err = VerifyPassword(pw+"x", h)
		if err != nil || ok {
			t.Fatalf("password salah diterima untuk %q", pw)
		}
	}
}

func TestPasswordHashSaltUnique(t *testing.T) {
	a, _ := HashPassword("sama", fastParams)
	b, _ := HashPassword("sama", fastParams)
	if a == b {
		t.Fatal("dua hash password yang sama harus berbeda (salt acak)")
	}
}

func TestOWASPParams(t *testing.T) {
	h, err := HashPassword("x", OWASPArgon2id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("parameter OWASP salah: %s", h)
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	for _, h := range []string{
		"",
		"plain",
		"$2a$10$abcdefghijklmnopqrstuv", // bcrypt
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",        // argon2i
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",       // versi lain
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", // memori raksasa
		"$argon2id$v=19$m=64,t=1,p=0$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$!!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		ok, err := VerifyPassword("x", h)
		if ok || !errors.Is(err, ErrInvalidPasswordHash) {
			t.Errorf("hash %q: ok=%v err=%v, ingin ErrInvalidPasswordHash", h, ok, err)
		}
	}
}
