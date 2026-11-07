package model

import (
	"time"

	"github.com/google/uuid"
)

// ConsumerSession: sesi konsumen hasil OTP; hanya hash SHA-256 token disimpan.
type ConsumerSession struct {
	ID            uuid.UUID `gorm:"primaryKey;type:uuid"`
	ConsumerID    uuid.UUID `gorm:"type:uuid;not null"`
	TokenHash     []byte    `gorm:"not null"`
	IdleExpiresAt time.Time `gorm:"not null"`
	ExpiresAt     time.Time `gorm:"not null"`
	RevokedAt     *time.Time
	LastSeenAt    time.Time `gorm:"not null;default:now()"`
	IPHash        []byte    `gorm:"column:ip_hash"`
	CreatedAt     time.Time
}

func (ConsumerSession) TableName() string { return "consumer_sessions" }

// OTPChallenge: satu permintaan OTP. Nomor HP hanya sebagai blind index.
type OTPChallenge struct {
	ID          uuid.UUID `gorm:"primaryKey;type:uuid"`
	PhoneBidx   []byte    `gorm:"not null"`
	CodeHash    []byte    `gorm:"not null"`
	Attempts    int       `gorm:"not null"`
	MaxAttempts int       `gorm:"not null"`
	ExpiresAt   time.Time `gorm:"not null"`
	ConsumedAt  *time.Time
	IPHash      []byte `gorm:"column:ip_hash"`
	CreatedAt   time.Time
}

func (OTPChallenge) TableName() string { return "otp_challenges" }
