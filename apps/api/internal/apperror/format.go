package apperror

import (
	"strconv"
	"strings"
)

// FormatNumber menulis bilangan bulat dengan pengelompokan id-ID untuk teks
// yang dibaca pengguna (DESIGN.md "Format"): 1234567 → "1.234.567".
func FormatNumber(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FormatRupiah menulis nominal rupiah utuh tanpa spasi: 150000 → "Rp150.000",
// -2500 → "-Rp2.500".
func FormatRupiah(n int64) string {
	if n < 0 {
		return "-Rp" + FormatNumber(n)[1:]
	}
	return "Rp" + FormatNumber(n)
}
