package model

import (
	"time"

	"github.com/google/uuid"
)

// PosDevice: token device hanya disimpan sebagai SHA-256.
type PosDevice struct {
	ID            uuid.UUID `gorm:"primaryKey;type:uuid"`
	LocationID    uuid.UUID `gorm:"type:uuid;not null"`
	Name          string    `gorm:"not null"`
	TokenHash     []byte    `gorm:"not null"`
	SigningKeyEnc []byte
	Status        string `gorm:"not null;default:ACTIVE"`
	LastSeenAt    *time.Time
	LastAckedSeq  int64      `gorm:"not null"`
	ProvisionedBy *uuid.UUID `gorm:"type:uuid"`
	RevokedAt     *time.Time
	Version       int64 `gorm:"not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (PosDevice) TableName() string { return "pos_devices" }

// Hasil pemrosesan transaksi POS (status teknis, text + CHECK).
const (
	PosResultAccepted             = "ACCEPTED"
	PosResultAcceptedWithConflict = "ACCEPTED_WITH_CONFLICT"
	PosResultRejected             = "REJECTED"
)

type PosTransaction struct {
	ID           uuid.UUID `gorm:"primaryKey;type:uuid"`
	DeviceID     uuid.UUID `gorm:"type:uuid;not null"`
	ClientTxnID  uuid.UUID `gorm:"type:uuid;not null"`
	DeviceSeq    int64     `gorm:"not null"`
	LocationID   uuid.UUID `gorm:"type:uuid;not null"`
	CashierID    uuid.UUID `gorm:"type:uuid;not null"`
	OccurredAt   time.Time `gorm:"not null"`
	ReceivedAt   time.Time `gorm:"not null;default:now()"`
	ClockSkewMs  *int64
	PayloadHash  []byte     `gorm:"not null"`
	Result       string     `gorm:"not null"`
	ResultDetail JSON       `gorm:"type:jsonb;not null;default:'{}'"`
	OrderID      *uuid.UUID `gorm:"type:uuid"`
}

func (PosTransaction) TableName() string { return "pos_transactions" }

type AllocationConflict struct {
	ID               uuid.UUID  `gorm:"primaryKey;type:uuid"`
	Kind             string     `gorm:"not null"`
	PosTransactionID *uuid.UUID `gorm:"type:uuid"`
	ReservationID    *uuid.UUID `gorm:"type:uuid"`
	OrderID          *uuid.UUID `gorm:"type:uuid"`
	LocationID       uuid.UUID  `gorm:"type:uuid;not null"`
	ProductID        uuid.UUID  `gorm:"type:uuid;not null"`
	SerialUnitID     *uuid.UUID `gorm:"type:uuid"`
	Qty              *int64
	Status           string `gorm:"not null;default:OPEN"`
	Resolution       *string
	Details          JSON       `gorm:"type:jsonb;not null;default:'{}'"`
	ResolvedBy       *uuid.UUID `gorm:"type:uuid"`
	ResolvedAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (AllocationConflict) TableName() string { return "allocation_conflicts" }
