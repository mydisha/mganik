package domain

import (
	"crypto/hmac"
	"crypto/sha256"
)

const (
	hmacBlockSize = 64
	// keyedInlineMax: pesan sampai panjang ini di-hash di stack tanpa alokasi.
	// Nilai yang di-hash di jalur panas (id cookie anonim, prefix IP) jauh
	// lebih pendek dari ini.
	keyedInlineMax = 128
)

// KeyedHasher menghitung HMAC-SHA256 dengan kunci tetap. ipad/opad dihitung
// sekali saat dibuat, lalu setiap Sum meng-hash di stack tanpa alokasi
// (jalur verifikasi publik dipanggil per request). Hasilnya identik dengan
// crypto/hmac (dikunci test).
type KeyedHasher struct {
	key  []byte
	ipad [hmacBlockSize]byte
	opad [hmacBlockSize]byte
}

// NewKeyedHasher menyiapkan hasher untuk key. Key di atas 64 byte di-hash
// dulu, sesuai RFC 2104.
func NewKeyedHasher(key []byte) *KeyedHasher {
	k := &KeyedHasher{key: append([]byte(nil), key...)}
	block := key
	if len(block) > hmacBlockSize {
		sum := sha256.Sum256(block)
		block = sum[:]
	}
	for i := range hmacBlockSize {
		var b byte
		if i < len(block) {
			b = block[i]
		}
		k.ipad[i] = b ^ 0x36
		k.opad[i] = b ^ 0x5c
	}
	return k
}

// SumString mengembalikan HMAC-SHA256(key, s).
func (k *KeyedHasher) SumString(s string) [32]byte {
	if len(s) > keyedInlineMax {
		mac := hmac.New(sha256.New, k.key)
		mac.Write([]byte(s))
		var out [32]byte
		copy(out[:], mac.Sum(nil))
		return out
	}
	var inner [hmacBlockSize + keyedInlineMax]byte
	copy(inner[:hmacBlockSize], k.ipad[:])
	n := copy(inner[hmacBlockSize:], s)
	innerSum := sha256.Sum256(inner[:hmacBlockSize+n])

	var outer [hmacBlockSize + sha256.Size]byte
	copy(outer[:hmacBlockSize], k.opad[:])
	copy(outer[hmacBlockSize:], innerSum[:])
	return sha256.Sum256(outer[:])
}

// DeriveKey menurunkan sub-kunci dengan pemisah domain: HMAC(master, label).
// Dipakai supaya satu secret (CLAIM_PEPPER) tidak dipakai mentah untuk dua
// tujuan berbeda (hash PIN vs fingerprint scan).
func DeriveKey(master []byte, label string) []byte {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}
