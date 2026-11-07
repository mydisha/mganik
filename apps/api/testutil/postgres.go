// Package testutil menyiapkan PostgreSQL sungguhan untuk test.
//
// Pola pakai (satu server per package test, satu database per test):
//
//	func TestMain(m *testing.M) { testutil.Main(m) }
//
//	func TestX(t *testing.T) {
//		db := testutil.NewDB(t) // database baru, sudah dimigrasi, dihapus saat test selesai
//		...
//	}
//
// Server dijalankan lewat testcontainers (postgres:17-alpine, data di tmpfs)
// SEKALI per package. Migrasi diterapkan sekali ke database template; setiap
// NewDB membuat salinan lewat CREATE DATABASE ... TEMPLATE, jadi test terisolasi
// dan cepat, termasuk test konkurensi yang butuh banyak koneksi.
//
// Tanpa Docker, set TEST_DATABASE_URL ke server Postgres 17 dengan user yang
// boleh CREATE DATABASE, misal:
//
//	TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // driver database/sql "pgx" untuk koneksi admin
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"mganik/api/internal/config"
	"mganik/api/internal/database"
)

const (
	// EnvDatabaseURL: server Postgres yang sudah ada, dipakai alih-alih Docker.
	EnvDatabaseURL = "TEST_DATABASE_URL"
	// Image sama dengan compose.yaml.
	postgresImage = "postgres:17-alpine"
	// Koneksi maksimal per database test; cukup untuk test 100 goroutine.
	maxOpenConns = 50
)

// Postgres adalah satu server Postgres beserta database template termigrasi.
type Postgres struct {
	admin     *sql.DB
	baseURL   *url.URL
	template  string
	container *tcpostgres.PostgresContainer

	// CREATE DATABASE ... TEMPLATE gagal kalau template sedang disalin proses
	// lain; salinan dalam satu package diserialkan.
	createMu sync.Mutex
}

// Start menyalakan server (atau memakai TEST_DATABASE_URL), membuat database
// template, dan menerapkan semua migrasi ke template itu.
func Start(ctx context.Context) (*Postgres, error) {
	p := &Postgres{}
	raw := strings.TrimSpace(os.Getenv(EnvDatabaseURL))
	if raw == "" {
		ctr, err := tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("postgres"),
			tcpostgres.WithUsername("postgres"),
			tcpostgres.WithPassword("postgres"),
			tcpostgres.BasicWaitStrategies(),
			testcontainers.WithTmpfs(map[string]string{"/var/lib/postgresql/data": "rw"}),
			// Data test boleh hilang; matikan fsync supaya cepat.
			testcontainers.WithCmd("postgres",
				"-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off",
				"-c", "max_connections=300"),
		)
		if err != nil {
			if ctr != nil {
				_ = testcontainers.TerminateContainer(ctr)
			}
			return nil, fmt.Errorf("menyalakan postgres via testcontainers (Docker tersedia? atau set %s): %w", EnvDatabaseURL, err)
		}
		p.container = ctr
		raw, err = ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			_ = p.Close(ctx)
			return nil, fmt.Errorf("connection string container: %w", err)
		}
	}

	base, err := url.Parse(raw)
	if err != nil {
		_ = p.Close(ctx)
		return nil, fmt.Errorf("%s tidak valid: %w", EnvDatabaseURL, err)
	}
	p.baseURL = base
	p.admin, err = sql.Open("pgx", raw)
	if err != nil {
		_ = p.Close(ctx)
		return nil, fmt.Errorf("membuka koneksi admin: %w", err)
	}
	if err := p.admin.PingContext(ctx); err != nil {
		_ = p.Close(ctx)
		return nil, fmt.Errorf("ping postgres test: %w", err)
	}

	p.template = "mganik_tpl_" + randomSuffix()
	if err := p.createDatabase(ctx, p.template, ""); err != nil {
		_ = p.Close(ctx)
		return nil, err
	}
	db, err := p.open(ctx, p.template)
	if err != nil {
		_ = p.Close(ctx)
		return nil, err
	}
	err = database.Migrate(ctx, db, discardLogger())
	closeErr := database.Close(db)
	if err != nil {
		_ = p.Close(ctx)
		return nil, fmt.Errorf("migrasi database template: %w", err)
	}
	if closeErr != nil {
		_ = p.Close(ctx)
		return nil, closeErr
	}
	return p, nil
}

// Close menghapus database template dan mematikan container (kalau ada).
func (p *Postgres) Close(ctx context.Context) error {
	var errs []error
	if p.admin != nil {
		if p.template != "" && p.container == nil {
			// Server milik orang lain (TEST_DATABASE_URL): bersihkan template kita.
			if _, err := p.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(p.template)+" WITH (FORCE)"); err != nil {
				errs = append(errs, err)
			}
		}
		errs = append(errs, p.admin.Close())
	}
	if p.container != nil {
		errs = append(errs, testcontainers.TerminateContainer(p.container))
	}
	return errors.Join(errs...)
}

