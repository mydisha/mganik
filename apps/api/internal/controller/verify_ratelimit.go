package controller

import (
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
)

// ErrRateLimited: terlalu banyak request dari satu IP (429).
var ErrRateLimited = apperror.New(http.StatusTooManyRequests, "rate_limited",
	"Terlalu banyak permintaan", "Terlalu banyak permintaan dari jaringan ini. Tunggu sebentar, lalu coba lagi.")

// publicRateMaxKeys membatasi memori limiter. Kalau penuh setelah pembersihan,
// IP baru ditolak (fail closed) alih-alih membiarkan memori tumbuh tanpa batas.
const publicRateMaxKeys = 100_000

// PublicRateLimiter adalah rate limit fixed-window per kunci (IP) di memori,
// cukup untuk satu instance prototype. Produksi: Redis atau WAF/CDN di depan
// (rate limit terdistribusi).
type PublicRateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	clock     clock.Clock
	buckets   map[string]*rateBucket
	maxKeys   int
	lastSweep time.Time
}

type rateBucket struct {
	start time.Time
	count int
}

// NewPublicRateLimiter mengizinkan limit request per window per kunci.
func NewPublicRateLimiter(limit int, window time.Duration, clk clock.Clock) *PublicRateLimiter {
	if clk == nil {
		clk = clock.Real{}
	}
	return &PublicRateLimiter{
		limit: limit, window: window, clock: clk,
		buckets: map[string]*rateBucket{}, maxKeys: publicRateMaxKeys,
	}
}

// Allow mencatat satu request untuk key. ok=false berarti ditolak, dengan
// perkiraan waktu tunggu.
func (l *PublicRateLimiter) Allow(key string) (bool, time.Duration) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= l.window {
		for k, b := range l.buckets {
			if now.Sub(b.start) >= l.window {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok || now.Sub(b.start) >= l.window {
		if !ok && len(l.buckets) >= l.maxKeys {
			return false, l.window
		}
		l.buckets[key] = &rateBucket{start: now, count: 1}
		return true, 0
	}
	if b.count >= l.limit {
		return false, b.start.Add(l.window).Sub(now)
	}
	b.count++
	return true, 0
}

// RateLimitKeyForIP: IPv4 per alamat, IPv6 per prefix /64 (satu pelanggan
// IPv6 biasanya memegang seluruh /64, jadi per alamat mudah diakali dengan
// berganti alamat). IP yang tidak bisa diurai dipakai apa adanya.
func RateLimitKeyForIP(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return "raw:" + ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return prefix.String()
}

func writeRateLimited(c *gin.Context, log *slog.Logger, wait time.Duration) {
	c.Header("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	apperror.Write(c, log, ErrRateLimited)
}

// RateLimitByIP menolak request dengan 429 + Retry-After saat IP klien
// (c.ClientIP, menghormati TRUSTED_PROXIES) melewati batas.
func RateLimitByIP(l *PublicRateLimiter, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ok, wait := l.Allow(RateLimitKeyForIP(c.ClientIP())); !ok {
			writeRateLimited(c, log, wait)
			return
		}
		c.Next()
	}
}

// RateLimitByConsumer membatasi per akun konsumen (dipasang setelah
// RequireKind). Melengkapi batas per IP: pelaku yang berpindah jaringan tetap
// dibatasi per akunnya.
func RateLimitByConsumer(l *PublicRateLimiter, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := auth.FromContext(c.Request.Context())
		if p == nil {
			apperror.Write(c, log, apperror.ErrUnauthenticated)
			return
		}
		if ok, wait := l.Allow("consumer:" + p.ID.String()); !ok {
			writeRateLimited(c, log, wait)
			return
		}
		c.Next()
	}
}
