package dto

import "github.com/google/uuid"

// PaymentWebhookRequest adalah event dari provider pembayaran mock. Idempotent
// per provider_ref.
type PaymentWebhookRequest struct {
	ProviderRef string `json:"provider_ref" binding:"required,max=128"`
	OrderID     string `json:"order_id" binding:"required,uuid"`
	Amount      int64  `json:"amount" binding:"required,gt=0"`
	Currency    string `json:"currency" binding:"required,len=3"`
	Status      string `json:"status" binding:"required,oneof=SUCCEEDED FAILED"`
	Method      string `json:"method" binding:"omitempty,max=32"`
}

// PaymentWebhookResponse.outcome: PAID, LATE_PAYMENT_REALLOCATED,
// LATE_PAYMENT_NEEDS_ACTION, PAYMENT_FAILED, REFUND_REQUIRED, ALREADY_PROCESSED.
type PaymentWebhookResponse struct {
	PaymentID   *uuid.UUID `json:"payment_id"`
	OrderID     uuid.UUID  `json:"order_id"`
	OrderStatus string     `json:"order_status"`
	Outcome     string     `json:"outcome"`
	Duplicate   bool       `json:"duplicate"`
}
