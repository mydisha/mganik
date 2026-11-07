// Package repository berisi akses database per domain. Repository tidak
// tahu HTTP; ia menerima *gorm.DB (atau transaksi) dan mengembalikan data.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

type HealthRepository struct {
	db *gorm.DB
}

func NewHealthRepository(db *gorm.DB) *HealthRepository {
	return &HealthRepository{db: db}
}

// Ping memastikan koneksi ke database hidup.
func (r *HealthRepository) Ping(ctx context.Context) error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return fmt.Errorf("mengambil pool database: %w", err)
	}
	return sqlDB.PingContext(ctx)
}

// SchemaVersion mengembalikan versi migrasi goose terakhir yang diterapkan.
func (r *HealthRepository) SchemaVersion(ctx context.Context) (int64, error) {
	var version sql.NullInt64
	err := r.db.WithContext(ctx).
		Raw("SELECT MAX(version_id) FROM goose_db_version WHERE is_applied").
		Scan(&version).Error
	if err != nil {
		return 0, fmt.Errorf("membaca versi skema: %w", err)
	}
	if !version.Valid {
		return 0, errors.New("belum ada migrasi yang diterapkan")
	}
	return version.Int64, nil
}
