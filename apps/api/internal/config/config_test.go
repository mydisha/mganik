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
		"DB_CONN_MAX_LIFETIME", "DB_CONNECT_TIMEOUT", "CLAIM_PEPPER", "SEED_DEMO", "SEED_DEMO_PASSWORD", "SEED_DEMO_ALLOW_PRODUCTION",
		"WEBHOOK_SECRET", "RESERVATION_TTL", "RUN_WORKER", "WORKER_INTERVAL", "OUTBOX_MAX_ATTEMPTS",
		"PII_ENC_KEY", "OTP_MOCK_CODE", "ALLOWED_ORIGINS"} {
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

func TestLoad_InventoryConfig(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	inv := cfg.Inventory
	if inv.ReservationTTL != 15*time.Minute || !inv.RunWorker || inv.WorkerInterval != 2*time.Second || inv.OutboxMaxAttempts != 10 {
		t.Fatalf("default inventory salah: ttl=%s run=%v interval=%s max=%d", inv.ReservationTTL, inv.RunWorker, inv.WorkerInterval, inv.OutboxMaxAttempts)
	}
	if string(inv.WebhookSecret) != DemoWebhookSecret {
		t.Fatal("development tanpa WEBHOOK_SECRET harus memakai secret demo")
	}

	t.Setenv("RESERVATION_TTL", "30m")
	t.Setenv("RUN_WORKER", "false")
	t.Setenv("WORKER_INTERVAL", "500ms")
	t.Setenv("WEBHOOK_SECRET", " rahasia dengan spasi ")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	inv = cfg.Inventory
	if inv.ReservationTTL != 30*time.Minute || inv.RunWorker || inv.WorkerInterval != 500*time.Millisecond {
		t.Fatalf("nilai env tidak terbaca: ttl=%s run=%v interval=%s", inv.ReservationTTL, inv.RunWorker, inv.WorkerInterval)
	}
	if string(inv.WebhookSecret) != " rahasia dengan spasi " {
		t.Fatal("WEBHOOK_SECRET tidak boleh di-trim")
	}

	t.Setenv("OUTBOX_MAX_ATTEMPTS", "3")
	if cfg, err = Load(); err != nil || cfg.Inventory.OutboxMaxAttempts != 3 {
		t.Fatalf("OUTBOX_MAX_ATTEMPTS tidak terbaca: %v", err)
	}

	t.Setenv("RESERVATION_TTL", "0s")
	t.Setenv("RUN_WORKER", "kadang")
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "0")
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), "RESERVATION_TTL") || !strings.Contains(err.Error(), "RUN_WORKER") ||
		!strings.Contains(err.Error(), "OUTBOX_MAX_ATTEMPTS") {
		t.Fatalf("ingin error RESERVATION_TTL, RUN_WORKER, OUTBOX_MAX_ATTEMPTS, dapat %v", err)
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
