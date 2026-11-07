package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
)

func stdHMAC(secret []byte, ts string, body []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts + "."))
	m.Write(body)
	return m.Sum(nil)
}

// Signer dengan state ipad/opad yang di-precompute harus identik dengan
// crypto/hmac untuk key pendek, tepat block size, dan lebih panjang dari block size.
func TestWebhookSigner_MatchesCryptoHMAC(t *testing.T) {
	bodies := [][]byte{nil, []byte(`{}`), []byte(`{"catatan":"pembayaran ✓ Rp 46.000"}`), make([]byte, 5000)}
	for _, secret := range [][]byte{[]byte("k"), make([]byte, 64), []byte(strings.Repeat("rahasia-", 20))} {
		s := NewWebhookSigner(secret)
		for _, b := range bodies {
			got := s.Sum("1762500000", b)
			if want := stdHMAC(secret, "1762500000", b); !hmac.Equal(got[:], want) {
				t.Fatalf("secret len %d body len %d: HMAC berbeda", len(secret), len(b))
			}
		}
	}
}

func TestWebhookSigner_ConcurrentUse(t *testing.T) {
	s := NewWebhookSigner([]byte("rahasia"))
	want := stdHMAC([]byte("rahasia"), "1", []byte("x"))
	done := make(chan bool)
	for range 8 {
		go func() {
			ok := true
			for range 200 {
				got := s.Sum("1", []byte("x"))
				ok = ok && hmac.Equal(got[:], want)
			}
			done <- ok
		}()
	}
	for range 8 {
		if !<-done {
			t.Fatal("hasil HMAC berubah saat dipakai bersamaan")
		}
	}
}

// Jalur per request tidak boleh lebih boros dari hmac.New per request. Sisa
// alokasi: clone state ipad, konversi timestamp, dan buffer lapisan luar yang
// lolos ke heap lewat interface hash.Hash.
func TestWebhookSigner_Allocs(t *testing.T) {
	s := NewWebhookSigner([]byte("rahasia-demo"))
	body := []byte(`{"order_id":"x","amount":46000}`)
	got := testing.AllocsPerRun(200, func() { _ = s.Sum("1762500000", body) })
	std := testing.AllocsPerRun(200, func() { _ = stdHMAC([]byte("rahasia-demo"), "1762500000", body) })
	if got > 3 || got >= std {
		t.Fatalf("alokasi per Sum %.0f (crypto/hmac %.0f), ingin <= 3 dan lebih kecil dari crypto/hmac", got, std)
	}
}

func BenchmarkWebhookSigner_Sum(b *testing.B) {
	s := NewWebhookSigner([]byte("rahasia-demo"))
	body := []byte(`{"provider_ref":"PAY-1","order_id":"0193","amount":46000,"currency":"IDR","status":"SUCCEEDED"}`)
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Sum("1762500000", body)
	}
}

func BenchmarkWebhookSigner_StdHMAC(b *testing.B) {
	body := []byte(`{"provider_ref":"PAY-1","order_id":"0193","amount":46000,"currency":"IDR","status":"SUCCEEDED"}`)
	b.ReportAllocs()
	for b.Loop() {
		_ = stdHMAC([]byte("rahasia-demo"), "1762500000", body)
	}
}

func TestVerifyWebhook(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)
	clk := clock.NewFake(now)
	secret := []byte("rahasia-webhook-uji")
	signer := NewWebhookSigner(secret)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	r := gin.New()
	r.Use(BodyLimit(1024))
	r.POST("/hook", VerifyWebhook(signer, clk, log), func(c *gin.Context) {
		b, _ := io.ReadAll(c.Request.Body)
		c.String(http.StatusOK, string(b))
	})

	body := `{"order_id":"abc","amount":46000}`
	ts := strconv.FormatInt(now.Unix(), 10)
	tests := []struct {
		name   string
		ts     string
		sig    string
		body   string
		status int
		code   string
	}{
		{"valid", ts, signer.Sign(ts, []byte(body)), body, 200, ""},
		{"timestamp 4 menit lalu masih diterima", strconv.FormatInt(now.Add(-4*time.Minute).Unix(), 10),
			signer.Sign(strconv.FormatInt(now.Add(-4*time.Minute).Unix(), 10), []byte(body)), body, 200, ""},
		{"tanpa header", "", "", body, 401, "invalid_signature"},
		{"signature salah", ts, signer.Sign(ts, []byte(body+" ")), body, 401, "invalid_signature"},
		{"body diubah", ts, signer.Sign(ts, []byte(body)), `{"order_id":"abc","amount":1}`, 401, "invalid_signature"},
		{"secret lain", ts, NewWebhookSigner([]byte("bukan")).Sign(ts, []byte(body)), body, 401, "invalid_signature"},
		{"prefix salah", ts, "v2=" + strings.TrimPrefix(signer.Sign(ts, []byte(body)), "v1="), body, 401, "invalid_signature"},
		{"hex terpotong", ts, signer.Sign(ts, []byte(body))[:20], body, 401, "invalid_signature"},
		{"timestamp bukan angka", "kemarin", signer.Sign("kemarin", []byte(body)), body, 401, "invalid_signature"},
		{"timestamp basi 6 menit", strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10),
			signer.Sign(strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10), []byte(body)), body, 401, "stale_timestamp"},
		{"timestamp masa depan 6 menit", strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10),
			signer.Sign(strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10), []byte(body)), body, 401, "stale_timestamp"},
		{"body terlalu besar", ts, signer.Sign(ts, []byte(strings.Repeat("x", 2000))), strings.Repeat("x", 2000), 413, "body_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(tt.body))
			if tt.ts != "" {
				req.Header.Set(WebhookTimestampHeader, tt.ts)
			}
			if tt.sig != "" {
				req.Header.Set(WebhookSignatureHeader, tt.sig)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status %d, ingin %d: %s", rec.Code, tt.status, rec.Body)
			}
			if tt.status == 200 {
				if rec.Body.String() != tt.body {
					t.Fatalf("body tidak diteruskan utuh ke handler: %q", rec.Body)
				}
				return
			}
			var p apperror.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != tt.code {
				t.Fatalf("kode %q, ingin %q (%v)", p.Code, tt.code, err)
			}
		})
	}
}
