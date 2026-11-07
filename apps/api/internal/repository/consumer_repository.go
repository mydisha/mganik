package repository

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// otpLockClass memisahkan advisory lock OTP dari advisory lock domain lain
// (pg_advisory_xact_lock(int4, int4): class, objek).
const otpLockClass int32 = 0x4F5450 // "OTP"

// ConsumerRepository: challenge OTP, konsumen, dan sesi konsumen.
type ConsumerRepository struct {
	db *gorm.DB
}

func NewConsumerRepository(db *gorm.DB) *ConsumerRepository {
	return &ConsumerRepository{db: db}
}

// LockPhone mengunci nomor (lewat blind index) sampai transaksi selesai,
// supaya hitung-lalu-insert challenge per nomor tidak balapan.
func (r *ConsumerRepository) LockPhone(tx *gorm.DB, phoneBidx []byte) error {
	if len(phoneBidx) < 4 {
		return fmt.Errorf("blind index terlalu pendek")
	}
	key := int32(binary.BigEndian.Uint32(phoneBidx[:4]))
	if err := tx.Exec(`SELECT pg_advisory_xact_lock(?, ?)`, otpLockClass, key).Error; err != nil {
		return fmt.Errorf("mengunci nomor: %w", err)
	}
	return nil
}

// ChallengeWindow: jumlah challenge sebuah nomor sejak `since` dan yang tertua.
type ChallengeWindow struct {
	Count  int64
	Oldest *time.Time
}

// RecentChallenges menghitung permintaan OTP sebuah nomor sejak since.
func (r *ConsumerRepository) RecentChallenges(tx *gorm.DB, phoneBidx []byte, since time.Time) (ChallengeWindow, error) {
	var w ChallengeWindow
	err := tx.Raw(`
		SELECT count(*) AS count, min(created_at) AS oldest
		  FROM otp_challenges
		 WHERE phone_bidx = ? AND created_at > ?`, phoneBidx, since).Scan(&w).Error
	if err != nil {
		return ChallengeWindow{}, fmt.Errorf("menghitung challenge OTP: %w", err)
	}
	return w, nil
}

// CloseActiveChallenges menutup challenge lama yang belum dipakai: hanya
// kode terbaru yang berlaku.
func (r *ConsumerRepository) CloseActiveChallenges(tx *gorm.DB, phoneBidx []byte, now time.Time) error {
	err := tx.Exec(`UPDATE otp_challenges SET consumed_at = ? WHERE phone_bidx = ? AND consumed_at IS NULL`,
		now, phoneBidx).Error
	if err != nil {
		return fmt.Errorf("menutup challenge OTP lama: %w", err)
	}
	return nil
}

// CreateChallenge menyimpan challenge OTP baru.
func (r *ConsumerRepository) CreateChallenge(tx *gorm.DB, c *model.OTPChallenge) error {
	if err := tx.Create(c).Error; err != nil {
		return fmt.Errorf("membuat challenge OTP: %w", err)
	}
	return nil
}

