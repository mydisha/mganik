package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/model"
)

// ErrMarketplaceRefConflict: external_ref yang sama diimpor ulang dengan isi
// berbeda. Ditolak (bukan ditimpa diam-diam) supaya integrasi marketplace
// yang salah kirim atau payload yang dimanipulasi langsung terlihat.
var ErrMarketplaceRefConflict = apperror.New(http.StatusConflict, "external_ref_conflict",
	"Referensi order bentrok", "external_ref ini sudah diimpor dengan isi berbeda; impor ulang ditolak.")

// MarketplaceService mengimpor order yang sudah dibayar di marketplace.
type MarketplaceService struct {
	orders *OrderService
}

func NewMarketplaceService(orders *OrderService) *MarketplaceService {
	return &MarketplaceService{orders: orders}
}

// MarketplaceLine adalah satu baris order marketplace (harga dari marketplace).
type MarketplaceLine struct {
	SKU       string
	Qty       int64
	UnitPrice int64
	Discount  int64
}

type MarketplaceOrderInput struct {
	ChannelCode string
	ExternalRef string
	PlacedAt    *time.Time
	Currency    string
	Lines       []MarketplaceLine
}

type MarketplaceImportResult struct {
	View      OrderView
	Duplicate bool
}

func marketplaceValidateLines(lines []MarketplaceLine) error {
	if len(lines) == 0 || len(lines) > MaxOrderLines {
		return apperror.Validation(apperror.FieldError{Field: "lines", Code: "length", Message: fmt.Sprintf("wajib berisi 1 sampai %d baris", MaxOrderLines)})
	}
	seen := map[string]int{}
	var fields []apperror.FieldError
	for i, l := range lines {
		f := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		if l.Qty <= 0 || l.Qty > MaxOrderLineQty {
			fields = append(fields, apperror.FieldError{Field: f("qty"), Code: "range", Message: "harus 1 sampai " + apperror.FormatNumber(MaxOrderLineQty)})
		}
		if l.UnitPrice < 0 || l.UnitPrice > MaxUnitPrice {
			fields = append(fields, apperror.FieldError{Field: f("unit_price"), Code: "range", Message: "harus Rp0 sampai " + apperror.FormatRupiah(MaxUnitPrice)})
		}
		if l.Discount < 0 || (l.Qty > 0 && l.UnitPrice >= 0 && l.Discount > l.Qty*l.UnitPrice) {
			fields = append(fields, apperror.FieldError{Field: f("discount"), Code: "range", Message: "tidak boleh negatif atau melebihi qty × harga satuan"})
		}
		if j, dup := seen[l.SKU]; dup {
			fields = append(fields, apperror.FieldError{Field: f("sku"), Code: "duplicate", Message: fmt.Sprintf("SKU sama dengan baris %d", j+1)})
		}
		seen[l.SKU] = i
	}
	if len(fields) > 0 {
		return apperror.Validation(fields...)
	}
	return nil
}

