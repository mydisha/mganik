package testutil

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// PgError mengambil *pgconn.PgError dari rantai error (nil kalau bukan).
//
// Error dari GORM (TranslateError aktif) tetap membawa *pgconn.PgError di
// rantainya: errors.Is(err, gorm.ErrDuplicatedKey) dan PgError(err).ConstraintName
// sama-sama bisa dipakai (dikunci TestGormErrorKeepsConstraintName). Pemetaan
// constraint → kode domain cukup memakai errors.As seperti di sini.
func PgError(err error) *pgconn.PgError {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr
	}
	return nil
}

// RequireConstraint menggagalkan test kalau err bukan pelanggaran constraint
// bernama constraint.
func RequireConstraint(t testing.TB, err error, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ingin pelanggaran constraint %s, tetapi tidak ada error", constraint)
	}
	pgErr := PgError(err)
	if pgErr == nil {
		t.Fatalf("ingin pelanggaran constraint %s, dapat error non-Postgres: %v", constraint, err)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("ingin constraint %s, dapat %q (kode %s): %s", constraint, pgErr.ConstraintName, pgErr.Code, pgErr.Message)
	}
}

// RequireSQLState menggagalkan test kalau err bukan error Postgres dengan
// SQLSTATE code.
func RequireSQLState(t testing.TB, err error, code string) {
	t.Helper()
	pgErr := PgError(err)
	if pgErr == nil {
		t.Fatalf("ingin SQLSTATE %s, dapat %v", code, err)
	}
	if pgErr.Code != code {
		t.Fatalf("ingin SQLSTATE %s, dapat %s: %s", code, pgErr.Code, pgErr.Message)
	}
}
