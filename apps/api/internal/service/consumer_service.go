package service

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/pii"
	"mganik/api/internal/repository"
)

var (
	ErrOTPUnavailable = apperror.New(http.StatusServiceUnavailable, "otp_unavailable",
		"Masuk belum tersedia", "Masuk dengan kode OTP belum diaktifkan di layanan ini, jadi klaim poin belum bisa dilakukan lewat halaman ini.")
	// Detail generik ini dipakai saat tidak ada kode aktif untuk nomor itu
	// (tidak pernah diminta atau sudah kedaluwarsa). Kalau ada kode aktif dan
	// yang dimasukkan salah, verifyOTP mengganti detailnya dengan sisa
	// percobaan ("Sisa N percobaan"); itu keputusan owner.
	ErrOTPInvalid = apperror.New(http.StatusUnauthorized, "otp_invalid",
		"Kode salah", "Kode OTP salah atau sudah kedaluwarsa. Periksa kodenya, atau minta kode baru.")
	ErrConsumerBlocked = apperror.New(http.StatusForbidden, "consumer_blocked",
		"Akun tidak aktif", "Akun ini tidak bisa dipakai. Hubungi layanan pelanggan.")
)

// otpCodeExhausted: jatah percobaan satu kode habis. Status, code, dan
// Retry-After sama dengan tooManyAttempts(0, ...); bedanya hanya pesan, karena
// menunggu tidak membantu: konsumen harus meminta kode baru.
func otpCodeExhausted() error {
	return &RetryAfterError{
		Err: apperror.New(http.StatusTooManyRequests, "too_many_attempts", "Terlalu banyak percobaan",
			"Kode ini sudah terlalu sering salah dimasukkan. Minta kode baru untuk mencoba lagi."),
		After: time.Minute,
	}
}

// OTPPolicy: batas OTP. Permintaan per nomor dan percobaan per kode dihitung
// di DB (tahan restart); batas per IP in-memory.
type OTPPolicy struct {
	CodeTTL time.Duration
	// MaxAttempts: percobaan verifikasi per kode.
	MaxAttempts int
	// RequestsPerPhone dalam RequestWindow.
	RequestsPerPhone int
	RequestWindow    time.Duration
	// RequestsPerIP dan VerifyFailsPerIP dalam RequestWindow (in-memory).
	RequestsPerIP    int
	VerifyFailsPerIP int
	MaxTrackedKeys   int
}

func DefaultOTPPolicy() OTPPolicy {
	return OTPPolicy{
		CodeTTL:          5 * time.Minute,
		MaxAttempts:      5,
		RequestsPerPhone: 5,
		RequestWindow:    15 * time.Minute,
		RequestsPerIP:    20,
		VerifyFailsPerIP: 30,
		MaxTrackedKeys:   100_000,
	}
}

type consumerStore interface {
	LockPhone(tx *gorm.DB, phoneBidx []byte) error
	RecentChallenges(tx *gorm.DB, phoneBidx []byte, since time.Time) (repository.ChallengeWindow, error)
	CloseActiveChallenges(tx *gorm.DB, phoneBidx []byte, now time.Time) error
	CreateChallenge(tx *gorm.DB, c *model.OTPChallenge) error
	LatestActiveChallenge(ctx context.Context, phoneBidx []byte, now time.Time) (*model.OTPChallenge, error)
	IncrementAttempt(ctx context.Context, id uuid.UUID) (int, bool, error)
	ConsumeChallenge(tx *gorm.DB, id uuid.UUID, now time.Time) (bool, error)
	UpsertConsumer(tx *gorm.DB, id uuid.UUID, phoneEnc, phoneBidx []byte, now time.Time) (repository.ConsumerRef, error)
	CreateConsumerSession(tx *gorm.DB, s *model.ConsumerSession) error
	RevokeConsumerSessionByHash(tx *gorm.DB, tokenHash []byte, now time.Time) (*repository.RevokedConsumerSession, error)
	ConsumerPhone(ctx context.Context, id uuid.UUID) (*repository.ConsumerPhoneRow, error)
}

// ConsumerService: login konsumen lewat OTP (mock) dan sesi konsumen.
type ConsumerService struct {
	db       *gorm.DB
	store    consumerStore
	audit    auditWriter
	clock    clock.Clock
	keys     *pii.Keys
	log      *slog.Logger
	otpCode  string
	policy   OTPPolicy
	reqByIP  *attemptLimiter
	failByIP *attemptLimiter
}

// NewConsumerService: otpCode kosong = login konsumen dimatikan (503).
func NewConsumerService(db *gorm.DB, store consumerStore, audit auditWriter, c clock.Clock, keys *pii.Keys,
	log *slog.Logger, otpCode string, policy OTPPolicy) *ConsumerService {
	return &ConsumerService{
		db: db, store: store, audit: audit, clock: c, keys: keys, log: log, otpCode: otpCode, policy: policy,
		reqByIP:  newAttemptLimiter(c, policy.RequestsPerIP, policy.RequestWindow, policy.MaxTrackedKeys),
		failByIP: newAttemptLimiter(c, policy.VerifyFailsPerIP, policy.RequestWindow, policy.MaxTrackedKeys),
	}
}

