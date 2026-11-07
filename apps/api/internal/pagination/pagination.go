// Package pagination menyediakan pagination keyset bersama untuk endpoint list
// (PLANNING §4.5): cursor opaque, limit default 50 maksimal 200, dan respons
// `{items, next_cursor}`.
//
// Cursor adalah base64url (tanpa padding) dari JSON `{"k": kind, "v": key}`.
// kind mengikat cursor ke satu endpoint/urutan, jadi cursor dari endpoint lain
// ditolak. key adalah nilai kolom urut baris terakhir halaman sebelumnya,
// misal TimeID untuk urutan `(created_at, id)`. Cursor hanya posisi, bukan
// izin: query tetap menerapkan filter scope pemanggil.
//
// Pola pakai di repository: ambil Limit+1 baris dengan kondisi keyset, lalu
// panggil Trim untuk memotong dan membuat next_cursor.
package pagination

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/apperror"
)

const (
	// DefaultLimit dipakai kalau query `limit` kosong.
	DefaultLimit = 50
	// MaxLimit adalah batas atas `limit`; nilai di atasnya ditolak 422.
	MaxLimit = 200
	// MaxCursorLen membatasi panjang cursor sebelum di-decode.
	MaxCursorLen = 1024

	// FieldLimit dan FieldCursor adalah nama query param di FieldError.
	FieldLimit  = "limit"
	FieldCursor = "cursor"
)

// Page adalah bentuk respons list. NextCursor nil = halaman terakhir.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// Request adalah parameter pagination yang sudah divalidasi.
type Request struct {
	Limit int
	// HasCursor true kalau klien mengirim cursor (key sudah terisi).
	HasCursor bool
}

// Validator diimplementasikan key cursor yang perlu cek isi setelah decode
// (misal ID tidak boleh nil).
type Validator interface {
	Validate() error
}

// ErrInvalidCursor adalah penyebab internal untuk cursor yang rusak.
var ErrInvalidCursor = errors.New("cursor tidak valid")

type envelope struct {
	Kind string          `json:"k"`
	Key  json.RawMessage `json:"v"`
}

// Parse memvalidasi query `limit` dan `cursor`. Kalau cursor ada, isinya
// di-decode ke key (pointer ke struct key milik endpoint kind). Semua
// kesalahan dikembalikan sekaligus sebagai 422 validation_failed.
func Parse(limitRaw, cursorRaw, kind string, key any) (Request, error) {
	var fields []apperror.FieldError
	limit, err := ParseLimit(limitRaw)
	if err != nil {
		fields = append(fields, *err)
	}
	req := Request{Limit: limit}
	if cursorRaw != "" {
		if err := Decode(cursorRaw, kind, key); err != nil {
			fields = append(fields, apperror.FieldError{
				Field: FieldCursor, Code: "invalid_cursor",
				Message: "posisi halaman tidak valid; muat ulang dari halaman pertama",
			})
		} else {
			req.HasCursor = true
		}
	}
	if len(fields) > 0 {
		return Request{}, apperror.Validation(fields...)
	}
	return req, nil
}

// ParseLimit membaca `limit`: kosong = DefaultLimit, selain itu bilangan bulat
// 1..MaxLimit. Nilai di luar batas ditolak, tidak dipotong diam-diam.
func ParseLimit(raw string) (int, *apperror.FieldError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &apperror.FieldError{Field: FieldLimit, Code: "invalid_type", Message: "harus bilangan bulat"}
	}
	if n < 1 || n > MaxLimit {
		return 0, &apperror.FieldError{Field: FieldLimit, Code: "out_of_range",
			Message: fmt.Sprintf("harus antara 1 dan %d", MaxLimit)}
	}
	return n, nil
}

// Encode membuat cursor opaque dari key.
func Encode(kind string, key any) (string, error) {
	v, err := json.Marshal(key)
	if err != nil {
		return "", fmt.Errorf("encode key cursor: %w", err)
	}
	b, err := json.Marshal(envelope{Kind: kind, Key: v})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Decode membaca cursor ke key. Gagal (ErrInvalidCursor) kalau base64/JSON
// rusak, kind berbeda, ada field tak dikenal, atau key.Validate() gagal.
func Decode(raw, kind string, key any) error {
	if raw == "" || len(raw) > MaxCursorLen {
		return ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return ErrInvalidCursor
	}
	var env envelope
	if err := strictUnmarshal(b, &env); err != nil || env.Kind != kind || len(env.Key) == 0 {
		return ErrInvalidCursor
	}
	if err := strictUnmarshal(env.Key, key); err != nil {
		return ErrInvalidCursor
	}
	if v, ok := key.(Validator); ok {
		if err := v.Validate(); err != nil {
			return ErrInvalidCursor
		}
	}
	return nil
}

func strictUnmarshal(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return ErrInvalidCursor
	}
	return nil
}

// Trim memotong rows (hasil query Limit+1) menjadi Limit baris dan membuat
// next_cursor dari baris terakhir kalau masih ada halaman berikutnya.
func Trim[T any](rows []T, limit int, kind string, keyOf func(T) any) (Page[T], error) {
	page := Page[T]{Items: rows}
	if page.Items == nil {
		page.Items = []T{}
	}
	if len(rows) <= limit {
		return page, nil
	}
	page.Items = rows[:limit]
	cur, err := Encode(kind, keyOf(page.Items[limit-1]))
	if err != nil {
		return Page[T]{}, err
	}
	page.NextCursor = &cur
	return page, nil
}

// TimeID adalah key untuk urutan `(waktu, id)`, misal `(created_at, id)`.
type TimeID struct {
	At time.Time `json:"t"`
	ID uuid.UUID `json:"id"`
}

func (k TimeID) Validate() error {
	if k.At.IsZero() || k.ID == uuid.Nil {
		return ErrInvalidCursor
	}
	return nil
}
