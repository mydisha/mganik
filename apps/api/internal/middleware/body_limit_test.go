package middleware

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func readAllHandler(c *gin.Context) {
	_, err := io.ReadAll(c.Request.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}
	c.Status(http.StatusNoContent)
}

func TestBodyLimitByPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimitByPath(8, map[string]int64{"/sync": 16}))
	r.POST("/sync", readAllHandler)
	r.POST("/lain", readAllHandler)

	for _, tc := range []struct {
		path string
		size int
		want int
	}{
		{"/lain", 8, http.StatusNoContent},
		{"/lain", 9, http.StatusRequestEntityTooLarge},
		{"/sync", 16, http.StatusNoContent},
		{"/sync", 17, http.StatusRequestEntityTooLarge},
		// Query tidak ikut dicocokkan: hanya URL.Path persis.
		{"/lain?p=/sync", 9, http.StatusRequestEntityTooLarge},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(strings.Repeat("x", tc.size))))
		if rec.Code != tc.want {
			t.Errorf("%s %d byte: status %d, ingin %d", tc.path, tc.size, rec.Code, tc.want)
		}
	}
}

// Middleware per request: alokasi BodyLimitByPath tidak boleh lebih banyak
// dari BodyLimit yang digantikannya (jalur paling sering = path biasa).
func TestBodyLimitByPath_NoExtraAllocs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	run := func(h gin.HandlerFunc) float64 {
		r := limitEngine(h)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", http.NoBody)
		return testing.AllocsPerRun(200, func() { r.ServeHTTP(w, req) })
	}
	overrides := map[string]int64{"/api/v1/pos/sync": 5 << 20}
	base, byPath := run(BodyLimit(1<<20)), run(BodyLimitByPath(1<<20, overrides))
	// AllocsPerRun adalah rata-rata; di bawah -race dan GC paralel nilainya bisa
	// bergeser pecahan kecil walau jumlah alokasi per request sama. Bandingkan
	// alokasi utuh per request supaya test tidak flaky.
	if math.Round(byPath) > math.Round(base) {
		t.Fatalf("BodyLimitByPath %.1f alokasi/request, BodyLimit %.1f", byPath, base)
	}
}

func BenchmarkBodyLimit(b *testing.B) {
	benchLimit(b, BodyLimit(1<<20))
}

func BenchmarkBodyLimitByPath(b *testing.B) {
	benchLimit(b, BodyLimitByPath(1<<20, map[string]int64{"/api/v1/pos/sync": 5 << 20}))
}

func limitEngine(h gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(h)
	r.POST("/api/v1/orders", func(c *gin.Context) {})
	return r
}

func benchLimit(b *testing.B, h gin.HandlerFunc) {
	gin.SetMode(gin.TestMode)
	r := limitEngine(h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", http.NoBody)
	b.ReportAllocs()
	for b.Loop() {
		r.ServeHTTP(w, req)
	}
}
