package pii

import (
	"bytes"
	"strings"
	"testing"
)

func mustKeys(t *testing.T, master string) *Keys {
	t.Helper()
	k, err := New([]byte(master))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNew_RejectsShortKey(t *testing.T) {
	for _, key := range []string{"", "pendek", strings.Repeat("x", MinMasterKeyBytes-1)} {
		if _, err := New([]byte(key)); err == nil {
			t.Fatalf("kunci %d byte seharusnya ditolak", len(key))
		}
	}
	if _, err := New([]byte(strings.Repeat("x", MinMasterKeyBytes))); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptPhone_RoundTripAndNotPlaintext(t *testing.T) {
	k := mustKeys(t, "kunci-induk-test-panjang-32-byte!!")
	const phone = "+6281234567890"
	row := k.PhoneIndex(phone)
	a, err := k.EncryptPhone(phone, row)
	if err != nil {
		t.Fatal(err)
	}
	b, err := k.EncryptPhone(phone, row)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(a, []byte(phone)) || bytes.Contains(a, []byte("81234567890")) {
		t.Fatal("ciphertext memuat nomor polos")
	}
	if bytes.Equal(a, b) {
		t.Fatal("nonce harus acak: dua enkripsi nomor sama menghasilkan ciphertext sama")
	}
	for _, env := range [][]byte{a, b} {
		got, err := k.DecryptPhone(env, row)
		if err != nil || got != phone {
			t.Fatalf("dekripsi %q, %v", got, err)
		}
	}
}

// Ciphertext terikat ke barisnya: dipindah ke baris konsumen lain (blind
// index berbeda) gagal dibuka, walau kuncinya sama.
func TestDecryptPhone_BoundToRow(t *testing.T) {
	k := mustKeys(t, "kunci-induk-test-panjang-32-byte!!")
	rowA, rowB := k.PhoneIndex("+6281111111111"), k.PhoneIndex("+6282222222222")
	encA, err := k.EncryptPhone("+6281111111111", rowA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.DecryptPhone(encA, rowB); err != ErrDecrypt {
		t.Fatalf("ciphertext baris A terbuka di baris B: %v", err)
	}
	if _, err := k.DecryptPhone(encA, nil); err != ErrDecrypt {
		t.Fatalf("ciphertext terbuka tanpa pengikat baris: %v", err)
	}
	if got, err := k.DecryptPhone(encA, rowA); err != nil || got != "+6281111111111" {
		t.Fatalf("baris asal: %q %v", got, err)
	}
}

func TestDecryptPhone_RejectsTamperWrongKeyAndGarbage(t *testing.T) {
	k := mustKeys(t, "kunci-induk-test-panjang-32-byte!!")
	other := mustKeys(t, "kunci-induk-LAIN-panjang-32-byte!!")
	row := k.PhoneIndex("+628111111111")
	env, err := k.EncryptPhone("+628111111111", row)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(env)
	tampered[len(tampered)-1] ^= 0x01
	badVersion := bytes.Clone(env)
	badVersion[0] = 9
	cases := map[string]struct {
		keys *Keys
		env  []byte
	}{
		"tag diubah":     {k, tampered},
		"versi asing":    {k, badVersion},
		"kunci berbeda":  {other, env},
		"kosong":         {k, nil},
		"terlalu pendek": {k, env[:5]},
	}
	for name, c := range cases {
		if _, err := c.keys.DecryptPhone(c.env, row); err != ErrDecrypt {
			t.Errorf("%s: ingin ErrDecrypt, dapat %v", name, err)
		}
	}
}

func TestPhoneIndex_StableAndKeyed(t *testing.T) {
	k := mustKeys(t, "kunci-induk-test-panjang-32-byte!!")
	k2 := mustKeys(t, "kunci-induk-test-panjang-32-byte!!")
	other := mustKeys(t, "kunci-induk-LAIN-panjang-32-byte!!")
	a := k.PhoneIndex("+6281234567890")
	if len(a) != 32 {
		t.Fatalf("panjang blind index %d", len(a))
	}
	if !bytes.Equal(a, k2.PhoneIndex("+6281234567890")) {
		t.Fatal("blind index tidak stabil untuk kunci yang sama")
	}
	if bytes.Equal(a, k.PhoneIndex("+6281234567891")) {
		t.Fatal("nomor berbeda menghasilkan blind index sama")
	}
	if bytes.Equal(a, other.PhoneIndex("+6281234567890")) {
		t.Fatal("blind index tidak bergantung pada kunci")
	}
	// Subkey terpisah: blind index bukan HMAC dengan kunci hash IP.
	if bytes.Equal(a[:IPHashLen], k.HashIP("+6281234567890")) {
		t.Fatal("subkey blind index sama dengan subkey hash IP")
	}
}

func TestHashIP(t *testing.T) {
	k := mustKeys(t, "kunci-induk-test-panjang-32-byte!!")
	if k.HashIP("") != nil {
		t.Fatal("IP kosong harus nil")
	}
	h := k.HashIP("203.0.113.7")
	if len(h) != IPHashLen || !bytes.Equal(h, k.HashIP("203.0.113.7")) || bytes.Equal(h, k.HashIP("203.0.113.8")) {
		t.Fatalf("hash IP tidak sesuai: %x", h)
	}
	if bytes.Contains(h, []byte("203")) {
		t.Fatal("hash IP memuat IP polos")
	}
}

func TestOTPCodeHash_BoundToChallenge(t *testing.T) {
	k := mustKeys(t, "kunci-induk-test-panjang-32-byte!!")
	a := k.OTPCodeHash([]byte("challenge-1"), "246810")
	if !bytes.Equal(a, k.OTPCodeHash([]byte("challenge-1"), "246810")) {
		t.Fatal("hash OTP tidak deterministik")
	}
	if bytes.Equal(a, k.OTPCodeHash([]byte("challenge-2"), "246810")) || bytes.Equal(a, k.OTPCodeHash([]byte("challenge-1"), "246811")) {
		t.Fatal("hash OTP tidak terikat challenge/kode")
	}
}
