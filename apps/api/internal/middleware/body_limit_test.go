package middleware

import (
	"errors"
	"io"
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
// dari BodyLimit yang digantikannya, baik untuk path biasa maupun path override.
//
// Middleware dipanggil langsung pada satu gin.Context, tidak lewat
// gin.Engine.ServeHTTP. Engine mengambil Context dari sync.Pool, dan di bawah
// -race sync.Pool.Put sengaja membuang 1 dari 4 item secara acak. Context yang
// dialokasikan ulang membuat rata-rata AllocsPerRun lewat engine berubah-ubah
// antar pengukuran (13 dari 50 run -race gagal), padahal alokasi middleware
// itu sendiri tetap.
func TestBodyLimitByPath_NoExtraAllocs(t *testing.T) {
	overrides := map[string]int64{"/api/v1/pos/sync": 5 << 20}
	base := middlewareAllocs(BodyLimit(1<<20), "/api/v1/orders")
	for _, path := range []string{"/api/v1/orders", "/api/v1/pos/sync"} {
		if got := middlewareAllocs(BodyLimitByPath(1<<20, overrides), path); got > base {
			t.Errorf("%s: BodyLimitByPath %.1f alokasi/request, BodyLimit %.1f", path, got, base)
		}
	}
}

// middlewareAllocs mengukur alokasi satu middleware per request tanpa
// gin.Engine (alasannya di TestBodyLimitByPath_NoExtraAllocs).
func middlewareAllocs(h gin.HandlerFunc, path string) float64 {
	req := httptest.NewRequest(http.MethodPost, path, http.NoBody)
	c := new(gin.Context)
	return testing.AllocsPerRun(200, func() {
		// Context nol di setiap request: indeks handler kembali ke awal dan body
		// tidak membungkus MaxBytesReader dari iterasi sebelumnya.
		*c = gin.Context{Request: req}
		req.Body = http.NoBody
		h(c)
	})
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
