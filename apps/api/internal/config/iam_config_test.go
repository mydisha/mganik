package config

import (
	"strings"
	"testing"

	"mganik/api/internal/pii"
)

func TestLoad_IAMDefaultsOutsideProduction(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.PIIEncKey) != DemoPIIEncKey || cfg.OTPMockCode != DemoOTPCode || len(cfg.AllowedOrigins) != 0 {
		t.Fatalf("default IAM salah: otp=%q origins=%v", cfg.OTPMockCode, cfg.AllowedOrigins)
	}
}

func TestLoad_ProductionWithoutOTPDisablesMock(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("APP_ENV", EnvProduction)
	t.Setenv("CLAIM_PEPPER", strings.Repeat("c", MinClaimPepperBytes))
	t.Setenv("PII_ENC_KEY", strings.Repeat("k", MinPIIEncKeyBytes))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OTPMockCode != "" {
		t.Fatalf("production tanpa OTP_MOCK_CODE harus mematikan OTP mock, dapat %q", cfg.OTPMockCode)
	}
}

func TestLoad_PIIEncKeyKeepsSpaces(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	key := " " + strings.Repeat("k", MinPIIEncKeyBytes) + " "
	t.Setenv("PII_ENC_KEY", key)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.PIIEncKey) != key {
		t.Fatal("spasi di PII_ENC_KEY harus dipertahankan")
	}
}

// Kunci PII pendek ditolak di SEMUA lingkungan saat Load, dengan batas yang
// sama dengan package pii (bukan baru gagal di router.New).
func TestLoad_PIIEncKeyMinimumEverywhere(t *testing.T) {
	if MinPIIEncKeyBytes != pii.MinMasterKeyBytes {
		t.Fatalf("batas config %d ≠ batas pii %d", MinPIIEncKeyBytes, pii.MinMasterKeyBytes)
	}
	for _, env := range []string{EnvDevelopment, EnvTest, EnvProduction} {
		clearEnv(t)
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("APP_ENV", env)
		t.Setenv("CLAIM_PEPPER", strings.Repeat("c", MinClaimPepperBytes))
		t.Setenv("PII_ENC_KEY", strings.Repeat("k", MinPIIEncKeyBytes-1))
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "PII_ENC_KEY wajib diisi minimal 32 byte") {
			t.Errorf("%s: kunci 31 byte seharusnya ditolak, dapat %v", env, err)
		}
		if err != nil && strings.Contains(err.Error(), strings.Repeat("k", MinPIIEncKeyBytes-1)) {
			t.Errorf("%s: pesan error membocorkan kunci", env)
		}
	}
}

func TestLoad_OTPMockCodeFormat(t *testing.T) {
	for _, code := range []string{"12345", "1234567", "12345a", "１２３４５６"} {
		clearEnv(t)
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("OTP_MOCK_CODE", code)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "OTP_MOCK_CODE harus 6 digit") {
			t.Errorf("%q: ingin error format, dapat %v", code, err)
		}
	}
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("OTP_MOCK_CODE", "000123")
	cfg, err := Load()
	if err != nil || cfg.OTPMockCode != "000123" {
		t.Fatalf("kode valid ditolak: %v", err)
	}
}

func TestLoad_AllowedOrigins(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("ALLOWED_ORIGINS", " HTTP://LocalHost:5173/ , https://console.mganik.test ,")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://localhost:5173", "https://console.mganik.test"}
	if strings.Join(cfg.AllowedOrigins, "|") != strings.Join(want, "|") {
		t.Fatalf("origins %v, ingin %v", cfg.AllowedOrigins, want)
	}

	for _, bad := range []string{"localhost:5173", "ftp://x.test", "https://x.test/path", "https://x.test?q=1", "https://", "https://u:p@x.test"} {
		clearEnv(t)
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("ALLOWED_ORIGINS", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ALLOWED_ORIGINS") {
			t.Errorf("%q: ingin ditolak, dapat %v", bad, err)
		}
	}
}
