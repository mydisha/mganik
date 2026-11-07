// Package domain berisi aturan murni tanpa database: format kode serial,
// hash rahasia klaim, hash password. Bisa diuji tanpa I/O.
package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

const (
	// SerialCodeLen: kode publik di QR, 16 karakter Crockford Base32 = 80 bit.
	SerialCodeLen = 16
	// ClaimPINLen: PIN klaim di dalam kemasan, 10 karakter = 50 bit.
	ClaimPINLen = 10

	crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

var (
	crockford         = base32.NewEncoding(crockfordAlphabet).WithPadding(base32.NoPadding)
	serialCodePattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{16}$`)
)

// EncodeCrockford mengembalikan n karakter pertama encoding Crockford Base32
// dari b (5 bit per karakter). b harus memuat minimal n*5 bit.
func EncodeCrockford(b []byte, n int) (string, error) {
	if n <= 0 || len(b)*8 < n*5 {
		return "", fmt.Errorf("butuh minimal %d bit untuk %d karakter, tersedia %d bit", n*5, n, len(b)*8)
	}
	return crockford.EncodeToString(b)[:n], nil
}

// ValidSerialCode memeriksa format kode serial sebelum menyentuh DB.
func ValidSerialCode(code string) bool {
	return serialCodePattern.MatchString(code)
}

// ClaimSecretHash menghitung HMAC-SHA256(pepper, unit_id ‖ PIN) dengan unit_id
// sebagai 16 byte mentah UUID dan PIN sebagai byte ASCII. HMAC dipilih (bukan
// argon2) karena PIN berentropi tinggi (50 bit acak) dan perlu di-mint jutaan kali.
func ClaimSecretHash(pepper []byte, unitID uuid.UUID, pin string) []byte {
	mac := hmac.New(sha256.New, pepper)
	mac.Write(unitID[:])
	mac.Write([]byte(pin))
	return mac.Sum(nil)
}

// VerifyClaimPIN membandingkan PIN dengan hash tersimpan dalam waktu konstan.
func VerifyClaimPIN(pepper []byte, unitID uuid.UUID, pin string, stored []byte) bool {
	return hmac.Equal(ClaimSecretHash(pepper, unitID, pin), stored)
}
