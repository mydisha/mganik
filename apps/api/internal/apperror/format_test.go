package apperror_test

import (
	"math"
	"testing"

	"mganik/api/internal/apperror"
)

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{
		0:             "0",
		7:             "7",
		999:           "999",
		1000:          "1.000",
		12345:         "12.345",
		1234567:       "1.234.567",
		-1000:         "-1.000",
		-999:          "-999",
		math.MaxInt64: "9.223.372.036.854.775.807",
		math.MinInt64: "-9.223.372.036.854.775.808",
	} {
		if got := apperror.FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %q, ingin %q", in, got, want)
		}
	}
}

func TestFormatRupiah(t *testing.T) {
	for in, want := range map[int64]string{
		0:           "Rp0",
		150000:      "Rp150.000",
		100_000_000: "Rp100.000.000",
		-2500:       "-Rp2.500",
	} {
		if got := apperror.FormatRupiah(in); got != want {
			t.Errorf("FormatRupiah(%d) = %q, ingin %q", in, got, want)
		}
	}
}