var errInvalidPhoneField = apperror.Validation(apperror.FieldError{
	Field: "phone", Code: "invalid_phone", Message: "harus nomor HP Indonesia, misalnya 0812xxxxxxxx",
})

// OTPRequested: kapan kode kedaluwarsa. Respons sama untuk nomor baru dan
// nomor yang sudah terdaftar (tidak membocorkan keberadaan akun).
type OTPRequested struct {
	ExpiresAt time.Time
	TTL       time.Duration
}

// RequestOTP membuat challenge OTP baru untuk nomor. Kode lama ditutup.
func (s *ConsumerService) RequestOTP(ctx context.Context, rawPhone string, meta RequestMeta) (OTPRequested, error) {
	if s.otpCode == "" {
		return OTPRequested{}, ErrOTPUnavailable
	}
	phone, err := domain.NormalizeIDPhone(rawPhone)
	if err != nil {
		return OTPRequested{}, errInvalidPhoneField
	}
	// Cek + catat atomik sebelum menyentuh DB (burst paralel tidak lolos).
	if r := s.reqByIP.Reserve(meta.IP); !r.OK {
		return OTPRequested{}, tooManyAttempts(r.RetryAfter, "permintaan kode OTP")
	}

	bidx := s.keys.PhoneIndex(phone)
	now := s.clock.Now()
	ch := &model.OTPChallenge{
		ID:          uuid.Must(uuid.NewV7()),
		PhoneBidx:   bidx,
		MaxAttempts: s.policy.MaxAttempts,
		ExpiresAt:   now.Add(s.policy.CodeTTL),
		IPHash:      s.keys.HashIP(meta.IP),
		CreatedAt:   now,
	}
	ch.CodeHash = s.keys.OTPCodeHash(ch.ID[:], s.otpCode)

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.store.LockPhone(tx, bidx); err != nil {
			return err
		}
		win, err := s.store.RecentChallenges(tx, bidx, now.Add(-s.policy.RequestWindow))
		if err != nil {
			return err
		}
		if win.Count >= int64(s.policy.RequestsPerPhone) && win.Oldest != nil {
			return tooManyAttempts(win.Oldest.Add(s.policy.RequestWindow).Sub(now), "permintaan kode OTP untuk nomor ini")
		}
		if err := s.store.CloseActiveChallenges(tx, bidx, now); err != nil {
			return err
		}
		return s.store.CreateChallenge(tx, ch)
	})
	if err != nil {
		var ra *RetryAfterError
		if !errors.As(err, &ra) {
			s.reqByIP.Release(meta.IP) // kegagalan sistem tidak memakan jatah
		}
		return OTPRequested{}, err
	}
	return OTPRequested{ExpiresAt: ch.ExpiresAt, TTL: s.policy.CodeTTL}, nil
}

// ConsumerLogin: hasil verifikasi OTP. Token hanya untuk cookie.
type ConsumerLogin struct {
	Token       string
	ExpiresAt   time.Time
	ConsumerID  uuid.UUID
	PhoneMasked string
}

// VerifyOTP memeriksa kode; sukses membuat konsumen (kalau baru) dan sesi.
func (s *ConsumerService) VerifyOTP(ctx context.Context, rawPhone, code, previousToken string, meta RequestMeta) (ConsumerLogin, error) {
	if s.otpCode == "" {
		return ConsumerLogin{}, ErrOTPUnavailable
	}
	phone, err := domain.NormalizeIDPhone(rawPhone)
	if err != nil {
		return ConsumerLogin{}, errInvalidPhoneField
	}
	if !isSixASCIIDigits(code) {
		return ConsumerLogin{}, apperror.Validation(apperror.FieldError{Field: "code", Code: "invalid_code", Message: "harus 6 digit angka"})
	}
	// Jatah percobaan per IP dipesan atomik di depan; dikembalikan kalau
	// verifikasi sukses atau gagal karena sistem.
	if r := s.failByIP.Reserve(meta.IP); !r.OK {
		return ConsumerLogin{}, tooManyAttempts(r.RetryAfter, "percobaan kode OTP")
	}
	res, err := s.verifyOTP(ctx, phone, code, previousToken, meta)
	if err == nil || !isCountedOTPFailure(err) {
		s.failByIP.Release(meta.IP)
	}
	return res, err
}

// isCountedOTPFailure: kegagalan yang memakan jatah per IP (kode salah,
// tidak ada challenge, jatah kode habis), bukan kegagalan sistem.
func isCountedOTPFailure(err error) bool {
	var ae *apperror.Error
	return errors.As(err, &ae) && (ae.Code == ErrOTPInvalid.Code || ae.Code == "too_many_attempts")
}