// Import mencatat order marketplace yang SUDAH dibayar:
//
//   - dedupe per (channel_code, external_ref): impor ulang (termasuk bersamaan)
//     mengembalikan order yang sama (Duplicate=true);
//   - order langsung PAID dan pembayarannya dicatat;
//   - stok tidak cukup → order tetap dibuat lalu NEEDS_ACTION (PAID →
//     NEEDS_ACTION), bukan ditolak, karena order sudah ada di marketplace.
func (s *MarketplaceService) Import(ctx context.Context, in MarketplaceOrderInput) (MarketplaceImportResult, error) {
	o := s.orders
	if in.Currency == "" {
		in.Currency = "IDR"
	}
	texts := []string{"channel_code", in.ChannelCode, "external_ref", in.ExternalRef, "currency", in.Currency}
	for i, l := range in.Lines {
		texts = append(texts, fmt.Sprintf("lines[%d].sku", i), l.SKU)
	}
	if err := invCheckText(texts...); err != nil {
		return MarketplaceImportResult{}, err
	}
	if err := marketplaceValidateLines(in.Lines); err != nil {
		return MarketplaceImportResult{}, err
	}
	// Hash isi impor (kanonis) untuk mendeteksi external_ref yang sama dengan isi berbeda.
	canonical, err := json.Marshal(in)
	if err != nil {
		return MarketplaceImportResult{}, err
	}
	sum := sha256.Sum256(canonical)
	payloadHash := hex.EncodeToString(sum[:])
	var out MarketplaceImportResult
	err = invRunTx(ctx, o.db, func(tx *gorm.DB) error {
		out = MarketplaceImportResult{}
		now := o.clock.Now()
		if typ, err := o.orders.ChannelType(tx, in.ChannelCode); err != nil {
			return err
		} else if typ != "MARKETPLACE" {
			return ErrOrderChannelInvalid
		}
		skus := make([]string, len(in.Lines))
		for i, l := range in.Lines {
			skus[i] = l.SKU
		}
		products, err := o.orders.ProductsBySKU(tx, skus)
		if err != nil {
			return err
		}
		var fields []apperror.FieldError
		for i, l := range in.Lines {
			if _, ok := products[l.SKU]; !ok {
				fields = append(fields, apperror.FieldError{Field: fmt.Sprintf("lines[%d].sku", i), Code: "not_found", Message: "SKU tidak dikenal"})
			}
		}
		if len(fields) > 0 {
			return apperror.Validation(fields...)
		}

		placed := now
		if in.PlacedAt != nil {
			placed = in.PlacedAt.UTC()
		}
		ref := in.ExternalRef
		order := model.Order{
			ID: invNewID(), ChannelCode: in.ChannelCode, ExternalRef: &ref, Status: OrderStatusPaid,
			Currency: in.Currency, PlacedAt: placed, PaidAt: &now, CreatedAt: now, UpdatedAt: now,
			Metadata: model.MustJSON(map[string]string{"source": "marketplace_import", "payload_hash": payloadHash}),
		}
		lines := make([]model.OrderLine, len(in.Lines))
		alloc := make([]invAllocLine, len(in.Lines))
		for i, l := range in.Lines {
			pid := products[l.SKU].ID
			lines[i] = model.OrderLine{ID: invNewID(), OrderID: order.ID, ProductID: pid, Qty: l.Qty,
				UnitPrice: l.UnitPrice, Discount: l.Discount, CreatedAt: now}
			order.Subtotal += l.Qty * l.UnitPrice
			order.Discount += l.Discount
			alloc[i] = invAllocLine{Index: i, LineID: lines[i].ID, ProductID: pid, Qty: l.Qty}
		}
		order.Total = order.Subtotal - order.Discount

		inserted, err := o.orders.InsertOrderIfAbsent(tx, &order)
		if err != nil {
			return err
		}
		if !inserted {
			existing, err := o.orders.OrderByExternalRef(tx, in.ChannelCode, in.ExternalRef)
			if err != nil {
				return err
			}
			if existing == nil {
				return fmt.Errorf("order %s/%s bentrok tetapi tidak ditemukan", in.ChannelCode, in.ExternalRef)
			}
			var meta struct {
				PayloadHash string `json:"payload_hash"`
			}
			_ = json.Unmarshal(existing.Metadata, &meta)
			if meta.PayloadHash != "" && meta.PayloadHash != payloadHash {
				return ErrMarketplaceRefConflict
			}
			out.View, err = o.loadView(tx, existing.ID)
			out.Duplicate = true
			return err
		}
		if err := o.orders.InsertLines(tx, lines); err != nil {
			return err
		}
		if err := o.initialHistory(tx, order.ID, OrderStatusPaid, "Order marketplace diimpor (sudah dibayar)", SystemLedgerActor, now); err != nil {
			return err
		}
		if order.Total > 0 {
			if _, err := o.orders.InsertPaymentIfAbsent(tx, &model.Payment{
				ID: invNewID(), OrderID: order.ID, Provider: strings.ToLower(in.ChannelCode), ProviderRef: in.ExternalRef,
				Amount: order.Total, Currency: order.Currency, Status: PaymentStatusSucceeded,
				ReceivedAt: now, CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
		}
		if err := o.emit(tx, EventOrderCreated, order.ID, map[string]any{
			"channel_code": order.ChannelCode, "external_ref": in.ExternalRef, "status": OrderStatusPaid, "total": order.Total,
		}, now); err != nil {
			return err
		}

		loc, err := o.allocate(ctx, tx, order.ID, alloc, nil, SystemLedgerActor, now)
		switch {
		case err == nil:
			if err := o.orders.SetFulfillmentLocation(tx, order.ID, loc, now); err != nil {
				return err
			}
		case errors.Is(err, ErrOutOfStock):
			if err := o.transition(tx, order.ID, OrderStatusPaid, OrderStatusNeedsAction,
				"Stok tidak cukup saat impor; order sudah dibayar di marketplace", nil, SystemLedgerActor, now); err != nil {
				return err
			}
			if err := o.orders.PatchMetadata(tx, order.ID, model.MustJSON(map[string]any{
				"needs_action_reason": "insufficient_stock_on_import",
			}), now); err != nil {
				return err
			}
			if err := o.emit(tx, EventOrderNeedsAction, order.ID, map[string]any{"reason": "insufficient_stock_on_import"}, now); err != nil {
				return err
			}
		default:
			return err
		}
		out.View, err = o.loadView(tx, order.ID)
		return err
	})
	return out, err
}
