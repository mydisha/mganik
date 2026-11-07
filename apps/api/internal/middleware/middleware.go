// Package middleware berisi middleware Gin lintas endpoint.
package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
)

const requestIDHeader = "X-Request-ID"

// Request id dari klien hanya dipakai kalau bentuknya aman untuk log.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// RequestID memakai X-Request-ID dari klien bila valid, kalau tidak membuat
// UUIDv7 baru, lalu mengembalikannya di header respons.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if !validRequestID.MatchString(id) {
			id = uuid.Must(uuid.NewV7()).String()
		}
		c.Set(apperror.RequestIDKey, id)
		c.Header(requestIDHeader, id)
		c.Next()
	}
}

// AccessLog mencatat satu baris per request. Query string tidak dicatat
// karena bisa memuat kode serial atau data pribadi.
func AccessLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		log.InfoContext(c.Request.Context(), "http",
			"method", c.Request.Method,
			"route", route,
			"status", c.Writer.Status(),
			"bytes", c.Writer.Size(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", c.GetString(apperror.RequestIDKey),
		)
	}
}

// Recovery mengubah panic menjadi 500 problem+json dan mencatat stack trace.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(c.Request.Context(), "panic",
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()),
					"request_id", c.GetString(apperror.RequestIDKey))
				apperror.Write(c, log, fmt.Errorf("panic: %v", rec))
			}
		}()
		c.Next()
	}
}

// SecurityHeaders memasang header aman untuk respons JSON API.
func SecurityHeaders(hsts bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// BodyLimit membatasi ukuran body request. Pembacaan yang melewati batas
// gagal dengan *http.MaxBytesError.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
