package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoad_ClaimPepper(t *testing.T) {
	long := strings.Repeat("p", MinClaimPepperBytes)
	tests := []struct {
		name      string
		env       string
		pepper    string
		setPepper bool
		wantErr   bool
		want      string
	}{
		{name: "production tanpa pepper ditolak", env: EnvProduction, wantErr: true},
		{name: "production pepper pendek ditolak", env: EnvProduction, pepper: strings.Repeat("p", MinClaimPepperBytes-1), setPepper: true, wantErr: true},
		{name: "production pepper 31 byte unicode ditolak", env: EnvProduction, pepper: strings.Repeat("é", 15) + "x", setPepper: true, wantErr: true},
		{name: "production pepper 32 byte diterima", env: EnvProduction, pepper: long, setPepper: true, want: long},
		{name: "development tanpa pepper memakai pepper demo", env: EnvDevelopment, want: DemoClaimPepper},
		{name: "test pepper pendek tetap diterima", env: EnvTest, pepper: "pendek", setPepper: true, want: "pendek"},
		{name: "spasi di dalam pepper dipertahankan", env: EnvProduction, pepper: " " + long + " ", setPepper: true, want: " " + long + " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATABASE_URL", "postgres://x")
			t.Setenv("APP_ENV", tt.env)
			t.Setenv("PII_ENC_KEY", strings.Repeat("k", MinPIIEncKeyBytes))
			if tt.setPepper {
				t.Setenv("CLAIM_PEPPER", tt.pepper)
			}
			cfg, err := Load()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "CLAIM_PEPPER") {
					t.Fatalf("ingin error CLAIM_PEPPER, dapat %v", err)
				}
				// Nilai secret tidak boleh muncul di pesan error.
				if tt.pepper != "" && strings.Contains(err.Error(), tt.pepper) {
					t.Fatalf("pesan error membocorkan pepper: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(cfg.ClaimPepper) != tt.want {
				t.Fatalf("pepper %q, ingin %q", cfg.ClaimPepper, tt.want)
			}
		})
	}
}

// Kombinasi APP_ENV x nilai demo x SEED_DEMO_ALLOW_PRODUCTION.
func TestLoad_DemoValuesInProduction(t *testing.T) {
	realPepper := strings.Repeat("r", MinClaimPepperBytes)
	realPII := strings.Repeat("s", MinPIIEncKeyBytes)
	tests := []struct {
		name     string
		env      map[string]string
		wantErrs []string // kosong = diterima
		override bool
	}{
		{
			name:     "production pepper demo tanpa override ditolak",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": DemoClaimPepper},
			wantErrs: []string{"CLAIM_PEPPER memakai nilai demo"},
		},
		{
			name:     "production SEED_DEMO tanpa override ditolak (password default juga)",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": realPepper, "SEED_DEMO": "true"},
			wantErrs: []string{"SEED_DEMO=true ditolak", "SEED_DEMO_PASSWORD memakai password demo default"},
		},
		{
			name:     "production SEED_DEMO password kustom tanpa override tetap ditolak",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": realPepper, "SEED_DEMO": "true", "SEED_DEMO_PASSWORD": "password-kustom-panjang"},
			wantErrs: []string{"SEED_DEMO=true ditolak"},
		},
		{
			name:     "production override false eksplisit tetap ditolak",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": DemoClaimPepper, "SEED_DEMO": "true", "SEED_DEMO_ALLOW_PRODUCTION": "false"},
			wantErrs: []string{"CLAIM_PEPPER memakai nilai demo", "SEED_DEMO=true ditolak"},
		},
		{
			name: "production semua nilai demo dengan override diterima",
			env: map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": DemoClaimPepper, "SEED_DEMO": "true", "SEED_DEMO_ALLOW_PRODUCTION": "true",
				"PII_ENC_KEY": DemoPIIEncKey, "OTP_MOCK_CODE": DemoOTPCode},
			override: true,
		},
		{
			name:     "production PII_ENC_KEY demo tanpa override ditolak",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": realPepper, "PII_ENC_KEY": DemoPIIEncKey},
			wantErrs: []string{"PII_ENC_KEY memakai nilai demo"},
		},
		{
			name:     "production OTP mock tanpa override ditolak (kode kustom juga)",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": realPepper, "PII_ENC_KEY": realPII, "OTP_MOCK_CODE": "135799"},
			wantErrs: []string{"OTP_MOCK_CODE (OTP mock) ditolak"},
		},
		{
			name:     "production tanpa PII_ENC_KEY ditolak walau ada override",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": realPepper, "SEED_DEMO_ALLOW_PRODUCTION": "true"},
			wantErrs: []string{"PII_ENC_KEY wajib diisi minimal 32 byte"},
		},
		{
			name:     "production override tidak melonggarkan panjang pepper",
			env:      map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": "pendek", "SEED_DEMO_ALLOW_PRODUCTION": "true"},
			wantErrs: []string{"minimal 32 byte"},
		},
		{
			name: "production nilai sungguhan tanpa seed diterima",
			env:  map[string]string{"APP_ENV": EnvProduction, "CLAIM_PEPPER": realPepper, "PII_ENC_KEY": realPII},
		},
		{
			name: "development nilai demo dan SEED_DEMO diterima tanpa override",
			env:  map[string]string{"APP_ENV": EnvDevelopment, "SEED_DEMO": "true"},
		},
		{
			name: "test nilai demo diterima tanpa override",
			env:  map[string]string{"APP_ENV": EnvTest, "CLAIM_PEPPER": DemoClaimPepper, "SEED_DEMO": "true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATABASE_URL", "postgres://x")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			if len(tt.wantErrs) == 0 {
				if err != nil {
					t.Fatalf("ingin diterima, dapat %v", err)
				}
				if cfg.DemoOverrideActive() != tt.override {
					t.Fatalf("DemoOverrideActive = %v, ingin %v", cfg.DemoOverrideActive(), tt.override)
				}
				return
			}
			if err == nil {
				t.Fatal("ingin ditolak")
			}
			for _, want := range tt.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error tidak memuat %q:\n%v", want, err)
				}
			}
			if strings.Contains(err.Error(), DemoClaimPepper) || strings.Contains(err.Error(), DefaultDemoPassword) ||
				strings.Contains(err.Error(), DemoPIIEncKey) || strings.Contains(err.Error(), "135799") {
				t.Fatalf("pesan error membocorkan nilai secret: %v", err)
			}
		})
	}
}

