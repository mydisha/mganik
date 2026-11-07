package domain

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

var claimPINPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{10}$`)

// GenerateSerialCode membuat kode publik 80-bit acak (16 karakter Crockford)
// dari r. Produksi memakai crypto/rand.Reader; test boleh memakai reader tetap.
func GenerateSerialCode(r io.Reader) (string, error) {
	var b [10]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("membaca byte acak kode serial: %w", err)
	}
	return EncodeCrockford(b[:], SerialCodeLen)
}

// GenerateClaimPIN membuat PIN klaim 50-bit acak (10 karakter Crockford).
func GenerateClaimPIN(r io.Reader) (string, error) {
	var b [7]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("membaca byte acak PIN klaim: %w", err)
	}
	return EncodeCrockford(b[:], ClaimPINLen)
}

// NormalizeCrockford menyeragamkan input manusia: trim, huruf besar, buang
// spasi dan tanda hubung, lalu petakan karakter yang mudah tertukar sesuai
// aturan Crockford (I/L → 1, O → 0). Hasilnya belum tentu format yang sah.
func NormalizeCrockford(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		// Input sepanjang ini pasti tidak sah; jangan buang CPU untuk memetakannya.
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToUpper(s) {
		switch r {
		case ' ', '-':
			continue
		case 'I', 'L':
			b.WriteByte('1')
		case 'O':
			b.WriteByte('0')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeSerialCode menormalkan lalu memvalidasi kode serial. ok=false kalau
// formatnya tidak sah; pemanggil tidak boleh menyentuh DB untuk kode itu.
func NormalizeSerialCode(raw string) (string, bool) {
	code := NormalizeCrockford(raw)
	return code, ValidSerialCode(code)
}

// NormalizeClaimPIN menormalkan lalu memvalidasi PIN klaim.
func NormalizeClaimPIN(raw string) (string, bool) {
	pin := NormalizeCrockford(raw)
	return pin, claimPINPattern.MatchString(pin)
}
