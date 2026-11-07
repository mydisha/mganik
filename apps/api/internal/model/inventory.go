package model

import (
	"time"

	"github.com/google/uuid"
)

// MovementType mendefinisikan efek mutasi ke tiga bucket stok (-1, 0, 1).
type MovementType struct {
	Code            string `gorm:"primaryKey"`
	Name            string `gorm:"not null"`
	PhysicalSign    int16  `gorm:"not null"`
	ReservedSign    int16  `gorm:"not null"`
	QuarantineSign  int16  `gorm:"not null"`
	IsPromise       bool   `gorm:"not null"`
	AllowEitherSign bool   `gorm:"not null"`
	CreatedAt       time.Time
}

func (MovementType) TableName() string { return "movement_types" }

// InventoryBalance: proyeksi saldo per (lokasi, produk). AvailableQty adalah
// generated column, hanya dibaca.
type InventoryBalance struct {
	LocationID    uuid.UUID `gorm:"primaryKey;type:uuid"`
	ProductID     uuid.UUID `gorm:"primaryKey;type:uuid"`
	PhysicalQty   int64     `gorm:"not null"`
	ReservedQty   int64     `gorm:"not null"`
	QuarantineQty int64     `gorm:"not null"`
	AvailableQty  int64     `gorm:"->"`
	Version       int64     `gorm:"not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (InventoryBalance) TableName() string { return "inventory_balances" }

// InventoryMovement: baris ledger append-only.
type InventoryMovement struct {
	ID              uuid.UUID `gorm:"primaryKey;type:uuid"`
	LocationID      uuid.UUID `gorm:"type:uuid;not null"`
	ProductID       uuid.UUID `gorm:"type:uuid;not null"`
	MovementType    string    `gorm:"not null"`
	PhysicalDelta   int64     `gorm:"not null"`
	ReservedDelta   int64     `gorm:"not null"`
	QuarantineDelta int64     `gorm:"not null"`
	RefType         string    `gorm:"not null"`
	RefID           uuid.UUID `gorm:"type:uuid;not null"`
	IdempotencyKey  string    `gorm:"not null"`
	Reason          *string
	ActorType       string     `gorm:"not null"`
	ActorID         *uuid.UUID `gorm:"type:uuid"`
	OccurredAt      time.Time  `gorm:"not null"`
	RecordedAt      time.Time  `gorm:"not null;default:now()"`
}

func (InventoryMovement) TableName() string { return "inventory_movements" }

type StockPolicy struct {
	LocationID      uuid.UUID `gorm:"primaryKey;type:uuid"`
	ProductID       uuid.UUID `gorm:"primaryKey;type:uuid"`
	MinQty          int64     `gorm:"not null"`
	OnlineBufferQty int64     `gorm:"not null"`
	LeadTimeDays    int       `gorm:"not null"`
	SafetyDays      int       `gorm:"not null"`
	Version         int64     `gorm:"not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (StockPolicy) TableName() string { return "stock_policies" }