func (s *ConsumerService) verifyOTP(ctx context.Context, phone, code, previousToken string, meta RequestMeta) (ConsumerLogin, error) {
	bidx := s.keys.PhoneIndex(phone)
	now := s.clock.Now()
	ch, err := s.store.LatestActiveChallenge(ctx, bidx, now)
	if err != nil {
		return ConsumerLogin{}, err
	}
	if ch == nil {
		return ConsumerLogin{}, ErrOTPInvalid
	}
	// Counter dinaikkan atomik sebelum membandingkan, jadi request paralel
	// tidak bisa melampaui MaxAttempts.
	attempts, ok, err := s.store.IncrementAttempt(ctx, ch.ID)
	if err != nil {
		return ConsumerLogin{}, err
	}
	if !ok {
		return ConsumerLogin{}, otpCodeExhausted()
	}
	if !hmac.Equal(s.keys.OTPCodeHash(ch.ID[:], code), ch.CodeHash) {
		left := ch.MaxAttempts - attempts
		if left <= 0 {
			return ConsumerLogin{}, otpCodeExhausted()
		}
		return ConsumerLogin{}, apperror.New(ErrOTPInvalid.Status, ErrOTPInvalid.Code, ErrOTPInvalid.Title,
			fmt.Sprintf("Kode OTP salah. Sisa %d percobaan untuk kode ini.", left))
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return ConsumerLogin{}, err
	}
	enc, err := s.keys.EncryptPhone(phone, bidx)
	if err != nil {
		return ConsumerLogin{}, err
	}
	sess := &model.ConsumerSession{
		ID:            uuid.Must(uuid.NewV7()),
		TokenHash:     tokenHash,
		IdleExpiresAt: now.Add(ConsumerIdleTimeout),
		ExpiresAt:     now.Add(ConsumerAbsoluteTimeout),
		LastSeenAt:    now,
		IPHash:        s.keys.HashIP(meta.IP),
		CreatedAt:     now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		consumed, err := s.store.ConsumeChallenge(tx, ch.ID, now)
		if err != nil {
			return err
		}
		if !consumed {
			return ErrOTPInvalid // dipakai request lain lebih dulu
		}
		c, err := s.store.UpsertConsumer(tx, uuid.Must(uuid.NewV7()), enc, bidx, now)
		if err != nil {
			return err
		}
		if c.Status != consumerStatusActive {
			return ErrConsumerBlocked
		}
		if old, ok := hashSessionToken(previousToken); ok {
			if _, err := s.store.RevokeConsumerSessionByHash(tx, old[:], now); err != nil {
				return err
			}
		}
		sess.ConsumerID = c.ID
		if err := s.store.CreateConsumerSession(tx, sess); err != nil {
			return err
		}
		return insertAudit(tx, s.audit, s.keys, model.ActorConsumer, AuditConsumerLogin, &c.ID, "consumer_session", &sess.ID, "", meta, now)
	})
	if err != nil {
		return ConsumerLogin{}, err
	}
	return ConsumerLogin{Token: token, ExpiresAt: sess.ExpiresAt, ConsumerID: sess.ConsumerID, PhoneMasked: domain.MaskPhone(phone)}, nil
}

// Logout mencabut sesi konsumen. Idempotent.
func (s *ConsumerService) Logout(ctx context.Context, token string, meta RequestMeta) error {
	hash, ok := hashSessionToken(token)
	if !ok {
		return nil
	}
	now := s.clock.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rev, err := s.store.RevokeConsumerSessionByHash(tx, hash[:], now)
		if err != nil || rev == nil {
			return err
		}
		return insertAudit(tx, s.audit, s.keys, model.ActorConsumer, AuditConsumerLogout, &rev.ConsumerID, "consumer_session", &rev.ID, "", meta, now)
	})
}

// ConsumerProfile: data konsumen yang aman ditampilkan (nomor disamarkan).
type ConsumerProfile struct {
	ID          uuid.UUID
	PhoneMasked string
}

var errConsumerGone = errors.New("konsumen sesi tidak ditemukan")

// Profile mendekripsi nomor konsumen lalu menyamarkannya.
func (s *ConsumerService) Profile(ctx context.Context, consumerID uuid.UUID) (ConsumerProfile, error) {
	row, err := s.store.ConsumerPhone(ctx, consumerID)
	if err != nil {
		return ConsumerProfile{}, err
	}
	if row == nil {
		return ConsumerProfile{}, apperror.Wrap(errConsumerGone, http.StatusUnauthorized, ErrSessionExpired.Code, ErrSessionExpired.Title, ErrSessionExpired.Detail)
	}
	// AAD = blind index baris itu: ciphertext yang dipindah ke baris lain gagal dibuka.
	phone, err := s.keys.DecryptPhone(row.PhoneEnc, row.PhoneBidx)
	if err != nil {
		return ConsumerProfile{}, fmt.Errorf("membuka nomor konsumen %s: %w", consumerID, err)
	}
	return ConsumerProfile{ID: consumerID, PhoneMasked: domain.MaskPhone(phone)}, nil
}

func isSixASCIIDigits(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
