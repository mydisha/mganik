package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/model"
)

const (
	// PaymentProviderMock adalah provider webhook pembayaran mock.
	PaymentProviderMock = "mock"

	PaymentStatusSucceeded = "SUCCEEDED"
	PaymentStatusFailed    = "FAILED"
)

// Hasil pemrosesan webhook pembayaran.
const (
	PaymentOutcomePaid             = "PAID"
	PaymentOutcomeLateReallocated  = "LATE_PAYMENT_REALLOCATED"
	PaymentOutcomeLateNeedsAction  = "LATE_PAYMENT_NEEDS_ACTION"
	PaymentOutcomeFailed           = "PAYMENT_FAILED"
	PaymentOutcomeRefundRequired   = "REFUND_REQUIRED"
	PaymentOutcomeAlreadyProcessed = "ALREADY_PROCESSED"
)

// ErrPaymentRefConflict: provider_ref yang sama dipakai untuk order lain.
var ErrPaymentRefConflict = apperror.New(http.StatusConflict, "payment_ref_conflict",
	"Referensi pembayaran bentrok", "provider_ref ini sudah tercatat untuk order lain; pembayaran tidak dicatat.")

// PaymentService memproses webhook pembayaran.
type PaymentService struct {
	orders *OrderService
}

func NewPaymentService(orders *OrderService) *PaymentService {
	return &PaymentService{orders: orders}
}

// PaymentWebhookInput adalah event pembayaran dari provider (sudah lolos
// verifikasi signature di middleware).
type PaymentWebhookInput struct {
	ProviderRef string
	OrderID     uuid.UUID
	Amount      int64
	Currency    string
	Status      string // SUCCEEDED | FAILED
	Method      *string
	Raw         model.JSON
}

type PaymentWebhookResult struct {
	PaymentID   uuid.UUID
	OrderID     uuid.UUID
	OrderStatus string
	Outcome     string
	Duplicate   bool
}

// HandleMockWebhook memproses satu event pembayaran secara idempotent per
// (provider, provider_ref):
//
//   - event yang sama dikirim ulang (atau bersamaan) → satu pembayaran, satu
//     transisi; kiriman berikutnya ALREADY_PROCESSED;
//   - nominal/mata uang harus sama persis dengan total order;
//   - PENDING_PAYMENT → PAID (transisi bersyarat), reservasi tidak lagi kedaluwarsa;
//   - bayar telat (EXPIRED): pesan ulang stok; berhasil → PAID, gagal →
//     NEEDS_ACTION + refund_required (order tidak pernah PAID tanpa stok);
//   - pembayaran untuk order yang sudah dibayar/dibatalkan → dicatat, refund_required.
//
// Order dikunci lebih dulu, jadi webhook dan sweeper untuk order yang sama berjalan berurutan.
func (s *PaymentService) HandleMockWebhook(ctx context.Context, in PaymentWebhookInput) (PaymentWebhookResult, error) {
	o := s.orders
	method := ""
	if in.Method != nil {
		method = *in.Method
	}
	if err := invCheckText("provider_ref", in.ProviderRef, "currency", in.Currency, "method", method); err != nil {
		return PaymentWebhookResult{}, err
	}
	var out PaymentWebhookResult
	err := invRunTx(ctx, o.db, func(tx *gorm.DB) error {
		order, err := o.orders.LockOrder(tx, in.OrderID)
		if err != nil {
			return err
		}
		if order == nil {
			return ErrOrderNotFound
		}
		now := o.clock.Now() // setelah lock: riwayat status urut
		out = PaymentWebhookResult{OrderID: order.ID, OrderStatus: order.Status}

		if existing, err := o.orders.PaymentByProviderRef(tx, PaymentProviderMock, in.ProviderRef); err != nil {
			return err
		} else if existing != nil {
			if existing.OrderID != order.ID {
				return ErrPaymentRefConflict
			}
			out.PaymentID, out.Outcome, out.Duplicate = existing.ID, PaymentOutcomeAlreadyProcessed, true
			return nil
		}
		if in.Amount != order.Total || in.Currency != order.Currency {
			return ErrPaymentAmountMismatch
		}
		pay := model.Payment{
			ID: invNewID(), OrderID: order.ID, Provider: PaymentProviderMock, ProviderRef: in.ProviderRef,
			Method: in.Method, Amount: in.Amount, Currency: in.Currency, Status: in.Status, Raw: in.Raw,
			ReceivedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		inserted, err := o.orders.InsertPaymentIfAbsent(tx, &pay)
		if err != nil {
			return err
		}
		if !inserted {
			// Tidak terjadi selama order terkunci; tetap aman sebagai duplikat.
			out.Outcome, out.Duplicate = PaymentOutcomeAlreadyProcessed, true
			return nil
		}
		out.PaymentID = pay.ID
		if in.Status != PaymentStatusSucceeded {
			out.Outcome = PaymentOutcomeFailed
			return nil
		}

		switch order.Status {
		case OrderStatusPendingPayment:
			if err := o.transition(tx, order.ID, OrderStatusPendingPayment, OrderStatusPaid, "Pembayaran diterima",
				&now, SystemLedgerActor, now); err != nil {
				return err
			}
			if err := o.orders.ClearReservationExpiry(tx, order.ID); err != nil {
				return err
			}
			out.Outcome, out.OrderStatus = PaymentOutcomePaid, OrderStatusPaid
			return o.emit(tx, EventOrderPaid, order.ID, map[string]any{"payment_id": pay.ID, "amount": pay.Amount}, now)

		case OrderStatusExpired:
			loc, err := o.reserveAllLines(ctx, tx, order.ID, SystemLedgerActor, now)
			if err == nil {
				if err := o.transition(tx, order.ID, OrderStatusExpired, OrderStatusPaid,
					"Pembayaran datang setelah batas waktu; stok berhasil dipesan ulang", &now, SystemLedgerActor, now); err != nil {
					return err
				}
				out.Outcome, out.OrderStatus = PaymentOutcomeLateReallocated, OrderStatusPaid
				return o.emit(tx, EventOrderPaid, order.ID, map[string]any{
					"payment_id": pay.ID, "amount": pay.Amount, "late": true, "fulfillment_location_id": loc,
				}, now)
			}
			if !errors.Is(err, ErrOutOfStock) {
				return err
			}
			if err := o.transition(tx, order.ID, OrderStatusExpired, OrderStatusNeedsAction,
				"Pembayaran datang setelah batas waktu dan stok sudah habis; order perlu refund", &now, SystemLedgerActor, now); err != nil {
				return err
			}
			if err := o.requestRefund(tx, order.ID, "late_payment_out_of_stock", &pay.ID, now); err != nil {
				return err
			}
			out.Outcome, out.OrderStatus = PaymentOutcomeLateNeedsAction, OrderStatusNeedsAction
			return o.emit(tx, EventOrderNeedsAction, order.ID, map[string]any{"reason": "late_payment_out_of_stock"}, now)

		default:
			// Sudah dibayar (pembayaran ganda) atau dibatalkan: catat, minta refund.
			out.Outcome = PaymentOutcomeRefundRequired
			return o.requestRefund(tx, order.ID, "payment_on_"+order.Status, &pay.ID, now)
		}
	})
	return out, err
}
