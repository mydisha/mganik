package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"gorm.io/gorm"

	"mganik/api/migrations"
)

// Migrate menerapkan semua migrasi yang belum jalan. Session lock Postgres
// memastikan dua proses migrate yang start bersamaan tidak saling tabrak.
func Migrate(ctx context.Context, db *gorm.DB, log *slog.Logger) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("mengambil pool database: %w", err)
	}

	provider, err := NewMigrationProvider(sqlDB)
	if err != nil {
		return err
	}

	results, err := provider.Up(ctx)
	for _, r := range results {
		log.Info("migrasi", "hasil", r.String())
	}
	if err != nil {
		return fmt.Errorf("menjalankan migrasi: %w", err)
	}

	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("membaca versi skema: %w", err)
	}
	log.Info("skema database terbaru", "versi", version, "diterapkan", len(results))
	return nil
}

// NewMigrationProvider membuat provider goose atas migrasi yang di-embed,
// dengan session lock Postgres. Dipakai Migrate dan test (Down/Up).
//
// Provider sengaja tidak di-Close oleh pemanggil yang berbagi *sql.DB dengan
// GORM: Close milik goose akan menutup pool itu.
func NewMigrationProvider(sqlDB *sql.DB) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("membuat session locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return nil, fmt.Errorf("menyiapkan migrasi: %w", err)
	}
	return provider, nil
}
