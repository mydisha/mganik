package config

import (
	"strings"
	"testing"
	"time"
)

func clearSerializationEnv(t *testing.T) {
	t.Helper()
	clearEnv(t)
	for _, k := range []string{"LOYALTY_PENDING_PERIOD", "CLAIM_REVIEW_THRESHOLD", "LOYALTY_POINTS_PER_UNIT",
		"PUBLIC_VERIFY_RATE_PER_MIN", "PUBLIC_CLAIM_RATE_PER_MIN", "PUBLIC_CLAIM_RATE_PER_CONSUMER_HOUR"} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
}

func TestLoad_SerializationDefaultsAndOverrides(t *testing.T) {
	clearSerializationEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := SerializationConfig{
		LoyaltyPendingPeriod: 7 * 24 * time.Hour, ClaimReviewThreshold: 50, LoyaltyPointsPerUnit: 10,
		VerifyRatePerMinute: 60, ClaimRatePerMinute: 20, ClaimRatePerConsumerPerHour: 10,
	}
	if cfg.Serialization != want {
		t.Fatalf("default %+v", cfg.Serialization)
	}
	// Config literal kosong (test, router) memakai default yang sama.
	if (SerializationConfig{}).WithDefaults() != want {
		t.Fatal("WithDefaults tidak sama dengan default env")
	}

	t.Setenv("LOYALTY_PENDING_PERIOD", "72h")
	t.Setenv("CLAIM_REVIEW_THRESHOLD", "70")
	t.Setenv("PUBLIC_VERIFY_RATE_PER_MIN", "5")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Serialization.LoyaltyPendingPeriod != 72*time.Hour || cfg.Serialization.ClaimReviewThreshold != 70 ||
		cfg.Serialization.VerifyRatePerMinute != 5 {
		t.Fatalf("override %+v", cfg.Serialization)
	}
}

func TestLoad_SerializationRejectsInvalid(t *testing.T) {
	clearSerializationEnv(t)
	t.Setenv("CLAIM_REVIEW_THRESHOLD", "101")
	t.Setenv("LOYALTY_POINTS_PER_UNIT", "-1")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CLAIM_REVIEW_THRESHOLD") || !strings.Contains(err.Error(), "LOYALTY_POINTS_PER_UNIT") {
		t.Fatalf("error %v", err)
	}
}
