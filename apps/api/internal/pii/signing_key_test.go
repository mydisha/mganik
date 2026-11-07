package pii

import (
	"bytes"
	"errors"
	"testing"
)

func TestSigningKeyCipher_RoundTripAndBinding(t *testing.T) {
	master := []byte("kunci-induk-test-minimal-32-byte!!")
	c, err := NewSigningKeyCipher(master)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0xab}, 32)
	devA, devB := bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16)

	env1, err := c.Seal(key, devA)
	if err != nil {
		t.Fatal(err)
	}
	env2, _ := c.Seal(key, devA)
	if bytes.Equal(env1, env2) {
		t.Fatal("nonce acak: ciphertext key yang sama harus berbeda")
	}
	if bytes.Contains(env1, key) {
		t.Fatal("ciphertext memuat key polos")
	}
	got, err := c.Open(env1, devA)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("open: %v %x", err, got)
	}
	// Ciphertext tidak bisa dipindah ke device lain (AAD).
	if _, err := c.Open(env1, devB); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("device lain harus gagal: %v", err)
	}
	// Kunci induk berbeda tidak bisa membuka.
	other, _ := NewSigningKeyCipher([]byte("kunci-induk-lain-minimal-32-byte!!"))
	if _, err := other.Open(env1, devA); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("kunci induk lain harus gagal: %v", err)
	}
	// Subkey berbeda dari subkey nomor HP: envelope nomor HP tidak terbuka.
	keys, _ := New(master)
	phoneEnv, _ := keys.EncryptPhone("+628123", devA)
	if _, err := c.Open(phoneEnv, devA); !errors.Is(err, ErrDecrypt) {
		t.Fatal("subkey signing key tidak boleh sama dengan subkey nomor HP")
	}
	for _, bad := range [][]byte{nil, {envelopeV1}, append([]byte{9}, env1[1:]...)} {
		if _, err := c.Open(bad, devA); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("envelope rusak %x harus ErrDecrypt", bad)
		}
	}
	if _, err := NewSigningKeyCipher([]byte("pendek")); err == nil {
		t.Fatal("kunci induk pendek harus ditolak")
	}
}
