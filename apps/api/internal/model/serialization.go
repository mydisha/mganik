package model

import (
	"time"

	"github.com/google/uuid"
)

type CodeBatch struct {
	ID            uuid.UUID  `gorm:"primaryKey;type:uuid"`
	LevelCode     string     `gorm:"not null"`
	ProductID     *uuid.UUID `gorm:"type:uuid"`
	Qty           int        `gorm:"not null"`
	PrinterVendor string     `gorm:"not null"`
	IssuedAt      time.Time  `gorm:"not null"`
	DeliveredAt   *time.Time
	ReconciledAt  *time.Time
	VoidQty       int `gorm:"not null"`
	Notes         *string
	CreatedBy     *uuid.UUID `gorm:"type:uuid"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (CodeBatch) TableName() string { return "code_batches" }

// Batch: lot produksi. Status ACTIVE/ON_HOLD/RECALLED.
type Batch struct {
	ID           uuid.UUID `gorm:"primaryKey;type:uuid"`
	ProductID    uuid.UUID `gorm:"type:uuid;not null"`
	LotNo        string    `gorm:"not null"`
	Line         *string
	MfgDate      time.Time `gorm:"type:date;not null"`
	ExpDate      time.Time `gorm:"type:date;not null"`
	Status       string    `gorm:"not null;default:ACTIVE"`
	RecalledAt   *time.Time
	RecallReason *string
	Version      int64 `gorm:"not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Batch) TableName() string { return "batches" }

// SerialUnitStatus: status serial sebagai data (lookup).
type SerialUnitStatus struct {
	Code               string `gorm:"primaryKey"`
	Name               string `gorm:"not null"`
	IsSellable         bool   `gorm:"not null"`
	IsTerminal         bool   `gorm:"not null"`
	ConsumerMessageKey string `gorm:"not null"`
	SortOrder          int16  `gorm:"not null"`
	CreatedAt          time.Time
}

func (SerialUnitStatus) TableName() string { return "serial_unit_statuses" }

// Nilai lifecycle_transitions.entity_type.
const (
	EntitySerialUnit = "SERIAL_UNIT"
	EntityOrder      = "ORDER"
)

// LifecycleTransition: transisi status sebagai data. FromCode nil = status awal;
// Permission nil = hanya sistem/konsumen/webhook yang memicu.
type LifecycleTransition struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	EntityType  string `gorm:"not null"`
	FromCode    *string
	ToCode      string `gorm:"not null"`
	BizStep     string `gorm:"not null"`
	Permission  *string
	Description *string
	CreatedAt   time.Time
}

func (LifecycleTransition) TableName() string { return "lifecycle_transitions" }

type SerialUnit struct {
	ID                      uuid.UUID  `gorm:"primaryKey;type:uuid"`
	Code                    string     `gorm:"not null"`
	LevelCode               string     `gorm:"not null"`
	ProductID               *uuid.UUID `gorm:"type:uuid"`
	BatchID                 *uuid.UUID `gorm:"type:uuid"`
	CodeBatchID             *uuid.UUID `gorm:"type:uuid"`
	ParentID                *uuid.UUID `gorm:"type:uuid"`
	Status                  string     `gorm:"not null"`
	LocationID              *uuid.UUID `gorm:"type:uuid"`
	CustodianPartyID        *uuid.UUID `gorm:"type:uuid"`
	ClaimSecretHash         []byte
	ClaimFailedAttempts     int `gorm:"not null"`
	ClaimLockedAt           *time.Time
	CommissionedAt          *time.Time
	FirstVerifiedAt         *time.Time
	FirstVerifiedRegionCode *string
	VerifyCount             int   `gorm:"not null"`
	Attributes              JSON  `gorm:"type:jsonb;not null;default:'{}'"`
	Version                 int64 `gorm:"not null"`
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (SerialUnit) TableName() string { return "serial_units" }

// UnitEvent: riwayat serial ala GS1 EPCIS, append-only.
type UnitEvent struct {
	ID           uuid.UUID `gorm:"primaryKey;type:uuid"`
	SerialUnitID uuid.UUID `gorm:"type:uuid;not null"`
	BizStep      string    `gorm:"not null"`
	Disposition  *string
	FromStatus   *string
	ToStatus     *string
	ParentID     *uuid.UUID `gorm:"type:uuid"`
	LocationID   *uuid.UUID `gorm:"type:uuid"`
	PartyID      *uuid.UUID `gorm:"type:uuid"`
	ActorType    string     `gorm:"not null"`
	ActorID      *uuid.UUID `gorm:"type:uuid"`
	DeviceID     *uuid.UUID `gorm:"type:uuid"`
	RefType      *string
	RefID        *uuid.UUID `gorm:"type:uuid"`
	Payload      JSON       `gorm:"type:jsonb;not null;default:'{}'"`
	OccurredAt   time.Time  `gorm:"not null"`
	RecordedAt   time.Time  `gorm:"not null;default:now()"`
}

func (UnitEvent) TableName() string { return "unit_events" }

type Shipment struct {
	ID             uuid.UUID  `gorm:"primaryKey;type:uuid"`
	Code           string     `gorm:"not null"`
	FromLocationID uuid.UUID  `gorm:"type:uuid;not null"`
	ToLocationID   uuid.UUID  `gorm:"type:uuid;not null"`
	ToPartyID      *uuid.UUID `gorm:"type:uuid"`
	ProgramCode    *string
	Status         string `gorm:"not null;default:DRAFT"`
	ShippedAt      *time.Time
	ReceivedAt     *time.Time
	CreatedBy      *uuid.UUID `gorm:"type:uuid"`
	Version        int64      `gorm:"not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Shipment) TableName() string { return "shipments" }

type ShipmentItem struct {
	ID           uuid.UUID `gorm:"primaryKey;type:uuid"`
	ShipmentID   uuid.UUID `gorm:"type:uuid;not null"`
	SerialUnitID uuid.UUID `gorm:"type:uuid;not null"`
	ProductID    uuid.UUID `gorm:"type:uuid;not null"`
	Qty          int       `gorm:"not null"`
	CreatedAt    time.Time
}

func (ShipmentItem) TableName() string { return "shipment_items" }
