package repository_test

import (
	"context"
	"testing"

	"mganik/api/internal/database"
	"mganik/api/internal/repository"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func TestHealthRepository_SchemaVersion(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewHealthRepository(testutil.NewDB(t))
	if err := repo.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := repo.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := testutil.NewEmptyDB(t).DB()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := database.NewMigrationProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	sources := provider.ListSources()
	if want := sources[len(sources)-1].Version; got != want {
		t.Fatalf("versi skema %d, ingin %d", got, want)
	}
}

func TestHealthRepository_SchemaVersionWithoutMigrations(t *testing.T) {
	repo := repository.NewHealthRepository(testutil.NewEmptyDB(t))
	if _, err := repo.SchemaVersion(context.Background()); err == nil {
		t.Fatal("database tanpa migrasi seharusnya error")
	}
}
