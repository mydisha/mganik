package config

import (
	"strings"
	"testing"
	"time"
)

// clearEnv mengosongkan variabel yang mungkin sudah diset di shell developer.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"APP_ENV", "HTTP_ADDR", "LOG_LEVEL", "LOG_FORMAT", "TRUSTED_PROXIES",
		"HTTP_MAX_BODY_BYTES", "SHUTDOWN_GRACE", "DB_MAX_OPEN_CONNS", "DB_MAX_IDLE_CONNS",
		"DB_CONN_MAX_LIFETIME", "DB_CONNECT_TIMEOUT", "CLAIM_PEPPER", "SEED_DEMO", "SEED_DEMO_PASSWORD", "SEED_DEMO_ALLOW_PRODUCTION"} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppEnv != EnvDevelopment || cfg.HTTPAddr != ":8080" || cfg.MaxBodyBytes != 1<<20 {
		t.Fatalf("default salah: %+v", cfg)
	}
	if cfg.Database.ConnectTimeout != 10*time.Second {
		t.Fatalf("connect timeout %s", cfg.Database.ConnectTimeout)
	}
}

func TestLoad_TrustedProxiesTrimmed(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("TRUSTED_PROXIES", " 10.0.0.0/8, ,172.16.0.0/12 ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1] != "172.16.0.0/12" {
		t.Fatalf("proxies %q", cfg.TrustedProxies)
	}
}

func TestLoad_ReportsEveryInvalidValue(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("APP_ENV", "staging")
	t.Setenv("DB_MAX_OPEN_CONNS", "-1")
	t.Setenv("SHUTDOWN_GRACE", "sebentar")
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("ingin error")
	}
	for _, want := range []string{"DATABASE_URL", "APP_ENV", "DB_MAX_OPEN_CONNS", "SHUTDOWN_GRACE", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("pesan error tidak menyebut %s:\n%v", want, err)
		}
	}
}
