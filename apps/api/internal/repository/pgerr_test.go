package repository_test

import (
	"errors"
	"fmt"
	"testing"

	"mganik/api/internal/repository"
	"mganik/api/testutil"
)

func TestConstraintViolation(t *testing.T) {
	db := testutil.NewDB(t)

	// Database template berisi lookup dari migrasi (tanpa seed demo).
	// CHECK bernama: nama constraint harus bisa diambil dari error GORM.
	err := db.Exec(`UPDATE movement_types SET physical_sign = 5 WHERE code = 'RECEIPT'`).Error
	if name, ok := repository.ConstraintViolation(err); !ok || name != "movement_physical_sign_valid" {
		t.Fatalf("ingin movement_physical_sign_valid, dapat %q ok=%v err=%v", name, ok, err)
	}

	// UNIQUE bernama.
	err = db.Exec(`INSERT INTO packaging_levels (code, name, rank)
	               SELECT 'BARU', 'Baru', rank FROM packaging_levels WHERE code = 'UNIT'`).Error
	if name, ok := repository.ConstraintViolation(err); !ok || name != "packaging_level_rank_once" {
		t.Fatalf("ingin packaging_level_rank_once, dapat %q ok=%v err=%v", name, ok, err)
	}

	// FK.
	err = db.Exec(`INSERT INTO packaging_specs (id, product_id, child_level, parent_level, qty_per_parent)
	               VALUES (gen_random_uuid(), gen_random_uuid(), 'UNIT', 'CARTON', 12)`).Error
	if _, ok := repository.ConstraintViolation(err); !ok {
		t.Fatalf("ingin pelanggaran FK, dapat %v", err)
	}

	// Error bukan constraint.
	for _, e := range []error{nil, errors.New("biasa"), fmt.Errorf("bungkus: %w", errors.New("x"))} {
		if _, ok := repository.ConstraintViolation(e); ok {
			t.Fatalf("%v bukan pelanggaran constraint", e)
		}
	}
	syntaxErr := db.Exec(`SELEC 1`).Error
	if _, ok := repository.ConstraintViolation(syntaxErr); ok {
		t.Fatal("syntax error bukan pelanggaran constraint")
	}
	if repository.IsSerializationFailure(syntaxErr) {
		t.Fatal("syntax error bukan serialization failure")
	}
}
