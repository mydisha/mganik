package service

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
)

// attemptLimiter menghitung kejadian (mis. login gagal) per kunci dalam
// jendela tetap yang dimulai pada kejadian pertama. Setelah limit tercapai,
// kunci diblok sampai jendela berakhir. In-memory per proses: cukup untuk
// prototype satu instance; rate limit terdistribusi di luar lingkup
// (PLANNING §6.8). Aman dipakai bersamaan.
//
// Pemakai WAJIB memanggil Reserve SEBELUM pekerjaan mahal (argon2, cek OTP):
// cek dan penambahan counter terjadi di bawah satu lock, jadi burst paralel
// tidak bisa melewati limit. Percobaan yang ternyata tidak dihitung (sukses,
// atau batal sebelum verifikasi) dikembalikan lewat Release/Reset.
type attemptLimiter struct {
	mu      sync.Mutex
	clock   clock.Clock
	limit   int
	window  time.Duration
	maxKeys int
	entries map[string]*attemptWindow
}

type attemptWindow struct {
	start time.Time
	count int
	// rejectedNoted: penolakan pertama di jendela ini sudah dilaporkan
	// (audit cukup satu baris per kunci per jendela).
	rejectedNoted bool
}

func newAttemptLimiter(c clock.Clock, limit int, window time.Duration, maxKeys int) *attemptLimiter {
	return &attemptLimiter{clock: c, limit: limit, window: window, maxKeys: maxKeys, entries: map[string]*attemptWindow{}}
}

// Reservation adalah hasil Reserve.
type Reservation struct {
	// OK: slot didapat dan counter sudah dinaikkan.
	OK bool
	// RetryAfter: sisa jendela kalau ditolak.
	RetryAfter time.Duration
	// FirstRejection: penolakan pertama untuk kunci ini di jendela berjalan.
	FirstRejection bool
}

// Reserve secara atomik memeriksa limit dan, kalau masih ada jatah,
// menaikkan counter.
func (l *attemptLimiter) Reserve(key string) Reservation {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	e, ok := l.entries[key]
	if ok && !now.Before(e.start.Add(l.window)) {
		delete(l.entries, key)
		ok = false
	}
	if !ok {
		if len(l.entries) >= l.maxKeys {
			l.evictLocked(now)
		}
		l.entries[key] = &attemptWindow{start: now, count: 1}
		return Reservation{OK: true}
	}
	if e.count >= l.limit {
		first := !e.rejectedNoted
		e.rejectedNoted = true
		return Reservation{RetryAfter: e.start.Add(l.window).Sub(now), FirstRejection: first}
	}
	e.count++
	return Reservation{OK: true}
}

// Release mengembalikan satu slot yang dipesan Reserve tetapi tidak jadi
// dihitung (verifikasi tidak terjadi, atau percobaan sukses untuk limit per IP).
func (l *attemptLimiter) Release(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[key]; ok && e.count > 0 {
		e.count--
	}
}

// Reset menghapus hitungan key (mis. setelah login berhasil).
func (l *attemptLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// evictLocked membuang entri kedaluwarsa; kalau peta masih penuh, entri
// dengan jendela tertua dibuang supaya memori tetap terbatas.
func (l *attemptLimiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, e := range l.entries {
		if !now.Before(e.start.Add(l.window)) {
			delete(l.entries, k)
			continue
		}
		if oldestKey == "" || e.start.Before(oldest) {
			oldestKey, oldest = k, e.start
		}
	}
	if len(l.entries) >= l.maxKeys && oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

// RetryAfterError membawa durasi tunggu supaya controller bisa mengisi
// header Retry-After. Unwrap ke *apperror.Error untuk body problem+json.
type RetryAfterError struct {
	Err   *apperror.Error
	After time.Duration
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Err }

// tooManyAttempts membuat 429 too_many_attempts dengan menit tunggu
// (dibulatkan ke atas, minimal 1).
func tooManyAttempts(wait time.Duration, what string) error {
	minutes := int((wait + time.Minute - 1) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return &RetryAfterError{
		Err: apperror.New(http.StatusTooManyRequests, "too_many_attempts", "Terlalu banyak percobaan",
			fmt.Sprintf("Terlalu banyak %s. Coba lagi dalam %d menit.", what, minutes)),
		After: time.Duration(minutes) * time.Minute,
	}
}
