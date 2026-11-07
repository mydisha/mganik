package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"mganik/api/internal/apperror"
)

// Nama cookie sesi. Prefix __Host- memaksa browser hanya menerima cookie
// Secure, Path=/, tanpa Domain (tidak bisa ditimpa subdomain).
const (
	StaffSessionCookie    = "__Host-session"
	ConsumerSessionCookie = "__Host-consumer"
)

// Kebijakan sesi (PLANNING §6.2).
const (
	// StaffIdleTimeout: satu shift.
	StaffIdleTimeout = 8 * time.Hour
	// StaffAbsoluteTimeout: batas mutlak sejak login, walau terus dipakai.
	StaffAbsoluteTimeout = 12 * time.Hour
	// ConsumerIdleTimeout dan ConsumerAbsoluteTimeout untuk sesi OTP konsumen.
	ConsumerIdleTimeout     = 24 * time.Hour
	ConsumerAbsoluteTimeout = 7 * 24 * time.Hour
	// SessionTouchInterval: idle timeout diperpanjang (UPDATE DB) paling sering
	// sekali per interval ini, bukan di setiap request. Akibatnya idle timeout
	// efektif bisa sampai 5 menit lebih pendek dari nominal.
	SessionTouchInterval = 5 * time.Minute
)

// sessionTokenBytes: 256-bit acak.
const sessionTokenBytes = 32

// Panjang token base64url tanpa padding untuk 32 byte.
var sessionTokenLen = base64.RawURLEncoding.EncodedLen(sessionTokenBytes)

// Error sesi/token yang dikirim ke klien.
var (
	ErrSessionExpired = apperror.New(http.StatusUnauthorized, "session_expired",
		"Sesi berakhir", "Sesi Anda sudah berakhir. Masuk lagi untuk melanjutkan.")
	ErrInvalidToken = apperror.New(http.StatusUnauthorized, "invalid_token",
		"Perangkat tidak dikenali", "Token perangkat kasir ini tidak valid atau sudah dicabut. Minta kepala toko mendaftarkan ulang perangkatnya.")
	ErrCashierNotAllowed = apperror.New(http.StatusForbidden, "cashier_not_allowed",
		"Kasir tidak diizinkan", "Kasir ini tidak terdaftar di toko perangkat ini. Minta kepala toko memeriksa akses kasirnya.")
)

// errSessionInvalid: token sesi tidak dikenal, kedaluwarsa, atau dicabut.
// Resolver memutuskan apakah ini 401 atau anonim (tergantung path).
var errSessionInvalid = errors.New("sesi tidak valid")

// newSessionToken membuat token acak untuk cookie beserta hash SHA-256-nya
// (yang disimpan di DB).
func newSessionToken() (token string, hash []byte, err error) {
	var raw [sessionTokenBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, fmt.Errorf("membuat token sesi: %w", err)
	}
	sum := sha256.Sum256(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:]), sum[:], nil
}

// hashSessionToken memvalidasi bentuk token cookie dan mengembalikan hash-nya.
// Token yang bentuknya salah tidak pernah sampai ke DB.
func hashSessionToken(token string) ([32]byte, bool) {
	var raw [sessionTokenBytes]byte
	if len(token) != sessionTokenLen {
		return [32]byte{}, false
	}
	n, err := base64.RawURLEncoding.Decode(raw[:], []byte(token))
	if err != nil || n != sessionTokenBytes {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw[:]), true
}

// hashDeviceToken: pos_devices.token_hash = SHA-256 string token apa adanya.
func hashDeviceToken(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}
