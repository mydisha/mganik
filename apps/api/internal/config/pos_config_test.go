package config

import (
	"strings"
	"testing"
	"time"
)

func clearPOSEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	for _, k := range []string{"POS_OFFLINE_LIMIT", "POS_HEARTBEAT_INTERVAL", "POS_HEARTBEAT_TIMEOUT",
		"POS_CLOCK_SKEW_TOLERANCE", "POS_SYNC_MAX_BATCH", "POS_SYNC_MAX_BODY_BYTES",
		"POS_HIGH_DISCOUNT_PERCENT", "POS_OFFLINE_VALUE_LIMIT", "POS_MAX_SERIALS_PER_TXN"} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
}

func TestLoad_POSDefaultsAndOverrides(t *testing.T) {
	clearPOSEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := POSConfig{
		OfflineLimit: 24 * time.Hour, HeartbeatInterval: 30 * time.Second, HeartbeatTimeout: 2 * time.Minute,
		ClockSkewTolerance: 5 * time.Minute, SyncMaxBatch: 500, SyncMaxBodyBytes: 5 << 20,
		HighDiscountPercent: 50, OfflineValueLimit: 10_000_000, MaxSerialsPerTxn: 2000,
	}
	if cfg.POS != want {
		t.Fatalf("default %+v", cfg.POS)
	}
	// Config literal kosong (test, router) memakai default yang sama.
	if (POSConfig{}).WithDefaults() != want {
		t.Fatal("WithDefaults tidak sama dengan default env")
	}

	t.Setenv("POS_OFFLINE_LIMIT", "12h")
	t.Setenv("POS_HEARTBEAT_TIMEOUT", "90s")
	t.Setenv("POS_SYNC_MAX_BATCH", "100")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.POS.OfflineLimit != 12*time.Hour || cfg.POS.HeartbeatTimeout != 90*time.Second || cfg.POS.SyncMaxBatch != 100 {
		t.Fatalf("override %+v", cfg.POS)
	}
}

func TestLoad_POSRejectsInvalid(t *testing.T) {
	clearPOSEnv(t)
	t.Setenv("POS_HEARTBEAT_INTERVAL", "3m")
	t.Setenv("POS_HEARTBEAT_TIMEOUT", "2m")
	t.Setenv("POS_SYNC_MAX_BATCH", "999999")
	t.Setenv("POS_OFFLINE_LIMIT", "-1h")
	t.Setenv("POS_HIGH_DISCOUNT_PERCENT", "150")
	t.Setenv("POS_OFFLINE_VALUE_LIMIT", "0")
	_, err := Load()
	if err == nil {
		t.Fatal("konfigurasi POS tidak valid harus ditolak")
	}
	for _, want := range []string{"POS_HEARTBEAT_TIMEOUT", "POS_SYNC_MAX_BATCH", "POS_OFFLINE_LIMIT",
		"POS_HIGH_DISCOUNT_PERCENT", "POS_OFFLINE_VALUE_LIMIT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error tidak menyebut %s: %v", want, err)
		}
	}
}