// LatestActiveChallenge mengambil challenge terbaru yang belum dipakai dan
// belum kedaluwarsa. (nil, nil) kalau tidak ada.
func (r *ConsumerRepository) LatestActiveChallenge(ctx context.Context, phoneBidx []byte, now time.Time) (*model.OTPChallenge, error) {
	var c model.OTPChallenge
	res := r.db.WithContext(ctx).
		Where("phone_bidx = ? AND consumed_at IS NULL AND expires_at > ?", phoneBidx, now).
		Order("created_at DESC").Limit(1).Find(&c)
	if res.Error != nil {
		return nil, fmt.Errorf("memuat challenge OTP: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &c, nil
}

// IncrementAttempt menaikkan counter percobaan secara atomik SEBELUM kode
// dibandingkan. ok false kalau challenge sudah dipakai atau jatah habis.
func (r *ConsumerRepository) IncrementAttempt(ctx context.Context, id uuid.UUID) (attempts int, ok bool, err error) {
	res := r.db.WithContext(ctx).Raw(`
		UPDATE otp_challenges SET attempts = attempts + 1
		 WHERE id = ? AND consumed_at IS NULL AND attempts < max_attempts
		RETURNING attempts`, id).Scan(&attempts)
	if res.Error != nil {
		return 0, false, fmt.Errorf("mencatat percobaan OTP: %w", res.Error)
	}
	return attempts, res.RowsAffected == 1, nil
}

// ConsumeChallenge menandai challenge terpakai. false kalau sudah dipakai
// request lain (verifikasi bersamaan).
func (r *ConsumerRepository) ConsumeChallenge(tx *gorm.DB, id uuid.UUID, now time.Time) (bool, error) {
	res := tx.Exec(`UPDATE otp_challenges SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`, now, id)
	if res.Error != nil {
		return false, fmt.Errorf("memakai challenge OTP: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// ConsumerRef adalah id dan status konsumen.
type ConsumerRef struct {
	ID     uuid.UUID
	Status string
}

// UpsertConsumer membuat konsumen baru untuk blind index ini, atau
// mengembalikan konsumen yang sudah ada (phone_enc lama dipertahankan).
func (r *ConsumerRepository) UpsertConsumer(tx *gorm.DB, id uuid.UUID, phoneEnc, phoneBidx []byte, now time.Time) (ConsumerRef, error) {
	var c ConsumerRef
	err := tx.Raw(`
		INSERT INTO consumers (id, phone_enc, phone_bidx, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT ON CONSTRAINT consumer_phone_once DO UPDATE SET updated_at = consumers.updated_at
		RETURNING id, status`, id, phoneEnc, phoneBidx, now, now).Scan(&c).Error
	if err != nil {
		return ConsumerRef{}, fmt.Errorf("menyimpan konsumen: %w", err)
	}
	return c, nil
}

// CreateConsumerSession menyimpan sesi konsumen baru.
func (r *ConsumerRepository) CreateConsumerSession(tx *gorm.DB, s *model.ConsumerSession) error {
	if err := tx.Create(s).Error; err != nil {
		return fmt.Errorf("membuat sesi konsumen: %w", err)
	}
	return nil
}

// RevokedConsumerSession adalah sesi konsumen yang baru dicabut.
type RevokedConsumerSession struct {
	ID         uuid.UUID
	ConsumerID uuid.UUID
}

// RevokeConsumerSessionByHash mencabut sesi aktif. (nil, nil) kalau tidak ada.
func (r *ConsumerRepository) RevokeConsumerSessionByHash(tx *gorm.DB, tokenHash []byte, now time.Time) (*RevokedConsumerSession, error) {
	var out RevokedConsumerSession
	res := tx.Raw(`
		UPDATE consumer_sessions SET revoked_at = ?
		 WHERE token_hash = ? AND revoked_at IS NULL
		RETURNING id, consumer_id`, now, tokenHash).Scan(&out)
	if res.Error != nil {
		return nil, fmt.Errorf("mencabut sesi konsumen: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &out, nil
}

// ConsumerSessionAuth: sesi konsumen beserta status konsumennya.
type ConsumerSessionAuth struct {
	SessionID      uuid.UUID
	ConsumerID     uuid.UUID
	IdleExpiresAt  time.Time
	ExpiresAt      time.Time
	LastSeenAt     time.Time
	RevokedAt      *time.Time
	ConsumerStatus string
}

// ConsumerSessionByHash memuat sesi dan status konsumen dalam satu query.
func (r *ConsumerRepository) ConsumerSessionByHash(ctx context.Context, tokenHash []byte) (*ConsumerSessionAuth, error) {
	var s ConsumerSessionAuth
	res := r.db.WithContext(ctx).Raw(`
		SELECT cs.id AS session_id, cs.consumer_id, cs.idle_expires_at, cs.expires_at, cs.last_seen_at,
		       cs.revoked_at, c.status AS consumer_status
		  FROM consumer_sessions cs
		  JOIN consumers c ON c.id = cs.consumer_id
		 WHERE cs.token_hash = ?`, tokenHash).Scan(&s)
	if res.Error != nil {
		return nil, fmt.Errorf("memuat sesi konsumen: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &s, nil
}

// TouchConsumerSession memperpanjang idle timeout sesi konsumen aktif.
func (r *ConsumerRepository) TouchConsumerSession(ctx context.Context, id uuid.UUID, now, idleExpiresAt time.Time) error {
	err := r.db.WithContext(ctx).Exec(`
		UPDATE consumer_sessions SET last_seen_at = ?, idle_expires_at = ?
		 WHERE id = ? AND revoked_at IS NULL`, now, idleExpiresAt, id).Error
	if err != nil {
		return fmt.Errorf("memperpanjang sesi konsumen: %w", err)
	}
	return nil
}

// ConsumerPhoneRow: ciphertext nomor HP beserta blind index barisnya (AAD
// enkripsi).
type ConsumerPhoneRow struct {
	PhoneEnc  []byte
	PhoneBidx []byte
}

// ConsumerPhone mengambil ciphertext dan blind index nomor HP konsumen.
// (nil, nil) kalau konsumen tidak ada.
func (r *ConsumerRepository) ConsumerPhone(ctx context.Context, id uuid.UUID) (*ConsumerPhoneRow, error) {
	var c model.Consumer
	res := r.db.WithContext(ctx).Select("id", "phone_enc", "phone_bidx").Where("id = ?", id).Limit(1).Find(&c)
	if res.Error != nil {
		return nil, fmt.Errorf("memuat konsumen: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &ConsumerPhoneRow{PhoneEnc: c.PhoneEnc, PhoneBidx: c.PhoneBidx}, nil
}
