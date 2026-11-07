package model

import (
	"time"

	"github.com/google/uuid"
)

// OutboxEvent: efek samping yang diterbitkan worker setelah transaksi commit.
type OutboxEvent struct {
	ID            uuid.UUID `gorm:"primaryKey;type:uuid"`
	EventType     string    `gorm:"not null"`
	AggregateType string    `gorm:"not null"`
	AggregateID   uuid.UUID `gorm:"type:uuid;not null"`
	Payload       JSON      `gorm:"type:jsonb;not null"`
	Attempts      int       `gorm:"not null"`
	NextAttemptAt time.Time `gorm:"not null;default:now()"`
	PublishedAt   *time.Time
	LastError     *string
	// DeadAt diisi saat event gagal OUTBOX_MAX_ATTEMPTS kali (dead-letter).
	DeadAt    *time.Time
	CreatedAt time.Time `gorm:"not null"`
}

func (OutboxEvent) TableName() string { return "outbox_events" }

// Status idempotency key (status teknis, text + CHECK).
const (
	IdempotencyInProgress = "IN_PROGRESS"
	IdempotencyDone       = "DONE"
)

type IdempotencyKey struct {
	ID           uuid.UUID `gorm:"primaryKey;type:uuid"`
	Scope        string    `gorm:"not null"`
	Key          string    `gorm:"not null"`
	RequestHash  []byte    `gorm:"not null"`
	State        string    `gorm:"not null;default:IN_PROGRESS"`
	ResponseCode *int
	ResponseBody JSON `gorm:"type:jsonb"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExpiresAt    time.Time `gorm:"not null"`
}

func (IdempotencyKey) TableName() string { return "idempotency_keys" }

// AuditLog bersifat append-only (trigger forbid_mutation).
type AuditLog struct {
	ID        uuid.UUID  `gorm:"primaryKey;type:uuid"`
	ActorType string     `gorm:"not null"`
	ActorID   *uuid.UUID `gorm:"type:uuid"`
	Action    string     `gorm:"not null"`
	Entity    string     `gorm:"not null"`
	EntityID  *uuid.UUID `gorm:"type:uuid"`
	Before    JSON       `gorm:"type:jsonb"`
	After     JSON       `gorm:"type:jsonb"`
	Reason    *string
	RequestID *string
	IPHash    []byte    `gorm:"column:ip_hash"`
	At        time.Time `gorm:"not null;default:now()"`
}

func (AuditLog) TableName() string { return "audit_logs" }
