package domain

import (
	"strings"
	"testing"
)

func TestNormalizeIDPhone(t *testing.T) {
	valid := map[string]string{
		"081234567890":       "+6281234567890",
		"+6281234567890":     "+6281234567890",
		"6281234567890":      "+6281234567890",
		"81234567890":        "+6281234567890",
		" 0812-3456-7890 ":   "+6281234567890",
		"+62 812 3456 7890":  "+6281234567890",
		"(0812) 3456.7890":   "+6281234567890",
		"0812345678":         "+62812345678",    // 9 digit nasional (batas bawah)
		"0812345678901":      "+62812345678901", // 12 digit nasional (batas atas)
		"+62-857-1111-2222":  "+6285711112222",
		"0878 0000 0000":     "+6287800000000",
		"62 0812 3456 7890x": "",
	}
	for in, want := range valid {
		got, err := NormalizeIDPhone(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q seharusnya ditolak, dapat %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q → %q, %v; ingin %q", in, got, err, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"08123456",        // terlalu pendek
		"08123456789012",  // terlalu panjang
		"0212345678",      // telepon rumah Jakarta (bukan 8)
		"+6581234567",     // Singapura
		"+1 415 555 0100", // kode negara lain
		"62081234567890",  // 62 lalu 0
		"0812+34567890",   // plus di tengah
		"08１２34567890",    // digit fullwidth
		"٠٨١٢٣٤٥٦٧٨٩٠",    // digit Arab-Indic
		"0812345678a",     // huruf
		"++6281234567890", // plus ganda
		strings.Repeat("8", 40),
	}
	for _, in := range invalid {
		if got, err := NormalizeIDPhone(in); err == nil {
			t.Errorf("%q seharusnya ditolak, dapat %q", in, got)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	if got := MaskPhone("+6281234567890"); got != "+62812*****890" {
		t.Fatalf("mask %q", got)
	}
	if got := MaskPhone("+62"); got != "****" {
		t.Fatalf("mask pendek %q", got)
	}
	if strings.Contains(MaskPhone("+6281234567890"), "4567") {
		t.Fatal("bagian tengah nomor bocor")
	}
}
