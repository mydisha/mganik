// Package config membaca konfigurasi dari environment dan menolak start
// kalau ada nilai yang tidak valid.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

type Config struct {
	AppEnv         string
	HTTPAddr       string
	LogLevel       slog.Level
	LogFormat      string // json | text
	TrustedProxies []string
	MaxBodyBytes   int64
	ShutdownGrace  time.Duration
	Database       DatabaseConfig
	// ClaimPepper adalah kunci HMAC untuk serial_units.claim_secret_hash
	// (HMAC-SHA256(pepper, unit_id ‖ PIN)). Disimpan di env/secret manager,
	// tidak pernah di DB.
	ClaimPepper []byte
	Seed        SeedConfig
	// Serialization: verifikasi publik dan klaim loyalty (Fase 2). Nilai nol
	// berarti default (lihat SerializationConfig.WithDefaults).
	Serialization SerializationConfig
}

// SerializationConfig mengatur endpoint publik verifikasi dan klaim poin.
type SerializationConfig struct {
	// LoyaltyPendingPeriod: masa tunggu poin untuk unit yang diklaim saat
	// masih tercatat di mitra (AT_PARTNER). LOYALTY_PENDING_PERIOD, default 168h.
	LoyaltyPendingPeriod time.Duration
	// ClaimReviewThreshold: skor risiko (0-100) yang membuat klaim menunggu
	// review manusia. CLAIM_REVIEW_THRESHOLD, default 50.
	ClaimReviewThreshold int
	// LoyaltyPointsPerUnit: poin per unit kalau produk tidak punya atribut
	// loyalty_points. LOYALTY_POINTS_PER_UNIT, default 10.
	LoyaltyPointsPerUnit int
	// VerifyRatePerMinute dan ClaimRatePerMinute: rate limit per IP untuk
	// endpoint publik. PUBLIC_VERIFY_RATE_PER_MIN (60), PUBLIC_CLAIM_RATE_PER_MIN (20).
	VerifyRatePerMinute int
	ClaimRatePerMinute  int
	// ClaimRatePerConsumerPerHour: batas klaim per akun konsumen di samping
	// batas per IP. PUBLIC_CLAIM_RATE_PER_CONSUMER_HOUR, default 10.
	ClaimRatePerConsumerPerHour int
}

// WithDefaults mengisi nilai nol dengan default.
func (c SerializationConfig) WithDefaults() SerializationConfig {
	if c.LoyaltyPendingPeriod <= 0 {
		c.LoyaltyPendingPeriod = 7 * 24 * time.Hour
	}
	if c.ClaimReviewThreshold <= 0 {
		c.ClaimReviewThreshold = 50
	}
	if c.LoyaltyPointsPerUnit <= 0 {
		c.LoyaltyPointsPerUnit = 10
	}
	if c.VerifyRatePerMinute <= 0 {
		c.VerifyRatePerMinute = 60
	}
	if c.ClaimRatePerMinute <= 0 {
		c.ClaimRatePerMinute = 20
	}
	if c.ClaimRatePerConsumerPerHour <= 0 {
		c.ClaimRatePerConsumerPerHour = 10
	}
	return c
}

// SeedConfig mengatur data demo (`api seed`, `api migrate --seed`).
type SeedConfig struct {
	// Demo harus true (SEED_DEMO=true) supaya seed mau berjalan.
	Demo bool
	// DemoPassword adalah password semua akun demo.
	DemoPassword string
	// AllowProduction (SEED_DEMO_ALLOW_PRODUCTION=true) mengizinkan data dan
	// kredensial demo publik saat APP_ENV=production. Hanya untuk stack demo
	// lokal (compose); production sungguhan tidak pernah menyalakannya.
	AllowProduction bool
}

const (
	// MinClaimPepperBytes adalah panjang minimum CLAIM_PEPPER di production.
	MinClaimPepperBytes = 32
	// MinDemoPasswordLen adalah panjang minimum SEED_DEMO_PASSWORD.
	MinDemoPasswordLen = 12

	// DemoClaimPepper adalah pepper publik untuk stack demo. Nilai yang sama
	// dipakai default compose.yaml, Makefile, dan .env.example (dijaga test),
	// sehingga `make api` dan stack compose di volume yang sama memverifikasi
	// PIN dengan pepper yang sama. Di production ditolak kecuali
	// SEED_DEMO_ALLOW_PRODUCTION=true.
	DemoClaimPepper = "demo-claim-pepper-ganti-sebelum-dipakai-sungguhan"
	// DefaultDemoPassword dipakai kalau SEED_DEMO_PASSWORD kosong (dicatat di
	// README). Aturan production sama dengan DemoClaimPepper.
	DefaultDemoPassword = "mganik-demo-2026"
)

// DemoOverrideActive: production berjalan dengan izin data/kredensial demo.
// serve dan migrate menulis WARN saat start kalau ini true.
func (c Config) DemoOverrideActive() bool {
	return c.IsProduction() && c.Seed.AllowProduction
}

type DatabaseConfig struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnectTimeout  time.Duration
}

