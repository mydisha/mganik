package testutil_test

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

// Database per test terisolasi, sudah termigrasi, dan dihapus setelah test.
func TestNewDB_IsolatedAndDropped(t *testing.T) {
	var firstName string
	t.Run("tulis", func(t *testing.T) {
		db := testutil.NewDB(t)
		if err := db.Raw(`SELECT current_database()`).Scan(&firstName).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`INSERT INTO audit_logs (id, actor_type, action, entity) VALUES (gen_random_uuid(), 'SYSTEM', 'x', 'x')`).Error; err != nil {
			t.Fatal(err)
		}
	})

	if firstName == "" {
		t.Fatal("nama database subtest tidak terbaca")
	}
	db := testutil.NewDB(t)
	if n := scanInt(t, db, `SELECT count(*) FROM audit_logs`); n != 0 {
		t.Fatalf("database baru melihat %d baris dari test lain", n)
	}
	if n := scanInt(t, db, `SELECT count(*) FROM movement_types`); n == 0 {
		t.Fatal("database dari template belum termigrasi")
	}
	if n := scanInt(t, db, `SELECT count(*) FROM pg_database WHERE datname = ?`, firstName); n != 0 {
		t.Fatalf("database %s tidak dihapus setelah test selesai", firstName)
	}
}

func scanInt(t *testing.T, db *gorm.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("%v\nquery: %s", err, query)
	}
	return n
}

// GORM dengan TranslateError mengembalikan sentinel GORM sekaligus tetap
// membawa *pgconn.PgError beserta nama constraint di rantai error.
func TestGormErrorKeepsConstraintName(t *testing.T) {
	db := testutil.NewDB(t)
	q := `INSERT INTO idempotency_keys (id, scope, key, request_hash, expires_at)
	      VALUES (gen_random_uuid(), 's', 'k', '\x00', now() + interval '1 day')`
	if err := db.Exec(q).Error; err != nil {
		t.Fatal(err)
	}
	err := db.Exec(q).Error
	if !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("ingin gorm.ErrDuplicatedKey, dapat %v", err)
	}
	testutil.RequireConstraint(t, err, "idempotency_key_once")

	err = db.Exec(`INSERT INTO idempotency_keys (id, scope, key, request_hash, state, expires_at)
	               VALUES (gen_random_uuid(), 's', 'k2', '\x00', 'SALAH', now())`).Error
	if !errors.Is(err, gorm.ErrCheckConstraintViolated) {
		t.Fatalf("ingin gorm.ErrCheckConstraintViolated, dapat %v", err)
	}
	testutil.RequireConstraint(t, err, "idempotency_state_valid")
}

func TestNewEmptyDB_HasNoSchema(t *testing.T) {
	db := testutil.NewEmptyDB(t)
	if n := scanInt(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'`); n != 0 {
		t.Fatalf("database kosong berisi %d tabel", n)
	}
}

// Banyak database dibuat paralel dari template yang sama tanpa error.
func TestNewDB_Parallel(t *testing.T) {
	for i := range 6 {
		t.Run("db", func(t *testing.T) {
			t.Parallel()
			db := testutil.NewDB(t)
			var ok int
			if err := db.Raw(`SELECT ?::int`, i).Scan(&ok).Error; err != nil || ok != i {
				t.Fatalf("query gagal: %v", err)
			}
		})
	}
}
