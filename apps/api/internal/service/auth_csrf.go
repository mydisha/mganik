package service

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"mganik/api/internal/apperror"
)

var (
	ErrCSRFRejected = apperror.New(http.StatusForbidden, "csrf_rejected",
		"Permintaan ditolak", "Permintaan dari situs lain ditolak. Muat ulang halaman, lalu coba lagi.")
	ErrUnsupportedMediaType = apperror.New(http.StatusUnsupportedMediaType, "unsupported_media_type",
		"Format tidak didukung", "Permintaan wajib memakai Content-Type: application/json.")
)

// CSRFGuard memeriksa request bermetode tidak aman yang membawa cookie
// (PLANNING §6.2): Content-Type wajib application/json (form HTML lintas
// situs tidak bisa mengirimnya tanpa preflight CORS), dan Origin (bila ada)
// wajib sama dengan host request atau terdaftar di ALLOWED_ORIGINS.
// Request ber-token Bearer (device POS) tidak diperiksa: browser tidak
// melampirkan header Authorization secara otomatis.
type CSRFGuard struct {
	allowed map[string]struct{}
}

// NewCSRFGuard menerima origin yang sudah dinormalkan config (huruf kecil,
// tanpa garis miring penutup).
func NewCSRFGuard(allowedOrigins []string) *CSRFGuard {
	g := &CSRFGuard{allowed: make(map[string]struct{}, len(allowedOrigins))}
	for _, o := range allowedOrigins {
		g.allowed[strings.ToLower(strings.TrimSuffix(o, "/"))] = struct{}{}
	}
	return g
}

// Check mengembalikan nil untuk metode aman atau request yang lolos.
func (g *CSRFGuard) Check(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrUnsupportedMediaType
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Browser lama tanpa Origin: Sec-Fetch-Site tetap menandai lintas situs.
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			return ErrCSRFRejected
		}
		return nil
	}
	o := strings.ToLower(origin)
	if _, ok := g.allowed[o]; ok {
		return nil
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrCSRFRejected // termasuk Origin "null"
	}
	if u.Host == strings.ToLower(r.Host) {
		return nil
	}
	return ErrCSRFRejected
}
