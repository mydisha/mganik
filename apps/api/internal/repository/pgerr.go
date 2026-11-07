package repository

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ConstraintViolation mengembalikan nama constraint kalau err adalah
// pelanggaran constraint Postgres (unique, check, foreign key, exclusion).
// Error GORM dengan TranslateError tetap membawa *pgconn.PgError di rantainya,
// jadi fungsi ini bekerja untuk error dari GORM maupun database/sql.
//
// Service memetakan nama constraint ke error domain, misalnya
// "no_oversell" → 409 out_of_stock, "pos_txn_once" → transaksi duplikat.
func ConstraintViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName == "" {
		return "", false
	}
	switch pgErr.Code {
	case "23505", "23514", "23503", "23P01": // unique, check, fk, exclusion
		return pgErr.ConstraintName, true
	}
	return "", false
}

// IsSerializationFailure melaporkan deadlock (40P01) atau serialization
// failure (40001), dua kondisi yang aman di-retry dengan transaksi baru.
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}
