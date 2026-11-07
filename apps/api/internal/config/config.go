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