// Satu sumber default: compose, Makefile, dan .env.example memakai nilai yang
// sama dengan konstanta Go, sehingga stack compose dan `make api` di volume
// yang sama menghasilkan dan memverifikasi hash PIN dengan pepper yang sama.
func TestDemoDefaultsMatchRepoFiles(t *testing.T) {
	root := "../../../../"
	for _, file := range []string{"compose.yaml", "Makefile", ".env.example"} {
		b, err := os.ReadFile(root + file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for name, val := range map[string]string{
			"CLAIM_PEPPER": DemoClaimPepper, "SEED_DEMO_PASSWORD": DefaultDemoPassword,
			"PII_ENC_KEY": DemoPIIEncKey, "OTP_MOCK_CODE": DemoOTPCode,
		} {
			// Nilai harus menempel ke nama variabelnya (KEY=val, KEY ?= val,
			// atau ${KEY:-val}), bukan sekadar muncul di mana saja di file.
			if !strings.Contains(s, name+"="+val) && !strings.Contains(s, name+" ?= "+val) &&
				!strings.Contains(s, "${"+name+":-"+val+"}") {
				t.Errorf("%s: default %s tidak sama dengan konstanta Go", file, name)
			}
		}
	}
}

func TestLoad_SeedConfig(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Seed.Demo || cfg.Seed.DemoPassword != DefaultDemoPassword {
		t.Fatalf("default seed salah: demo=%v", cfg.Seed.Demo)
	}

	t.Setenv("SEED_DEMO", "true")
	t.Setenv("SEED_DEMO_PASSWORD", "kata-sandi-demo-panjang")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Seed.Demo || cfg.Seed.DemoPassword != "kata-sandi-demo-panjang" {
		t.Fatal("SEED_DEMO/SEED_DEMO_PASSWORD tidak terbaca")
	}

	t.Setenv("SEED_DEMO", "mungkin")
	t.Setenv("SEED_DEMO_PASSWORD", "pendek")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "SEED_DEMO ") || !strings.Contains(err.Error(), "SEED_DEMO_PASSWORD") {
		t.Fatalf("ingin error SEED_DEMO dan SEED_DEMO_PASSWORD, dapat %v", err)
	}
}
