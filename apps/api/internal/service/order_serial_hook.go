package service

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// OrderSerialSale menyambungkan fulfillment order ke serialisasi: serial yang
// dicatat di order_line_serials berpindah IN_STOCK → SOLD di transaksi yang
// sama dengan COMMIT_SALE stok. Serial yang tidak berada di lokasi pemenuhan,
// bukan level UNIT, atau tidak IN_STOCK menggagalkan seluruh fulfill.
type OrderSerialSale struct {
	serials *SerialService
}

// NewOrderSerialSale membuat hook untuk OrderService.SetSerialSaleHook.
func NewOrderSerialSale(serials *SerialService) *OrderSerialSale {
	return &OrderSerialSale{serials: serials}
}

// OnSerialsSold memenuhi SerialSaleHook.
func (h *OrderSerialSale) OnSerialsSold(ctx context.Context, tx *gorm.DB, orderID, locationID uuid.UUID, serialIDs []uuid.UUID, actor LedgerActor) error {
	var codes []string
	if err := tx.WithContext(ctx).Model(&model.SerialUnit{}).Where("id IN ?", serialIDs).Order("code").Pluck("code", &codes).Error; err != nil {
		return err
	}
	_, err := h.serials.SellSerials(ctx, tx, codes, locationID, SaleRef{
		RefType: "ORDER",
		RefID:   orderID,
		Actor:   serialActorFromLedger(actor),
	})
	return err
}

func serialActorFromLedger(a LedgerActor) SerialActor {
	switch a.Type {
	case model.ActorUser:
		return SerialActor{Type: model.ActorUser, UserID: a.ID}
	case model.ActorDevice:
		return SerialActor{Type: model.ActorDevice, DeviceID: a.ID}
	default:
		return SerialSystemActor
	}
}
