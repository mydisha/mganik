package pagination_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/pagination"
)

func TestParseLimit(t *testing.T) {
	tests := []struct {
		raw  string
		want int
		code string
	}{
		{"", pagination.DefaultLimit, ""},
		{"  ", pagination.DefaultLimit, ""},
		{"1", 1, ""},
		{"200", 200, ""},
		{"0", 0, "out_of_range"},
		{"-5", 0, "out_of_range"},
		{"201", 0, "out_of_range"},
		{"abc", 0, "invalid_type"},
		{"1.5", 0, "invalid_type"},
		{"99999999999999999999", 0, "invalid_type"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, ferr := pagination.ParseLimit(tt.raw)
			if tt.code == "" {
				if ferr != nil || got != tt.want {
					t.Fatalf("ParseLimit(%q) = %d, %v; ingin %d", tt.raw, got, ferr, tt.want)
				}
				return
			}
			if ferr == nil || ferr.Code != tt.code || ferr.Field != "limit" {
				t.Fatalf("ParseLimit(%q) error %+v, ingin kode %s", tt.raw, ferr, tt.code)
			}
		})
	}
}

func TestCursor_RoundTrip(t *testing.T) {
	key := pagination.TimeID{At: time.Date(2026, 11, 7, 1, 2, 3, 456789000, time.UTC), ID: uuid.Must(uuid.NewV7())}
	cur, err := pagination.Encode("anomalies", key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(cur, "+/=") {
		t.Fatalf("cursor bukan base64url tanpa padding: %s", cur)
	}
	var got pagination.TimeID
	if err := pagination.Decode(cur, "anomalies", &got); err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(key.At) || got.ID != key.ID {
		t.Fatalf("round trip %+v != %+v", got, key)
	}
}

func TestCursor_RejectsBrokenInput(t *testing.T) {
	valid, _ := pagination.Encode("anomalies", pagination.TimeID{At: time.Now(), ID: uuid.Must(uuid.NewV7())})
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := map[string]string{
		"bukan base64":      "%%%",
		"base64 bukan json": b64("halo"),
		"kind lain":         mustEncode(t, "conflicts", pagination.TimeID{At: time.Now(), ID: uuid.Must(uuid.NewV7())}),
		"field tak dikenal": b64(`{"k":"anomalies","v":{"t":"2026-11-07T00:00:00Z","id":"0190a8a0-0000-7000-8000-000000000001","x":1}}`),
		"id nil":            b64(`{"k":"anomalies","v":{"t":"2026-11-07T00:00:00Z","id":"00000000-0000-0000-0000-000000000000"}}`),
		"waktu kosong":      b64(`{"k":"anomalies","v":{"id":"0190a8a0-0000-7000-8000-000000000001"}}`),
		"tanpa v":           b64(`{"k":"anomalies"}`),
		"json berekor":      b64(`{"k":"anomalies","v":{"t":"2026-11-07T00:00:00Z","id":"0190a8a0-0000-7000-8000-000000000001"}}{}`),
		"terlalu panjang":   strings.Repeat("A", pagination.MaxCursorLen+1),
		"padding standar":   valid + "==",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var k pagination.TimeID
			if err := pagination.Decode(raw, "anomalies", &k); !errors.Is(err, pagination.ErrInvalidCursor) {
				t.Fatalf("ingin ErrInvalidCursor, dapat %v", err)
			}
		})
	}
}

func TestParse_CollectsAllFieldErrors(t *testing.T) {
	var k pagination.TimeID
	_, err := pagination.Parse("500", "rusak!", "anomalies", &k)
	var appErr *apperror.Error
	if !errors.As(err, &appErr) || appErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("ingin 422, dapat %v", err)
	}
	if len(appErr.Fields) != 2 || appErr.Fields[0].Field != "limit" || appErr.Fields[1].Field != "cursor" ||
		appErr.Fields[1].Code != "invalid_cursor" {
		t.Fatalf("field error salah: %+v", appErr.Fields)
	}

	req, err := pagination.Parse("", "", "anomalies", &k)
	if err != nil || req.Limit != pagination.DefaultLimit || req.HasCursor {
		t.Fatalf("default salah: %+v %v", req, err)
	}
}

func TestTrim(t *testing.T) {
	keyOf := func(n int) any {
		return pagination.TimeID{At: time.Unix(int64(n), 0), ID: uuid.NewSHA1(uuid.Nil, []byte{byte(n)})}
	}

	page, err := pagination.Trim([]int{1, 2, 3}, 3, "x", keyOf)
	if err != nil || len(page.Items) != 3 || page.NextCursor != nil {
		t.Fatalf("halaman terakhir salah: %+v %v", page, err)
	}

	page, err = pagination.Trim([]int{1, 2, 3, 4}, 3, "x", keyOf)
	if err != nil || len(page.Items) != 3 || page.NextCursor == nil {
		t.Fatalf("halaman berlanjut salah: %+v %v", page, err)
	}
	var k pagination.TimeID
	if err := pagination.Decode(*page.NextCursor, "x", &k); err != nil || k.At.Unix() != 3 {
		t.Fatalf("cursor harus menunjuk baris terakhir halaman (3): %+v %v", k, err)
	}

	empty, err := pagination.Trim[int](nil, 10, "x", keyOf)
	if err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("hasil kosong harus slice kosong (JSON []), dapat %+v", empty)
	}
}

func mustEncode(t *testing.T, kind string, key any) string {
	t.Helper()
	s, err := pagination.Encode(kind, key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