func (c Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// Load membaca environment. Semua kesalahan dikumpulkan supaya operator
// melihat daftar lengkap dalam satu kali start.
func Load() (Config, error) {
	var errs []error
	r := reader{errs: &errs}

	cfg := Config{
		AppEnv:         r.str("APP_ENV", EnvDevelopment),
		HTTPAddr:       r.str("HTTP_ADDR", ":8080"),
		LogFormat:      r.str("LOG_FORMAT", "json"),
		TrustedProxies: r.list("TRUSTED_PROXIES"),
		MaxBodyBytes:   int64(r.positiveInt("HTTP_MAX_BODY_BYTES", 1<<20)),
		ShutdownGrace:  r.duration("SHUTDOWN_GRACE", 20*time.Second),
		Database: DatabaseConfig{
			URL:             r.str("DATABASE_URL", ""),
			MaxOpenConns:    r.positiveInt("DB_MAX_OPEN_CONNS", 20),
			MaxIdleConns:    r.positiveInt("DB_MAX_IDLE_CONNS", 10),
			ConnMaxLifetime: r.duration("DB_CONN_MAX_LIFETIME", 30*time.Minute),
			ConnectTimeout:  r.duration("DB_CONNECT_TIMEOUT", 10*time.Second),
		},
		Seed: SeedConfig{
			Demo:            r.boolean("SEED_DEMO", false),
			DemoPassword:    r.str("SEED_DEMO_PASSWORD", DefaultDemoPassword),
			AllowProduction: r.boolean("SEED_DEMO_ALLOW_PRODUCTION", false),
		},
		Serialization: SerializationConfig{
			LoyaltyPendingPeriod: r.duration("LOYALTY_PENDING_PERIOD", 7*24*time.Hour),
			ClaimReviewThreshold: r.positiveInt("CLAIM_REVIEW_THRESHOLD", 50),
			LoyaltyPointsPerUnit: r.positiveInt("LOYALTY_POINTS_PER_UNIT", 10),
			VerifyRatePerMinute:  r.positiveInt("PUBLIC_VERIFY_RATE_PER_MIN", 60),
			ClaimRatePerMinute:   r.positiveInt("PUBLIC_CLAIM_RATE_PER_MIN", 20),

			ClaimRatePerConsumerPerHour: r.positiveInt("PUBLIC_CLAIM_RATE_PER_CONSUMER_HOUR", 10),
		},
	}
	if cfg.Serialization.ClaimReviewThreshold > 100 {
		errs = append(errs, fmt.Errorf("CLAIM_REVIEW_THRESHOLD harus 1-100 (sekarang %d)", cfg.Serialization.ClaimReviewThreshold))
	}

	// CLAIM_PEPPER tidak di-trim: spasi di dalam secret tetap bagian dari kunci.
	pepper, hasPepper := os.LookupEnv("CLAIM_PEPPER")
	switch {
	case hasPepper && pepper != "":
		cfg.ClaimPepper = []byte(pepper)
	case cfg.AppEnv != EnvProduction:
		cfg.ClaimPepper = []byte(DemoClaimPepper)
	}
	if len(cfg.Seed.DemoPassword) < MinDemoPasswordLen {
		errs = append(errs, fmt.Errorf("SEED_DEMO_PASSWORD minimal %d karakter", MinDemoPasswordLen))
	}
	if cfg.AppEnv == EnvProduction {
		if len(cfg.ClaimPepper) < MinClaimPepperBytes {
			errs = append(errs, fmt.Errorf("CLAIM_PEPPER wajib diisi minimal %d byte di production (sekarang %d byte)",
				MinClaimPepperBytes, len(cfg.ClaimPepper)))
		}
		// Nilai demo ada di repo publik: di production hanya boleh dengan izin
		// eksplisit (stack demo lokal).
		if !cfg.Seed.AllowProduction {
			const hint = "set SEED_DEMO_ALLOW_PRODUCTION=true hanya untuk stack demo lokal"
			if string(cfg.ClaimPepper) == DemoClaimPepper {
				errs = append(errs, fmt.Errorf("CLAIM_PEPPER memakai nilai demo publik di production (%s)", hint))
			}
			if cfg.Seed.Demo {
				errs = append(errs, fmt.Errorf("SEED_DEMO=true ditolak di production (%s)", hint))
				if cfg.Seed.DemoPassword == DefaultDemoPassword {
					errs = append(errs, fmt.Errorf("SEED_DEMO_PASSWORD memakai password demo default di production (%s)", hint))
				}
			}
		}
	}

	switch cfg.AppEnv {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("APP_ENV harus development, test, atau production (sekarang %q)", cfg.AppEnv))
	}
	switch cfg.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT harus json atau text (sekarang %q)", cfg.LogFormat))
	}
	if cfg.Database.URL == "" {
		errs = append(errs, errors.New("DATABASE_URL wajib diisi"))
	}
	if cfg.Database.MaxIdleConns > cfg.Database.MaxOpenConns {
		errs = append(errs, errors.New("DB_MAX_IDLE_CONNS tidak boleh lebih besar dari DB_MAX_OPEN_CONNS"))
	}

	level := r.str("LOG_LEVEL", "info")
	if err := cfg.LogLevel.UnmarshalText([]byte(level)); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL tidak dikenal: %q", level))
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("konfigurasi tidak valid:\n%w", errors.Join(errs...))
	}
	return cfg, nil
}

type reader struct{ errs *[]error }

func (r reader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (r reader) list(key string) []string {
	raw := r.str(key, "")
	if raw == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (r reader) positiveInt(key string, def int) int {
	raw := r.str(key, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		*r.errs = append(*r.errs, fmt.Errorf("%s harus bilangan bulat positif (sekarang %q)", key, raw))
		return def
	}
	return n
}

func (r reader) boolean(key string, def bool) bool {
	raw := r.str(key, "")
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		*r.errs = append(*r.errs, fmt.Errorf("%s harus true atau false (sekarang %q)", key, raw))
		return def
	}
	return b
}

func (r reader) duration(key string, def time.Duration) time.Duration {
	raw := r.str(key, "")
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		*r.errs = append(*r.errs, fmt.Errorf("%s harus durasi positif seperti 30s atau 5m (sekarang %q)", key, raw))
		return def
	}
	return d
}
