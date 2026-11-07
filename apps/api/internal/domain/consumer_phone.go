package domain

import (
	"errors"
	"strings"
)

// ErrInvalidPhone: nomor bukan nomor seluler Indonesia yang valid.
var ErrInvalidPhone = errors.New("nomor HP tidak valid")

const (
	// MaxPhoneInputLen membatasi panjang input mentah sebelum normalisasi.
	MaxPhoneInputLen = 32
	// Nomor seluler Indonesia: setelah kode negara 62 diawali 8, total 9-12
	// digit (mis. 812-3456-789 sampai 812-3456-78901).
	minIDMobileDigits = 9
	maxIDMobileDigits = 12
)

// NormalizeIDPhone menormalkan nomor seluler Indonesia ke E.164 (+628...).
// Menerima format umum: 0812..., 62812..., +62812..., 812..., dengan spasi,
// tanda hubung, titik, atau kurung sebagai pemisah. Hanya digit ASCII yang
// diterima (digit unicode lain ditolak supaya blind index tidak bercabang).
func NormalizeIDPhone(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > MaxPhoneInputLen {
		return "", ErrInvalidPhone
	}
	s := strings.TrimSpace(raw)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '+' && i == 0:
			// '+' hanya boleh di depan; diperiksa lewat prefix di bawah.
		case c == ' ' || c == '-' || c == '.' || c == '(' || c == ')':
		default:
			return "", ErrInvalidPhone
		}
	}
	digits := b.String()
	hasPlus := strings.HasPrefix(s, "+")
	switch {
	case hasPlus && strings.HasPrefix(digits, "62"):
		digits = digits[2:]
	case hasPlus:
		return "", ErrInvalidPhone // kode negara selain Indonesia
	case strings.HasPrefix(digits, "62"):
		digits = digits[2:]
	case strings.HasPrefix(digits, "0"):
		digits = digits[1:]
	}
	if len(digits) < minIDMobileDigits || len(digits) > maxIDMobileDigits || digits[0] != '8' {
		return "", ErrInvalidPhone
	}
	return "+62" + digits, nil
}

// MaskPhone menyamarkan nomor E.164 untuk ditampilkan: +62812****789.
func MaskPhone(e164 string) string {
	if len(e164) < 9 {
		return "****"
	}
	return e164[:6] + strings.Repeat("*", len(e164)-9) + e164[len(e164)-3:]
}