// NewDB membuat database baru dari template (skema terbaru, lookup terisi,
// tanpa data demo) dan menghapusnya saat test selesai.
func (p *Postgres) NewDB(t testing.TB) *gorm.DB {
	t.Helper()
	return p.openForTest(t, p.CreateDB(t, true))
}

// NewEmptyDB membuat database kosong tanpa migrasi (untuk test migrasi).
func (p *Postgres) NewEmptyDB(t testing.TB) *gorm.DB {
	t.Helper()
	return p.openForTest(t, p.CreateDB(t, false))
}

// CreateDB membuat database (salinan template kalau fromTemplate) dan
// mengembalikan URL-nya, untuk test yang butuh pool atau driver sendiri.
// Database dihapus saat test selesai.
func (p *Postgres) CreateDB(t testing.TB, fromTemplate bool) string {
	t.Helper()
	ctx := context.Background()
	name := "mganik_t_" + randomSuffix()
	tpl := ""
	if fromTemplate {
		tpl = p.template
	}
	if err := p.createDatabase(ctx, name, tpl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := p.admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quoteIdent(name)+" WITH (FORCE)"); err != nil {
			t.Errorf("menghapus database test %s: %v", name, err)
		}
	})
	return p.urlFor(name)
}

func (p *Postgres) openForTest(t testing.TB, dbURL string) *gorm.DB {
	t.Helper()
	u, _ := url.Parse(dbURL)
	db, err := p.open(context.Background(), strings.TrimPrefix(u.Path, "/"))
	if err != nil {
		t.Fatal(err)
	}
	// Ditutup sebelum DROP DATABASE (Cleanup berjalan LIFO).
	t.Cleanup(func() { _ = database.Close(db) })
	return db
}

func (p *Postgres) open(ctx context.Context, name string) (*gorm.DB, error) {
	db, err := database.Open(ctx, config.DatabaseConfig{
		URL:             p.urlFor(name),
		MaxOpenConns:    maxOpenConns,
		MaxIdleConns:    maxOpenConns,
		ConnMaxLifetime: 5 * time.Minute,
		ConnectTimeout:  15 * time.Second,
	}, discardLogger())
	if err != nil {
		return nil, err
	}
	// Test sengaja memicu pelanggaran constraint; jangan banjiri output.
	db.Logger = logger.Default.LogMode(logger.Silent)
	return db, nil
}

func (p *Postgres) createDatabase(ctx context.Context, name, template string) error {
	stmt := "CREATE DATABASE " + quoteIdent(name)
	if template != "" {
		stmt += " TEMPLATE " + quoteIdent(template)
	}
	p.createMu.Lock()
	defer p.createMu.Unlock()
	var err error
	for attempt := range 20 {
		if _, err = p.admin.ExecContext(ctx, stmt); err == nil {
			return nil
		}
		// Template sedang dipakai koneksi lain (misal autovacuum): coba lagi.
		if !strings.Contains(err.Error(), "is being accessed by other users") {
			break
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	return fmt.Errorf("membuat database %s: %w", name, err)
}

func (p *Postgres) urlFor(name string) string {
	u := *p.baseURL
	u.Path = "/" + name
	return u.String()
}

// ------------------------------------------------------------------ helper package-level

var shared *Postgres

// Main dipanggil dari TestMain: menyalakan Postgres sekali untuk package,
// menjalankan test, lalu membersihkan. Gagal menyalakan Postgres = test gagal
// (bukan dilewati), karena test DB adalah bagian wajib.
func Main(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	p, err := Start(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testutil:", err)
		os.Exit(1)
	}
	shared = p
	code := m.Run()
	if err := p.Close(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "testutil: membersihkan postgres:", err)
	}
	os.Exit(code)
}

// Shared mengembalikan server milik package ini (setelah Main).
func Shared(t testing.TB) *Postgres {
	t.Helper()
	if shared == nil {
		t.Fatal("testutil: Postgres belum dinyalakan; tambahkan `func TestMain(m *testing.M) { testutil.Main(m) }`")
	}
	return shared
}

// NewDB: database termigrasi baru dari server package ini.
func NewDB(t testing.TB) *gorm.DB {
	t.Helper()
	return Shared(t).NewDB(t)
}

// NewEmptyDB: database kosong baru dari server package ini.
func NewEmptyDB(t testing.TB) *gorm.DB {
	t.Helper()
	return Shared(t).NewEmptyDB(t)
}

// ------------------------------------------------------------------ util

func randomSuffix() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
