package middleware

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
)

// Webhook pembayaran mock dan impor order marketplace ditandatangani pengirim:
//
//	X-Mganik-Timestamp: <unix detik>
//	X-Mganik-Signature: v1=<hex HMAC-SHA256(secret, timestamp + "." + body)>
//
// Timestamp ikut ditandatangani dan harus berada dalam ±WebhookTolerance dari
// jam server (mencegah replay event lama).
const (
	WebhookTimestampHeader = "X-Mganik-Timestamp"
	WebhookSignatureHeader = "X-Mganik-Signature"
	WebhookTolerance       = 5 * time.Minute
	webhookSigPrefix       = "v1="
)

// webhookSigSeparator hanya dibaca (dipakai bersama antar goroutine).
var webhookSigSeparator = []byte{'.'}

var (
	ErrWebhookSignatureInvalid = apperror.New(http.StatusUnauthorized, "invalid_signature",
		"Signature tidak valid", "Header signature webhook tidak ada atau tidak cocok.")
	ErrWebhookTimestampStale = apperror.New(http.StatusUnauthorized, "stale_timestamp",
		"Timestamp kedaluwarsa", "Timestamp webhook di luar toleransi 5 menit.")
)

// WebhookSigner menghitung HMAC-SHA256 dengan key tetap. State ipad dihitung
// sekali saat start lalu di-clone per request, dan lapisan luar di-hash di
// stack, sehingga jalur per request tidak membuat hmac.New baru (lihat
// benchmark di test).
type WebhookSigner struct {
	inner hash.Cloner
	opad  [sha256.BlockSize]byte
}

// NewWebhookSigner menyiapkan signer untuk secret (key lebih panjang dari
// block size di-hash dulu, sesuai RFC 2104).
func NewWebhookSigner(secret []byte) *WebhookSigner {
	var key [sha256.BlockSize]byte
	if len(secret) > sha256.BlockSize {
		sum := sha256.Sum256(secret)
		copy(key[:], sum[:])
	} else {
		copy(key[:], secret)
	}
	s := &WebhookSigner{}
	var ipad [sha256.BlockSize]byte
	for i := range key {
		ipad[i] = key[i] ^ 0x36
		s.opad[i] = key[i] ^ 0x5c
	}
	h := sha256.New()
	h.Write(ipad[:])
	s.inner = h.(hash.Cloner)
	return s
}

// Sum mengembalikan HMAC-SHA256(secret, timestamp + "." + body).
func (s *WebhookSigner) Sum(timestamp string, body []byte) [sha256.Size]byte {
	c, err := s.inner.Clone()
	if err != nil {
		// sha256 standar library selalu bisa di-clone.
		panic("webhook signer: " + err.Error())
	}
	_, _ = io.WriteString(c, timestamp)
	_, _ = c.Write(webhookSigSeparator)
	_, _ = c.Write(body)
	var outer [sha256.BlockSize + sha256.Size]byte
	copy(outer[:], s.opad[:])
	c.Sum(outer[sha256.BlockSize:sha256.BlockSize])
	return sha256.Sum256(outer[:])
}

// Sign mengembalikan nilai header X-Mganik-Signature (dipakai pengirim/test).
func (s *WebhookSigner) Sign(timestamp string, body []byte) string {
	sum := s.Sum(timestamp, body)
	return webhookSigPrefix + hex.EncodeToString(sum[:])
}

// VerifyWebhook menolak request yang signature-nya tidak valid (401
// invalid_signature) atau timestamp-nya basi (401 stale_timestamp), lalu
// mengembalikan body supaya bisa di-bind handler.
func VerifyWebhook(signer *WebhookSigner, clk clock.Clock, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ts := c.GetHeader(WebhookTimestampHeader)
		sigHeader := c.GetHeader(WebhookSignatureHeader)
		sec, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || !strings.HasPrefix(sigHeader, webhookSigPrefix) {
			apperror.Write(c, log, ErrWebhookSignatureInvalid)
			return
		}
		var got [sha256.Size]byte
		if n, err := hex.Decode(got[:], []byte(sigHeader[len(webhookSigPrefix):])); err != nil || n != sha256.Size ||
			len(sigHeader) != len(webhookSigPrefix)+2*sha256.Size {
			apperror.Write(c, log, ErrWebhookSignatureInvalid)
			return
		}
		if c.Request.Body == nil {
			apperror.Write(c, log, apperror.ErrBadRequest)
			return
		}
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				apperror.Write(c, log, apperror.ErrBodyTooLarge)
				return
			}
			apperror.Write(c, log, apperror.ErrBadRequest)
			return
		}
		want := signer.Sum(ts, body)
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			apperror.Write(c, log, ErrWebhookSignatureInvalid)
			return
		}
		// Timestamp dicek setelah signature: nilainya sudah terbukti dari pengirim.
		age := clk.Now().Sub(time.Unix(sec, 0))
		if age > WebhookTolerance || age < -WebhookTolerance {
			apperror.Write(c, log, ErrWebhookTimestampStale)
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Next()
	}
}
