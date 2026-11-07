// Package database membuka koneksi GORM ke PostgreSQL dan menjalankan migrasi.
package database

import (
	"context"
	"fmt"
	"io"
	stdlog "log"
	"log/slog"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"mganik/api/internal/config"
)

// Open membuka pool koneksi dan menunggu sampai database menjawab ping,
// dibatasi ConnectTimeout.
func Open(ctx context.Context, cfg config.DatabaseConfig, log *slog.Logger) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.URL), &gorm.Config{
		// Transaksi dibuka eksplisit di layer service; insert/update tunggal tidak
		// perlu dibungkus transaksi tambahan.
		SkipDefaultTransaction: true,
		TranslateError:         true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
		Logger:                 NewGormLogger(os.Stdout),
	})
	if err != nil {
		return nil, fmt.Errorf("membuka koneksi database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("mengambil pool database: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	for {
		err = sqlDB.PingContext(pingCtx)
		if err == nil {
			return db, nil
		}
		select {
		case <-pingCtx.Done():
			_ = sqlDB.Close()
			return nil, fmt.Errorf("database tidak bisa dihubungi dalam %s: %w", cfg.ConnectTimeout, err)
		case <-time.After(500 * time.Millisecond):
			log.Debug("menunggu database", "error", err)
		}
	}
}

// NewGormLogger membuat logger GORM untuk slow query dan error SQL.
// ParameterizedQueries: log hanya memuat SQL dengan placeholder, tanpa nilai
// parameter, sehingga hash password, claim hash, token, dan PII tidak pernah
// masuk log.
func NewGormLogger(w io.Writer) logger.Interface {
	return logger.New(stdlog.New(w, "", stdlog.LstdFlags), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  logger.Warn,
		IgnoreRecordNotFoundError: true,
		ParameterizedQueries:      true,
		Colorful:                  false,
	})
}

// Close menutup pool koneksi.
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
