// Package model berisi struct GORM yang mencerminkan tabel hasil migrasi goose
// (apps/api/migrations). Skema dikelola migrasi SQL, bukan AutoMigrate; struct
// di sini hanya memetakan kolom. Model tidak dipakai sebagai DTO API, jadi
// tidak punya tag JSON.
package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// Nilai actor_type yang sama di semua tabel ledger/event.
const (
	ActorUser     = "USER"
	ActorDevice   = "DEVICE"
	ActorSystem   = "SYSTEM"
	ActorConsumer = "CONSUMER"
)

// JSON memetakan kolom jsonb apa adanya. nil ditulis sebagai NULL; untuk kolom
// NOT NULL, tag `default` membuat GORM menghilangkan kolom itu dari INSERT
// sehingga default DB ('{}' atau '[]') yang dipakai.
type JSON json.RawMessage

// Value mengirim JSON sebagai teks; Postgres mem-parse dan memvalidasinya.
func (j JSON) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return string(j), nil
}

// Scan menyalin byte dari driver (buffer driver bisa dipakai ulang).
func (j *JSON) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*j = nil
	case []byte:
		*j = append(JSON(nil), v...)
	case string:
		*j = JSON(v)
	default:
		return fmt.Errorf("model.JSON: tipe %T tidak didukung", src)
	}
	return nil
}

func (JSON) GormDataType() string { return "jsonb" }

// MustJSON meng-encode v menjadi JSON. Hanya untuk nilai yang pasti bisa
// di-encode (map/struct literal); panic kalau gagal.
func MustJSON(v any) JSON {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("model.MustJSON: %v", err))
	}
	return JSON(b)
}

// StringArray memetakan kolom text[]. nil ditulis sebagai array kosong.
type StringArray []string

// Value meng-encode ke literal array Postgres ({a,"b c"}) dengan quoting pgx.
func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return "{}", nil
	}
	buf, err := pgtype.NewMap().Encode(pgtype.TextArrayOID, pgtype.TextFormatCode, []string(a), nil)
	if err != nil {
		return nil, fmt.Errorf("model.StringArray: %w", err)
	}
	return string(buf), nil
}

func (a *StringArray) Scan(src any) error {
	return pgtype.NewMap().SQLScanner((*[]string)(a)).Scan(src)
}

func (StringArray) GormDataType() string { return "text[]" }
