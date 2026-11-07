package database_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"mganik/api/internal/config"
	"mganik/api/internal/database"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func appliedVersions(t *testing.T, ctx context.Context, url string) int {
	t.Helper()
	db, err := database.Open(ctx, dbConfig(url), quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(db)
	var n int
	if err := db.Raw(`SELECT count(*) FROM goose_db_version WHERE is_applied AND version_id > 0`).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func dbConfig(url string) config.DatabaseConfig {
	return config.DatabaseConfig{URL: url, MaxOpenConns: 4, MaxIdleConns: 4, ConnMaxLifetime: time.Minute, ConnectTimeout: 10 * time.Second}
}

// Migrate yang dijalankan ulang tidak menerapkan apa pun lagi.
func TestMigrate_Idempotent(t *testing.T) {
	ctx := context.Background()
	url := testutil.Shared(t).CreateDB(t, false)
	for range 2 {
		db, err := database.Open(ctx, dbConfig(url), quiet())
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Migrate(ctx, db, quiet()); err != nil {
			t.Fatal(err)
		}
		_ = database.Close(db)
	}
	sqlDB, _ := testutil.NewEmptyDB(t).DB()
	provider, err := database.NewMigrationProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := appliedVersions(t, ctx, url), len(provider.ListSources()); got != want {
		t.Fatalf("%d migrasi tercatat, ingin %d", got, want)
	}
}

// Dua proses migrate yang start bersamaan (misal dua replika) tidak saling
// tabrak berkat session lock: keduanya sukses dan setiap migrasi tercatat sekali.
func TestMigrate_ConcurrentProcesses(t *testing.T) {
	ctx := context.Background()
	url := testutil.Shared(t).CreateDB(t, false)
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() {
			db, err := database.Open(ctx, dbConfig(url), quiet())
			if err != nil {
				errs[i] = err
				return
			}
			defer database.Close(db)
			errs[i] = database.Migrate(ctx, db, quiet())
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
	}
	sqlDB, _ := testutil.NewEmptyDB(t).DB()
	provider, _ := database.NewMigrationProvider(sqlDB)
	if got, want := appliedVersions(t, ctx, url), len(provider.ListSources()); got != want {
		t.Fatalf("%d migrasi tercatat, ingin %d", got, want)
	}
}
