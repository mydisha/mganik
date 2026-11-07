package middleware

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	tests := []struct {
		name   string
		header string
		keep   bool
	}{
		{name: "valid dipakai ulang", header: "req-abc-12345", keep: true},
		{name: "kosong diganti", header: "", keep: false},
		{name: "terlalu pendek diganti", header: "abc", keep: false},
		{name: "karakter berbahaya diganti", header: "abc\ndef<script>", keep: false},
		{name: "unicode diganti", header: "permintaan-ñandú-01", keep: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set("X-Request-ID", tt.header)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			got := rec.Header().Get("X-Request-ID")
			if tt.keep && got != tt.header {
				t.Fatalf("ingin %q, dapat %q", tt.header, got)
			}
			if !tt.keep && (got == tt.header || !validRequestID.MatchString(got)) {
				t.Fatalf("id tidak diganti dengan id valid: %q", got)
			}
		})
	}
}

func TestBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(8))
	r.POST("/", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusNoContent)
	})

	for _, tc := range []struct {
		body string
		want int
	}{
		{body: "12345678", want: http.StatusNoContent},
		{body: "123456789", want: http.StatusRequestEntityTooLarge},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Errorf("body %d byte: status %d, ingin %d", len(tc.body), rec.Code, tc.want)
		}
	}
}

func TestRecoveryReturnsProblem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := gin.New()
	r.Use(RequestID(), Recovery(log))
	r.GET("/", func(*gin.Context) { panic("boom") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("pesan panic bocor ke klien: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("body bukan problem internal_error: %s", rec.Body)
	}
}
