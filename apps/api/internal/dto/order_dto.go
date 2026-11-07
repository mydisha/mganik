package dto

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// CreateOrderRequest: order D2C; harga diambil dari price list channel.
type CreateOrderRequest struct {
	// ChannelCode kosong = D2C_WEB. Hanya channel bertipe D2C.
	ChannelCode string                   `json:"channel_code" binding:"omitempty,max=32"`
	Lines       []CreateOrderLineRequest `json:"lines" binding:"required,min=1,max=50,dive"`
	Note        string                   `json:"note" binding:"max=2000"`
}

type CreateOrderLineRequest struct {
	ProductID string `json:"product_id" binding:"required,uuid"`
	Qty       int64  `json:"qty" binding:"required,gt=0,lte=1000"`
}

type CancelOrderRequest struct {
	Reason string `json:"reason" binding:"required,max=2000"`
}

// FulfillOrderRequest: body opsional; serial unit per line dicatat di
// order_line_serials.
type FulfillOrderRequest struct {
	Serials []FulfillLineSerials `json:"serials" binding:"omitempty,max=50,dive"`
}

type FulfillLineSerials struct {
	OrderLineID   string   `json:"order_line_id" binding:"required,uuid"`
	SerialUnitIDs []string `json:"serial_unit_ids" binding:"required,min=1,max=1000,dive,uuid"`
}

type OrderResponse struct {
	ID                    uuid.UUID         `json:"id"`
	ChannelCode           string            `json:"channel_code"`
	ExternalRef           *string           `json:"external_ref"`
	Status                string            `json:"status"`
	Currency              string            `json:"currency"`
	Subtotal              int64             `json:"subtotal"`
	Discount              int64             `json:"discount"`
	Total                 int64             `json:"total"`
	PlacedAt              time.Time         `json:"placed_at"`
	PaidAt                *time.Time        `json:"paid_at"`
	PaymentExpiresAt      *time.Time        `json:"payment_expires_at"`
	FulfillmentLocationID *uuid.UUID        `json:"fulfillment_location_id"`
	Metadata              json.RawMessage   `json:"metadata"`
	Lines                 []OrderLineDTO    `json:"lines"`
	Reservations          []ReservationDTO  `json:"reservations"`
	History               []OrderHistoryDTO `json:"history"`
	Payments              []OrderPaymentDTO `json:"payments"`
}

type OrderLineDTO struct {
	ID            uuid.UUID   `json:"id"`
	ProductID     uuid.UUID   `json:"product_id"`
	SKU           string      `json:"sku"`
	ProductName   string      `json:"product_name"`
	Qty           int64       `json:"qty"`
	UnitPrice     int64       `json:"unit_price"`
	Discount      int64       `json:"discount"`
	SerialUnitIDs []uuid.UUID `json:"serial_unit_ids"`
}

type ReservationDTO struct {
	ID          uuid.UUID  `json:"id"`
	OrderLineID uuid.UUID  `json:"order_line_id"`
	LocationID  uuid.UUID  `json:"location_id"`
	ProductID   uuid.UUID  `json:"product_id"`
	Qty         int64      `json:"qty"`
	Status      string     `json:"status"`
	ExpiresAt   *time.Time `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	ClosedAt    *time.Time `json:"closed_at"`
	CloseReason *string    `json:"close_reason"`
}

type OrderHistoryDTO struct {
	FromStatus *string   `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Reason     *string   `json:"reason"`
	ActorType  string    `json:"actor_type"`
	At         time.Time `json:"at"`
}

type OrderPaymentDTO struct {
	ID          uuid.UUID `json:"id"`
	Provider    string    `json:"provider"`
	ProviderRef string    `json:"provider_ref"`
	Amount      int64     `json:"amount"`
	Currency    string    `json:"currency"`
	Status      string    `json:"status"`
	ReceivedAt  time.Time `json:"received_at"`
}
