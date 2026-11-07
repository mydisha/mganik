package model

import (
	"time"

	"github.com/google/uuid"
)

type OrderStatus struct {
	Code       string `gorm:"primaryKey"`
	Name       string `gorm:"not null"`
	IsTerminal bool   `gorm:"not null"`
	HoldsStock bool   `gorm:"not null"`
	SortOrder  int16  `gorm:"not null"`
	CreatedAt  time.Time
}

func (OrderStatus) TableName() string { return "order_statuses" }

// Order: nominal dalam rupiah utuh.
type Order struct {
	ID                    uuid.UUID  `gorm:"primaryKey;type:uuid"`
	ChannelCode           string     `gorm:"not null"`
	ExternalRef           *string    `gorm:"column:external_ref"`
	ConsumerID            *uuid.UUID `gorm:"type:uuid"`
	FulfillmentLocationID *uuid.UUID `gorm:"type:uuid"`
	Status                string     `gorm:"not null"`
	Currency              string     `gorm:"not null;default:IDR"`
	Subtotal              int64      `gorm:"not null"`
	Discount              int64      `gorm:"not null"`
	Total                 int64      `gorm:"not null"`
	PlacedAt              time.Time  `gorm:"not null"`
	PaidAt                *time.Time
	PaymentExpiresAt      *time.Time
	Metadata              JSON  `gorm:"type:jsonb;not null;default:'{}'"`
	Version               int64 `gorm:"not null"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (Order) TableName() string { return "orders" }

type OrderLine struct {
	ID        uuid.UUID `gorm:"primaryKey;type:uuid"`
	OrderID   uuid.UUID `gorm:"type:uuid;not null"`
	ProductID uuid.UUID `gorm:"type:uuid;not null"`
	Qty       int64     `gorm:"not null"`
	UnitPrice int64     `gorm:"not null"`
	Discount  int64     `gorm:"not null"`
	CreatedAt time.Time
}

func (OrderLine) TableName() string { return "order_lines" }

type OrderLineSerial struct {
	OrderLineID  uuid.UUID `gorm:"primaryKey;type:uuid"`
	SerialUnitID uuid.UUID `gorm:"primaryKey;type:uuid"`
	AssignedAt   time.Time `gorm:"not null;default:now()"`
}

func (OrderLineSerial) TableName() string { return "order_line_serials" }

// OrderStatusHistory bersifat append-only.
type OrderStatusHistory struct {
	ID         uuid.UUID `gorm:"primaryKey;type:uuid"`
	OrderID    uuid.UUID `gorm:"type:uuid;not null"`
	FromStatus *string
	ToStatus   string `gorm:"not null"`
	Reason     *string
	ActorType  string     `gorm:"not null"`
	ActorID    *uuid.UUID `gorm:"type:uuid"`
	At         time.Time  `gorm:"not null;default:now()"`
}

func (OrderStatusHistory) TableName() string { return "order_status_history" }

type Payment struct {
	ID          uuid.UUID `gorm:"primaryKey;type:uuid"`
	OrderID     uuid.UUID `gorm:"type:uuid;not null"`
	Provider    string    `gorm:"not null"`
	ProviderRef string    `gorm:"not null"`
	Method      *string
	Amount      int64     `gorm:"not null"`
	Currency    string    `gorm:"not null;default:IDR"`
	Status      string    `gorm:"not null"`
	Raw         JSON      `gorm:"type:jsonb;not null;default:'{}'"`
	ReceivedAt  time.Time `gorm:"not null;default:now()"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Payment) TableName() string { return "payments" }

// Status reservasi (status teknis, text + CHECK).
const (
	ReservationActive    = "ACTIVE"
	ReservationCommitted = "COMMITTED"
	ReservationReleased  = "RELEASED"
	ReservationExpired   = "EXPIRED"
	ReservationPreempted = "PREEMPTED"
)

type StockReservation struct {
	ID          uuid.UUID `gorm:"primaryKey;type:uuid"`
	OrderID     uuid.UUID `gorm:"type:uuid;not null"`
	OrderLineID uuid.UUID `gorm:"type:uuid;not null"`
	LocationID  uuid.UUID `gorm:"type:uuid;not null"`
	ProductID   uuid.UUID `gorm:"type:uuid;not null"`
	Qty         int64     `gorm:"not null"`
	Status      string    `gorm:"not null"`
	ExpiresAt   *time.Time
	CreatedAt   time.Time
	ClosedAt    *time.Time
	CloseReason *string
}

func (StockReservation) TableName() string { return "stock_reservations" }
