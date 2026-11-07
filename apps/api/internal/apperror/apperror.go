// Package apperror mendefinisikan error domain dan cara menuliskannya
// sebagai application/problem+json (RFC 9457).
package apperror

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Error adalah error yang aman ditampilkan ke klien. Code stabil dan
// dipakai frontend untuk memilih pesan; Detail berbahasa Indonesia.
type Error struct {
	Status int
	Code   string
	Title  string
	Detail string
	cause  error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Detail, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (e *Error) Unwrap() error { return e.cause }

// New membuat error domain baru.
func New(status int, code, title, detail string) *Error {
	return &Error{Status: status, Code: code, Title: title, Detail: detail}
}

// Wrap membuat error domain yang membawa penyebab internal. Penyebab hanya
// masuk log, tidak pernah dikirim ke klien.
func Wrap(cause error, status int, code, title, detail string) *Error {
	return &Error{Status: status, Code: code, Title: title, Detail: detail, cause: cause}
}

// Problem adalah body respons error.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// RequestIDKey adalah kunci gin.Context tempat middleware menyimpan request id.
const RequestIDKey = "request_id"

// Write menulis err sebagai problem+json. Error yang bukan *Error dianggap
// bug: dicatat di log dan klien hanya menerima pesan umum.
func Write(c *gin.Context, log *slog.Logger, err error) {
	var appErr *Error
	if !errors.As(err, &appErr) {
		log.ErrorContext(c.Request.Context(), "error tak tertangani",
			"error", err, "path", c.FullPath(), "request_id", c.GetString(RequestIDKey))
		appErr = New(http.StatusInternalServerError, "internal_error",
			"Terjadi kesalahan", "Terjadi kesalahan di server. Coba lagi beberapa saat lagi.")
	} else if appErr.cause != nil {
		log.WarnContext(c.Request.Context(), "error domain",
			"code", appErr.Code, "error", appErr.cause, "request_id", c.GetString(RequestIDKey))
	}

	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(appErr.Status, Problem{
		Type:      "about:blank",
		Title:     appErr.Title,
		Status:    appErr.Status,
		Code:      appErr.Code,
		Detail:    appErr.Detail,
		RequestID: c.GetString(RequestIDKey),
	})
}

// Error umum yang dipakai lintas domain.
var (
	ErrNotFound = New(http.StatusNotFound, "not_found",
		"Tidak ditemukan", "Alamat yang diminta tidak ada.")
	ErrMethodNotAllowed = New(http.StatusMethodNotAllowed, "method_not_allowed",
		"Metode tidak diizinkan", "Metode HTTP ini tidak didukung untuk alamat tersebut.")
)
