// Package migrations menyimpan file migrasi goose di dalam binary.
package migrations

import "embed"

// FS berisi semua file *.sql di folder ini.
//
//go:embed *.sql
var FS embed.FS
