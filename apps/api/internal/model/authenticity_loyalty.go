package model

import (
	"time"

	"github.com/google/uuid"
)

// ScanEvent: observasi scan apa adanya, append-only.
type ScanEvent struct {
	ID               uuid.UUID  `gorm:"primaryKey;type:uuid"`
	RawCode          string     `gorm:"not null"`
	SerialUnitID     *uuid.UUID `gorm:"type:uuid"`
	Context          string     `gorm:"not null"`
	ConsumerID       *uuid.UUID `gorm:"type:uuid"`
	ActorFingerprint []byte
	IPHash           []byte `gorm:"column:ip_hash"`
	Lat              *float64
	Lng              *float64
	AccuracyM        *int
	GeoSource        string `gorm:"not null;default:NONE"`
	RegionCode       *string
	Result           string      `gorm:"not null"`
	RuleHits         StringArray `gorm:"type:text[];not null"`
	OccurredAt       time.Time   `gorm:"not null;default:now()"`
}

func (ScanEvent) TableName() string { return "scan_events" }

// AnomalyRule: aturan radar/risiko sebagai data; Params berisi ambang.
type AnomalyRule struct {
	Code           string `gorm:"primaryKey"`
	Name           string `gorm:"not null"`
	Description    string `gorm:"not null"`
	Category       string `gorm:"not null"`
	Enabled        bool   `gorm:"not null"`
	Severity       string `gorm:"not null"`
	ConsumerEffect string `gorm:"not null"`
	Params         JSON   `gorm:"type:jsonb;not null;default:'{}'"`
	Version        int64  `gorm:"not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (AnomalyRule) TableName() string { return "anomaly_rules" }

type Anomaly struct {
	ID             uuid.UUID  `gorm:"primaryKey;type:uuid"`
	RuleCode       string     `gorm:"not null"`
	Severity       string     `gorm:"not null"`
	Status         string     `gorm:"not null;default:OPEN"`
	SubjectType    string     `gorm:"not null"`
	SubjectID      *uuid.UUID `gorm:"type:uuid"`
	PartyID        *uuid.UUID `gorm:"type:uuid"`
	RegionCode     *string
	DedupeKey      string     `gorm:"not null"`
	HitCount       int        `gorm:"not null"`
	Evidence       JSON       `gorm:"type:jsonb;not null;default:'{}'"`
	FirstSeenAt    time.Time  `gorm:"not null"`
	LastSeenAt     time.Time  `gorm:"not null"`
	ResolvedBy     *uuid.UUID `gorm:"type:uuid"`
	ResolvedAt     *time.Time
	ResolutionNote *string
	Version        int64 `gorm:"not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Anomaly) TableName() string { return "anomalies" }

// Status klaim loyalty (status teknis, text + CHECK).
const (
	ClaimPending  = "PENDING"
	ClaimApproved = "APPROVED"
	ClaimRejected = "REJECTED"
	ClaimReversed = "REVERSED"
)

type LoyaltyClaim struct {
	ID           uuid.UUID  `gorm:"primaryKey;type:uuid"`
	SerialUnitID uuid.UUID  `gorm:"type:uuid;not null"`
	ConsumerID   uuid.UUID  `gorm:"type:uuid;not null"`
	Status       string     `gorm:"not null"`
	Points       int64      `gorm:"not null"`
	RiskScore    int        `gorm:"not null"`
	RiskReasons  JSON       `gorm:"type:jsonb;not null;default:'[]'"`
	AvailableAt  time.Time  `gorm:"not null"`
	ScanEventID  *uuid.UUID `gorm:"type:uuid"`
	DecidedBy    *uuid.UUID `gorm:"type:uuid"`
	DecidedAt    *time.Time
	// ReviewRequired: PENDING karena skor risiko (menunggu review manusia),
	// bukan sekadar masa tunggu. Migrasi 00012.
	ReviewRequired bool `gorm:"not null;default:false"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (LoyaltyClaim) TableName() string { return "loyalty_claims" }

// LoyaltyLedgerEntry: baris ledger poin bertanda, append-only.
type LoyaltyLedgerEntry struct {
	ID             uuid.UUID `gorm:"primaryKey;type:uuid"`
	ConsumerID     uuid.UUID `gorm:"type:uuid;not null"`
	EntryType      string    `gorm:"not null"`
	Points         int64     `gorm:"not null"`
	RefType        string    `gorm:"not null"`
	RefID          uuid.UUID `gorm:"type:uuid;not null"`
	IdempotencyKey string    `gorm:"not null"`
	OccurredAt     time.Time `gorm:"not null;default:now()"`
	RecordedAt     time.Time `gorm:"not null;default:now()"`
}

func (LoyaltyLedgerEntry) TableName() string { return "loyalty_ledger" }
