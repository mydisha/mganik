package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// Status order (order_statuses.code) yang dipakai engine.
const (
	OrderStatusPendingPayment = "PENDING_PAYMENT"
	OrderStatusPaid           = "PAID"
	OrderStatusFulfilling     = "FULFILLING"
	OrderStatusShipped        = "SHIPPED"
	OrderStatusExpired        = "EXPIRED"
	OrderStatusNeedsAction    = "NEEDS_ACTION"
	OrderStatusCancelled      = "CANCELLED"
)

// Event outbox domain order. Kontrak untuk konsumen lain (Command Center,
// notifikasi): payload selalu memuat order_id.
const (
	EventOrderCreated         = "order.created.v1"
	EventOrderPaid            = "order.paid.v1"
	EventOrderExpired         = "order.expired.v1"
	EventOrderCancelled       = "order.cancelled.v1"
	EventOrderShipped         = "order.shipped.v1"
	EventOrderNeedsAction     = "order.needs_action.v1"
	EventOrderReallocated     = "order.reallocated.v1"
	EventOrderRefundRequested = "order.refund_requested.v1"
)

const (
	// MaxOrderLines dan MaxOrderLineQty membatasi isi satu order (validasi boundary).
	MaxOrderLines   = 50
	MaxOrderLineQty = 1000
	// MaxUnitPrice membatasi harga per unit dari channel luar (rupiah) supaya
	// total tidak pernah overflow int64.
	MaxUnitPrice = 1_000_000_000_000
	// DefaultOrderChannel adalah channel order yang dibuat lewat POST /orders.
	DefaultOrderChannel = "D2C_WEB"
)

// SerialSaleHook dipanggil di dalam transaksi fulfill setelah serial dicatat
// di order_line_serials.
//
// TODO(Fase 2 serialisasi): implementasi mengubah status serial unit ke SOLD
// (conditional update + unit_events, biz_step retail_selling). Fase 3 sengaja
// tidak memanggil kode serialisasi; hook nil = hanya order_line_serials yang
// ditulis (UNIQUE serial_sold_once tetap mencegah satu unit terjual dua kali).
type SerialSaleHook interface {
	OnSerialsSold(ctx context.Context, tx *gorm.DB, orderID, locationID uuid.UUID, serialIDs []uuid.UUID, actor LedgerActor) error
}

// OrderService mengelola siklus hidup order omnichannel: reservasi stok,
// pembatalan, pemenuhan, kedaluwarsa, dan realokasi setelah preempt.
type OrderService struct {
	db             *gorm.DB
	clock          clock.Clock
	inv            *InventoryService
	idem           *IdempotencyService
	invRepo        repository.InventoryRepository
	orders         repository.OrderRepository
	outbox         repository.OutboxRepository
	reservationTTL time.Duration
	serialHook     SerialSaleHook
}

func NewOrderService(db *gorm.DB, clk clock.Clock, inv *InventoryService, idem *IdempotencyService, reservationTTL time.Duration) *OrderService {
	if clk == nil {
		clk = clock.Real{}
	}
	if reservationTTL <= 0 {
		reservationTTL = 15 * time.Minute
	}
	return &OrderService{db: db, clock: clk, inv: inv, idem: idem, reservationTTL: reservationTTL}
}

// SetSerialSaleHook memasang integrasi serialisasi (Fase 2). Dipanggil saat wiring.
func (s *OrderService) SetSerialSaleHook(h SerialSaleHook) { s.serialHook = h }

// OrderView adalah order lengkap untuk respons API.
type OrderView struct {
	Order        model.Order
	Lines        []repository.LineRow
	Reservations []model.StockReservation
	History      []model.OrderStatusHistory
	Payments     []model.Payment
	Serials      []model.OrderLineSerial
}

// loadView membaca order beserta turunannya dalam lima query tetap (tanpa N+1).
func (s *OrderService) loadView(tx *gorm.DB, orderID uuid.UUID) (OrderView, error) {
	o, err := s.orders.OrderByID(tx, orderID)
	if err != nil {
		return OrderView{}, err
	}
	if o == nil {
		return OrderView{}, ErrOrderNotFound
	}
	v := OrderView{Order: *o}
	if v.Lines, err = s.orders.Lines(tx, orderID); err != nil {
		return OrderView{}, err
	}
	if v.Reservations, err = s.orders.Reservations(tx, orderID); err != nil {
		return OrderView{}, err
	}
	if v.History, err = s.orders.History(tx, orderID); err != nil {
		return OrderView{}, err
	}
	if v.Payments, err = s.orders.Payments(tx, orderID); err != nil {
		return OrderView{}, err
	}
	if v.Serials, err = s.orders.LineSerials(tx, orderID); err != nil {
		return OrderView{}, err
	}
	return v, nil
}

// ------------------------------------------------------------------ buat order

// CreateOrderLine adalah satu baris order D2C.
type CreateOrderLine struct {
	ProductID uuid.UUID `json:"product_id"`
	Qty       int64     `json:"qty"`
}

// CreateOrderInput adalah order D2C baru (harga dari price list channel).
type CreateOrderInput struct {
	ChannelCode string            `json:"channel_code"`
	Lines       []CreateOrderLine `json:"lines"`
	Note        string            `json:"note,omitempty"`
}

func orderValidateLines(lines []CreateOrderLine) error {
	if len(lines) == 0 || len(lines) > MaxOrderLines {
		return apperror.Validation(apperror.FieldError{
			Field: "lines", Code: "length", Message: fmt.Sprintf("wajib berisi 1-%d baris", MaxOrderLines),
		})
	}
	seen := make(map[uuid.UUID]int, len(lines))
	var fields []apperror.FieldError
	for i, l := range lines {
		if l.Qty <= 0 || l.Qty > MaxOrderLineQty {
			fields = append(fields, apperror.FieldError{
				Field: fmt.Sprintf("lines[%d].qty", i), Code: "range", Message: fmt.Sprintf("harus 1-%d", MaxOrderLineQty),
			})
		}
		if l.ProductID == uuid.Nil {
			fields = append(fields, apperror.FieldError{Field: fmt.Sprintf("lines[%d].product_id", i), Code: "required", Message: "wajib diisi"})
			continue
		}
		if j, dup := seen[l.ProductID]; dup {
			fields = append(fields, apperror.FieldError{
				Field: fmt.Sprintf("lines[%d].product_id", i), Code: "duplicate",
				Message: fmt.Sprintf("produk sama dengan lines[%d]; gabungkan qty-nya", j),
			})
			continue
		}
		seen[l.ProductID] = i
	}
	if len(fields) > 0 {
		return apperror.Validation(fields...)
	}
	return nil
}

// IdempotentResponse adalah respons HTTP yang disimpan di idempotency_keys.
type IdempotentResponse struct {
	Status   int
	Body     []byte
	Replayed bool
}

// CreateIdempotent membuat order D2C dengan perlindungan Idempotency-Key:
// request ulang dengan key dan isi sama mendapat respons yang sama persis,
// isi berbeda ditolak 422, dan request bersamaan hanya menghasilkan satu order.
// render memetakan OrderView ke body respons (DTO milik controller).
func (s *OrderService) CreateIdempotent(ctx context.Context, p *auth.Principal, idempotencyKey string,
	in CreateOrderInput, render func(OrderView) any) (IdempotentResponse, error) {
	if p == nil {
		return IdempotentResponse{}, apperror.ErrUnauthenticated
	}
	if err := invCheckText("channel_code", in.ChannelCode, "note", in.Note); err != nil {
		return IdempotentResponse{}, err
	}
	if in.ChannelCode == "" {
		in.ChannelCode = DefaultOrderChannel
	}
	if err := orderValidateLines(in.Lines); err != nil {
		return IdempotentResponse{}, err
	}
	note, n := invTrimmed(in.Note)
	if n > 500 {
		return IdempotentResponse{}, apperror.Validation(apperror.FieldError{Field: "note", Code: "max", Message: "maksimal 500 karakter"})
	}
	in.Note = note
	canonical, err := json.Marshal(in)
	if err != nil {
		return IdempotentResponse{}, err
	}
	hash := sha256.Sum256(canonical)
	scope := fmt.Sprintf("orders.create:%s:%s", p.Kind, p.ID)
	actor := LedgerActorFromPrincipal(p)
	return s.idem.Do(ctx, scope, idempotencyKey, hash[:], func(tx *gorm.DB) (int, any, error) {
		v, err := s.createTx(ctx, tx, in, actor)
		if err != nil {
			return 0, nil, err
		}
		return 201, render(v), nil
	})
}

func (s *OrderService) createTx(ctx context.Context, tx *gorm.DB, in CreateOrderInput, actor LedgerActor) (OrderView, error) {
	now := s.clock.Now()
	if typ, err := s.orders.ChannelType(tx, in.ChannelCode); err != nil {
		return OrderView{}, err
	} else if typ != "D2C" {
		return OrderView{}, ErrOrderChannelInvalid
	}
	ids := make([]uuid.UUID, len(in.Lines))
	for i, l := range in.Lines {
		ids[i] = l.ProductID
	}
	products, err := s.orders.ProductsByID(tx, ids)
	if err != nil {
		return OrderView{}, err
	}
	prices, err := s.orders.Prices(tx, in.ChannelCode, ids, now)
	if err != nil {
		return OrderView{}, err
	}
	var fields []apperror.FieldError
	for i, l := range in.Lines {
		if _, ok := products[l.ProductID]; !ok {
			fields = append(fields, apperror.FieldError{Field: fmt.Sprintf("lines[%d].product_id", i), Code: "not_found", Message: "produk tidak ditemukan"})
		} else if _, ok := prices[l.ProductID]; !ok {
			fields = append(fields, apperror.FieldError{Field: fmt.Sprintf("lines[%d].product_id", i), Code: "price_not_found", Message: "produk belum punya harga di channel ini"})
		}
	}
	if len(fields) > 0 {
		return OrderView{}, apperror.Validation(fields...)
	}

	expires := now.Add(s.reservationTTL)
	order := model.Order{
		ID: invNewID(), ChannelCode: in.ChannelCode, Status: OrderStatusPendingPayment, Currency: "IDR",
		PlacedAt: now, PaymentExpiresAt: &expires, CreatedAt: now, UpdatedAt: now,
	}
	if in.Note != "" {
		order.Metadata = model.MustJSON(map[string]string{"note": in.Note})
	}
	lines := make([]model.OrderLine, len(in.Lines))
	alloc := make([]invAllocLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = model.OrderLine{ID: invNewID(), OrderID: order.ID, ProductID: l.ProductID, Qty: l.Qty,
			UnitPrice: prices[l.ProductID], CreatedAt: now}
		order.Subtotal += l.Qty * prices[l.ProductID]
		alloc[i] = invAllocLine{Index: i, LineID: lines[i].ID, ProductID: l.ProductID, Qty: l.Qty}
	}
	order.Total = order.Subtotal
	if err := s.orders.InsertOrder(tx, &order); err != nil {
		return OrderView{}, err
	}
	if err := s.orders.InsertLines(tx, lines); err != nil {
		return OrderView{}, err
	}
	loc, err := s.allocate(ctx, tx, order.ID, alloc, &expires, actor, now)
	if err != nil {
		return OrderView{}, err
	}
	if err := s.orders.SetFulfillmentLocation(tx, order.ID, loc, now); err != nil {
		return OrderView{}, err
	}
	if err := s.initialHistory(tx, order.ID, OrderStatusPendingPayment, "Order dibuat, stok dipesan", actor, now); err != nil {
		return OrderView{}, err
	}
	if err := s.emit(tx, EventOrderCreated, order.ID, map[string]any{
		"channel_code": order.ChannelCode, "status": order.Status, "total": order.Total,
		"fulfillment_location_id": loc, "payment_expires_at": expires,
	}, now); err != nil {
		return OrderView{}, err
	}
	return s.loadView(tx, order.ID)
}

// ------------------------------------------------------------------ alokasi

type invAllocLine struct {
	// Index adalah posisi line di request/order (untuk pesan error per line).
	Index     int
	LineID    uuid.UUID
	ProductID uuid.UUID
	Qty       int64
}

// invOutOfStock membuat 409 out_of_stock yang menyebut line mana yang kurang.
// Error tetap errors.Is(ErrOutOfStock) lewat Unwrap.
func invOutOfStock(lines []invAllocLine, maxATP map[uuid.UUID]int64) error {
	var fields []apperror.FieldError
	for _, l := range lines {
		if atp := maxATP[l.ProductID]; atp < l.Qty {
			fields = append(fields, apperror.FieldError{
				Field: fmt.Sprintf("lines[%d].qty", l.Index), Code: "insufficient_stock",
				Message: fmt.Sprintf("stok online tersedia maksimal %d di satu lokasi", atp),
			})
		}
	}
	if len(fields) == 0 {
		// Tiap line tersedia di suatu lokasi, tetapi tidak bersama di satu lokasi.
		for _, l := range lines {
			fields = append(fields, apperror.FieldError{
				Field: fmt.Sprintf("lines[%d].qty", l.Index), Code: "not_at_single_location",
				Message: "tidak tersedia bersama line lain di satu lokasi fulfillment",
			})
		}
	}
	e := apperror.Wrap(ErrOutOfStock, ErrOutOfStock.Status, ErrOutOfStock.Code, ErrOutOfStock.Title, ErrOutOfStock.Detail)
	e.Fields = fields
	return e
}

// allocate memesan seluruh line di SATU lokasi fulfillment (all-or-nothing).
// Urutan kandidat: gudang pusat, hub, lalu lokasi lain yang
// online_fulfillment_enabled. ATP online toko = available − buffer online, dan
// 0 kalau POS-nya OFFLINE. Line diurutkan per produk sebelum dikunci supaya
// urutan lock (lokasi, produk) konsisten antar transaksi (anti deadlock).
// Tiap kandidat dicoba di SAVEPOINT; kalau satu line gagal, seluruh reservasi
// di lokasi itu dibatalkan lalu kandidat berikutnya dicoba.
func (s *OrderService) allocate(ctx context.Context, tx *gorm.DB, orderID uuid.UUID, lines []invAllocLine,
	expiresAt *time.Time, actor LedgerActor, now time.Time) (uuid.UUID, error) {
	sorted := slices.Clone(lines)
	slices.SortFunc(sorted, func(a, b invAllocLine) int { return bytes.Compare(a.ProductID[:], b.ProductID[:]) })
	ids := make([]uuid.UUID, len(sorted))
	for i, l := range sorted {
		ids[i] = l.ProductID
	}
	rows, err := s.invRepo.FulfillmentCandidates(tx, ids)
	if err != nil {
		return uuid.Nil, err
	}

	type candidate struct {
		id        uuid.UUID
		syncState string
		stock     map[uuid.UUID]repository.CandidateRow
	}
	var cands []*candidate
	byID := map[uuid.UUID]*candidate{}
	for _, r := range rows {
		c, ok := byID[r.LocationID]
		if !ok {
			c = &candidate{id: r.LocationID, syncState: r.SyncState, stock: map[uuid.UUID]repository.CandidateRow{}}
			byID[r.LocationID] = c
			cands = append(cands, c)
		}
		c.stock[r.ProductID] = r
	}
	// ATP online terbesar per produk di satu lokasi, untuk pesan error per line.
	maxATP := map[uuid.UUID]int64{}
	for _, c := range cands {
		for pid, r := range c.stock {
			maxATP[pid] = max(maxATP[pid], domain.OnlineATP(r.AvailableQty, r.OnlineBufferQty, c.syncState))
		}
	}

	for _, c := range cands {
		if c.syncState == domain.SyncStateOffline {
			continue
		}
		enough := true
		for _, l := range sorted {
			r, ok := c.stock[l.ProductID]
			if !ok || domain.OnlineATP(r.AvailableQty, r.OnlineBufferQty, c.syncState) < l.Qty {
				enough = false
				break
			}
		}
		if !enough {
			continue
		}
		err := invSavepoint(tx, "inv_alloc", func() error {
			rs := make([]model.StockReservation, 0, len(sorted))
			for _, l := range sorted {
				resID := invNewID()
				if _, err := s.inv.ApplyMovement(ctx, tx, MovementInput{
					LocationID: c.id, ProductID: l.ProductID, Type: MovementReserve, Qty: l.Qty,
					RefType: "ORDER", RefID: orderID, IdempotencyKey: "reservation:" + resID.String() + ":reserve",
					Actor: actor, OccurredAt: now, KeepAvailable: c.stock[l.ProductID].OnlineBufferQty,
				}); err != nil {
					return err
				}
				rs = append(rs, model.StockReservation{
					ID: resID, OrderID: orderID, OrderLineID: l.LineID, LocationID: c.id, ProductID: l.ProductID,
					Qty: l.Qty, Status: model.ReservationActive, ExpiresAt: expiresAt, CreatedAt: now,
				})
			}
			return s.orders.InsertReservations(tx, rs)
		})
		if errors.Is(err, ErrInsufficientStock) {
			continue // kalah balapan di lokasi ini; coba lokasi berikutnya
		}
		if err != nil {
			return uuid.Nil, err
		}
		return c.id, nil
	}
	return uuid.Nil, invOutOfStock(lines, maxATP)
}

// releaseActive melepas semua reservasi ACTIVE order (RELEASE) dan menutupnya
// dengan status akhir closeStatus.
func (s *OrderService) releaseActive(ctx context.Context, tx *gorm.DB, orderID uuid.UUID, closeStatus, reason string,
	actor LedgerActor, now time.Time) error {
	rs, err := s.orders.LockActiveReservations(tx, orderID)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if _, err := s.inv.ApplyMovement(ctx, tx, MovementInput{
			LocationID: r.LocationID, ProductID: r.ProductID, Type: MovementRelease, Qty: r.Qty,
			RefType: "ORDER", RefID: orderID, IdempotencyKey: "reservation:" + r.ID.String() + ":release",
			Reason: &reason, Actor: actor, OccurredAt: now,
		}); err != nil {
			return err
		}
		if _, err := s.orders.CloseReservation(tx, r.ID, closeStatus, reason, now); err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------------ status

func (s *OrderService) initialHistory(tx *gorm.DB, orderID uuid.UUID, to, reason string, actor LedgerActor, now time.Time) error {
	ok, err := s.orders.InsertHistory(tx, &model.OrderStatusHistory{
		ID: invNewID(), OrderID: orderID, ToStatus: to, Reason: &reason, ActorType: actor.Type, ActorID: actor.ID, At: now,
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrOrderInvalidTransition
	}
	return nil
}

// transition mengubah status secara bersyarat (lifecycle_transitions) dan
// menulis riwayatnya. Gagal → ErrOrderInvalidTransition.
func (s *OrderService) transition(tx *gorm.DB, orderID uuid.UUID, from, to, reason string, paidAt *time.Time,
	actor LedgerActor, now time.Time) error {
	ok, err := s.orders.Transition(tx, orderID, from, to, paidAt, now)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOrderInvalidTransition
	}
	_, err = s.orders.InsertHistory(tx, &model.OrderStatusHistory{
		ID: invNewID(), OrderID: orderID, FromStatus: &from, ToStatus: to, Reason: &reason,
		ActorType: actor.Type, ActorID: actor.ID, At: now,
	})
	return err
}

func (s *OrderService) emit(tx *gorm.DB, eventType string, orderID uuid.UUID, fields map[string]any, now time.Time) error {
	payload := map[string]any{"order_id": orderID, "occurred_at": now}
	for k, v := range fields {
		payload[k] = v
	}
	return s.outbox.Insert(tx, eventType, "ORDER", orderID, model.MustJSON(payload), now)
}

// orderAuthorize: principal ber-scope ALL boleh semua order; principal
// SELECTED hanya order yang lokasi fulfillment-nya di dalam scope. Lokasi
// tambahan (reservasi hasil realokasi) dicek di aksi yang menyentuhnya.
func orderAuthorize(p *auth.Principal, o *model.Order) error {
	if o.FulfillmentLocationID == nil {
		return invScopeAllows(p) // tanpa lokasi: hanya principal ber-scope ALL
	}
	return invScopeAllows(p, *o.FulfillmentLocationID)
}

// orderAuthorizeMutation: aksi yang mengubah stok (batal, fulfill) pada order
// yang mungkin terpecah (split fulfillment) mewajibkan principal berwenang di
// lokasi fulfillment DAN di SEMUA lokasi reservasi aktifnya.
func orderAuthorizeMutation(p *auth.Principal, o *model.Order, active []model.StockReservation) error {
	locs := make([]uuid.UUID, 0, len(active)+1)
	if o.FulfillmentLocationID != nil {
		locs = append(locs, *o.FulfillmentLocationID)
	}
	for _, r := range active {
		locs = append(locs, r.LocationID)
	}
	return invScopeAllows(p, locs...)
}

// ------------------------------------------------------------------ baca, batal, fulfill

// Get membaca order (permission order.read + scope lokasi).
func (s *OrderService) Get(ctx context.Context, p *auth.Principal, orderID uuid.UUID) (OrderView, error) {
	var view OrderView
	// Satu snapshot (REPEATABLE READ, read-only) untuk lima query view.
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		o, err := s.orders.OrderByID(tx, orderID)
		if err != nil {
			return err
		}
		if o == nil {
			return ErrOrderNotFound
		}
		if err := orderAuthorize(p, o); err != nil {
			return err
		}
		view, err = s.loadView(tx, orderID)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return view, err
}

// Cancel membatalkan order dan melepas reservasinya. Order yang sudah dibayar
// ditandai refund_required. Membatalkan order yang sudah CANCELLED aman
// (mengembalikan keadaan sekarang).
func (s *OrderService) Cancel(ctx context.Context, p *auth.Principal, orderID uuid.UUID, reason string) (OrderView, error) {
	if err := invCheckText("reason", reason); err != nil {
		return OrderView{}, err
	}
	reason, n := invTrimmed(reason)
	if n < 3 || n > 500 {
		return OrderView{}, apperror.Validation(apperror.FieldError{Field: "reason", Code: "length", Message: "wajib diisi 3-500 karakter"})
	}
	actor := LedgerActorFromPrincipal(p)
	var view OrderView
	err := invRunTx(ctx, s.db, func(tx *gorm.DB) error {
		o, err := s.orders.LockOrder(tx, orderID)
		if err != nil {
			return err
		}
		if o == nil {
			return ErrOrderNotFound
		}
		now := s.clock.Now() // setelah lock: riwayat status urut
		if err := orderAuthorize(p, o); err != nil {
			return err
		}
		active, err := s.orders.LockActiveReservations(tx, o.ID)
		if err != nil {
			return err
		}
		if err := orderAuthorizeMutation(p, o, active); err != nil {
			return err
		}
		if o.Status != OrderStatusCancelled {
			switch o.Status {
			case OrderStatusPendingPayment, OrderStatusPaid, OrderStatusNeedsAction:
			default:
				return ErrOrderInvalidTransition
			}
			if err := s.releaseActive(ctx, tx, o.ID, model.ReservationReleased, "order_cancelled", actor, now); err != nil {
				return err
			}
			if err := s.transition(tx, o.ID, o.Status, OrderStatusCancelled, reason, nil, actor, now); err != nil {
				return err
			}
			if o.PaidAt != nil {
				if err := s.requestRefund(tx, o.ID, "cancelled_after_payment", nil, now); err != nil {
					return err
				}
			}
			if err := s.emit(tx, EventOrderCancelled, o.ID, map[string]any{"from_status": o.Status, "reason": reason}, now); err != nil {
				return err
			}
		}
		view, err = s.loadView(tx, o.ID)
		return err
	})
	return view, err
}

// requestRefund menandai order perlu refund (diproses tim finance/payment
// gateway; prototype hanya mencatat flag + event). Satu event
// order.refund_requested.v1 per pembayaran: pembayaran yang sudah ada di
// metadata.refund_payment_ids tidak memicu event lagi. paymentID nil = semua
// pembayaran SUCCEEDED order (misal batal setelah bayar, termasuk bayar
// ganda). Payload selalu memuat payment_id; null hanya kalau order memang
// tidak punya pembayaran (misal impor bernilai 0) dan belum pernah ditandai.
func (s *OrderService) requestRefund(tx *gorm.DB, orderID uuid.UUID, reason string, paymentID *uuid.UUID, now time.Time) error {
	var payIDs []uuid.UUID
	if paymentID != nil {
		payIDs = []uuid.UUID{*paymentID}
	} else {
		var err error
		if payIDs, err = s.orders.SucceededPaymentIDs(tx, orderID); err != nil {
			return err
		}
	}
	raw, err := s.orders.OrderMetadata(tx, orderID)
	if err != nil {
		return err
	}
	var meta struct {
		RefundRequired   bool     `json:"refund_required"`
		RefundPaymentIDs []string `json:"refund_payment_ids"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return fmt.Errorf("metadata order %s: %w", orderID, err)
		}
	}
	var fresh []uuid.UUID
	for _, id := range payIDs {
		if !slices.Contains(meta.RefundPaymentIDs, id.String()) {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) == 0 && (len(payIDs) > 0 || meta.RefundRequired) {
		return nil // sudah diminta sebelumnya: jangan emit ganda
	}
	listed := meta.RefundPaymentIDs
	for _, id := range fresh {
		listed = append(listed, id.String())
	}
	patch := map[string]any{"refund_required": true, "refund_reason": reason}
	if len(fresh) > 0 {
		patch["refund_payment_ids"] = listed
	}
	if err := s.orders.PatchMetadata(tx, orderID, model.MustJSON(patch), now); err != nil {
		return err
	}
	if len(fresh) == 0 {
		return s.emit(tx, EventOrderRefundRequested, orderID, map[string]any{"reason": reason, "payment_id": nil}, now)
	}
	for _, id := range fresh {
		if err := s.emit(tx, EventOrderRefundRequested, orderID, map[string]any{"reason": reason, "payment_id": id}, now); err != nil {
			return err
		}
	}
	return nil
}

// LineSerials adalah serial unit yang dikirim untuk satu line.
type LineSerials struct {
	OrderLineID uuid.UUID
	SerialIDs   []uuid.UUID
}

// Fulfill memenuhi order PAID: COMMIT_SALE untuk setiap reservasi (fisik dan
// reserved berkurang bersama), reservasi COMMITTED, status PAID → FULFILLING →
// SHIPPED, dan serial opsional dicatat di order_line_serials. Order yang sudah
// SHIPPED dikembalikan apa adanya (aman di-retry).
func (s *OrderService) Fulfill(ctx context.Context, p *auth.Principal, orderID uuid.UUID, serials []LineSerials) (OrderView, error) {
	actor := LedgerActorFromPrincipal(p)
	var view OrderView
	err := invRunTx(ctx, s.db, func(tx *gorm.DB) error {
		o, err := s.orders.LockOrder(tx, orderID)
		if err != nil {
			return err
		}
		if o == nil {
			return ErrOrderNotFound
		}
		now := s.clock.Now() // setelah lock: riwayat status urut
		if err := orderAuthorize(p, o); err != nil {
			return err
		}
		if o.Status == OrderStatusShipped {
			view, err = s.loadView(tx, o.ID)
			return err
		}
		if o.Status != OrderStatusPaid {
			return ErrOrderInvalidTransition
		}
		rs, err := s.orders.LockActiveReservations(tx, o.ID)
		if err != nil {
			return err
		}
		lines, err := s.orders.Lines(tx, o.ID)
		if err != nil {
			return err
		}
		// Order split: wajib berwenang di SEMUA lokasi reservasinya.
		if err := orderAuthorizeMutation(p, o, rs); err != nil {
			return err
		}
		byLine := make(map[uuid.UUID]model.StockReservation, len(rs))
		for _, r := range rs {
			byLine[r.OrderLineID] = r
		}
		for _, l := range lines {
			if r, ok := byLine[l.ID]; !ok || r.Qty != l.Qty {
				return ErrOrderNotAllocated
			}
		}
		serialRows, serialIDs, err := s.validateSerials(tx, lines, serials)
		if err != nil {
			return err
		}

		if err := s.transition(tx, o.ID, OrderStatusPaid, OrderStatusFulfilling, "Pick dan assign serial", nil, actor, now); err != nil {
			return err
		}
		for _, r := range rs {
			if _, err := s.inv.ApplyMovement(ctx, tx, MovementInput{
				LocationID: r.LocationID, ProductID: r.ProductID, Type: MovementCommitSale, Qty: r.Qty,
				RefType: "ORDER", RefID: o.ID, IdempotencyKey: "reservation:" + r.ID.String() + ":commit",
				Actor: actor, OccurredAt: now,
			}); err != nil {
				return err
			}
			if _, err := s.orders.CloseReservation(tx, r.ID, model.ReservationCommitted, "fulfilled", now); err != nil {
				return err
			}
		}
		if err := s.orders.InsertLineSerials(tx, serialRows); err != nil {
			if name, ok := repository.ConstraintViolation(err); ok && name == "serial_sold_once" {
				return ErrOrderSerialSold
			}
			return err
		}
		if s.serialHook != nil && len(serialIDs) > 0 {
			loc := uuid.Nil
			if o.FulfillmentLocationID != nil {
				loc = *o.FulfillmentLocationID
			}
			if err := s.serialHook.OnSerialsSold(ctx, tx, o.ID, loc, serialIDs, actor); err != nil {
				return err
			}
		}
		if err := s.transition(tx, o.ID, OrderStatusFulfilling, OrderStatusShipped, "Stok di-commit, order dikirim", nil, actor, now); err != nil {
			return err
		}
		if err := s.emit(tx, EventOrderShipped, o.ID, map[string]any{"serial_unit_ids": serialIDs}, now); err != nil {
			return err
		}
		view, err = s.loadView(tx, o.ID)
		return err
	})
	return view, err
}

// validateSerials memeriksa serial opsional saat fulfill: line milik order,
// tanpa duplikat, jumlah ≤ qty line, serial level UNIT dengan produk yang sama.
func (s *OrderService) validateSerials(tx *gorm.DB, lines []repository.LineRow, in []LineSerials) ([]model.OrderLineSerial, []uuid.UUID, error) {
	if len(in) == 0 {
		return nil, nil, nil
	}
	lineByID := make(map[uuid.UUID]repository.LineRow, len(lines))
	for _, l := range lines {
		lineByID[l.ID] = l
	}
	seen := map[uuid.UUID]bool{}
	var rows []model.OrderLineSerial
	var ids []uuid.UUID
	wantProduct := map[uuid.UUID]uuid.UUID{}
	for _, ls := range in {
		l, ok := lineByID[ls.OrderLineID]
		if !ok || int64(len(ls.SerialIDs)) > l.Qty {
			return nil, nil, ErrOrderSerialInvalid
		}
		for _, id := range ls.SerialIDs {
			if seen[id] || id == uuid.Nil {
				return nil, nil, ErrOrderSerialInvalid
			}
			seen[id] = true
			ids = append(ids, id)
			wantProduct[id] = l.ProductID
			rows = append(rows, model.OrderLineSerial{OrderLineID: l.ID, SerialUnitID: id, AssignedAt: s.clock.Now()})
		}
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	got, err := s.orders.SerialProducts(tx, ids)
	if err != nil {
		return nil, nil, err
	}
	for id, prod := range wantProduct {
		if got[id] != prod {
			return nil, nil, ErrOrderSerialInvalid
		}
	}
	return rows, ids, nil
}

// ------------------------------------------------------------------ worker

// ExpireDueOrders melepas reservasi order PENDING_PAYMENT yang lewat batas
// bayar (status EXPIRED), paling banyak limit order. SATU order per transaksi:
// order diklaim dengan FOR NO KEY UPDATE SKIP LOCKED (sweeper paralel
// mendapat order berbeda), lalu reservasinya dikunci urut (lokasi, produk) dan
// saldonya diubah dengan urutan yang sama, persis seperti jalur batal/fulfill.
// Memproses banyak order dalam satu transaksi memegang saldo order pertama
// sambil mengunci reservasi order berikutnya, sehingga bertabrakan dengan
// pembuatan order (deadlock). Mengembalikan jumlah order yang diproses.
func (s *OrderService) ExpireDueOrders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	n := 0
	for n < limit {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		processed := false
		err := invRunTx(ctx, s.db, func(tx *gorm.DB) error {
			processed = false
			due, err := s.orders.LockDueOrders(tx, s.clock.Now(), 1)
			if err != nil || len(due) == 0 {
				return err
			}
			o := due[0]
			now := s.clock.Now() // setelah lock: riwayat status urut
			if err := s.releaseActive(ctx, tx, o.ID, model.ReservationExpired, "payment_expired", SystemLedgerActor, now); err != nil {
				return err
			}
			if err := s.transition(tx, o.ID, OrderStatusPendingPayment, OrderStatusExpired,
				"Batas waktu pembayaran habis, stok dilepas", nil, SystemLedgerActor, now); err != nil {
				return err
			}
			if err := s.emit(tx, EventOrderExpired, o.ID, map[string]any{"reason": "payment_expired"}, now); err != nil {
				return err
			}
			processed = true
			return nil
		})
		if err != nil {
			return n, err
		}
		if !processed {
			break
		}
		n++
	}
	return n, nil
}

// HandleReservationPreempted adalah handler outbox reservation.preempted.v1
// (dipanggil dispatcher di dalam tx event). Mencoba memesan ulang line yang
// di-preempt di lokasi lain:
//   - berhasil → order tetap (PAID/PENDING_PAYMENT), konflik AUTO_RESOLVED;
//   - gagal, order PAID → NEEDS_ACTION (tawarkan tunggu restock + voucher atau
//     refund), reservasi lain dilepas, konflik tetap OPEN untuk ops;
//   - gagal, order PENDING_PAYMENT → EXPIRED (belum ditagih; pembayaran yang
//     tetap masuk diproses sebagai bayar telat), konflik tetap OPEN.
//
// Idempotent: order yang statusnya sudah berubah atau line yang sudah punya
// reservasi ACTIVE dilewati.
func (s *OrderService) HandleReservationPreempted(ctx context.Context, tx *gorm.DB, payload []byte) error {
	var ev PreemptedEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("payload %s tidak valid: %w", EventReservationPreempted, err)
	}
	o, err := s.orders.LockOrder(tx, ev.OrderID)
	if err != nil {
		return err
	}
	if o == nil || (o.Status != OrderStatusPaid && o.Status != OrderStatusPendingPayment) {
		return nil
	}
	now := s.clock.Now() // setelah lock: riwayat status urut
	active, err := s.orders.LockActiveReservations(tx, o.ID)
	if err != nil {
		return err
	}
	for _, r := range active {
		if r.OrderLineID == ev.OrderLineID {
			return nil // sudah direalokasi
		}
	}
	lines, err := s.orders.Lines(tx, o.ID)
	if err != nil {
		return err
	}
	var line *repository.LineRow
	lineIdx := 0
	for i := range lines {
		if lines[i].ID == ev.OrderLineID {
			line, lineIdx = &lines[i], i
		}
	}
	if line == nil {
		return fmt.Errorf("line %s tidak ada di order %s", ev.OrderLineID, o.ID)
	}
	var expires *time.Time
	if o.Status == OrderStatusPendingPayment {
		expires = o.PaymentExpiresAt
	}
	loc, err := s.allocate(ctx, tx, o.ID, []invAllocLine{{Index: lineIdx, LineID: line.ID, ProductID: line.ProductID, Qty: line.Qty}},
		expires, SystemLedgerActor, now)
	switch {
	case err == nil:
		if err := s.invRepo.ResolveReservationConflicts(tx, ev.ReservationID, "reallocated_to:"+loc.String(), now); err != nil {
			return err
		}
		if o.FulfillmentLocationID == nil || *o.FulfillmentLocationID != loc {
			if err := s.orders.PatchMetadata(tx, o.ID, model.MustJSON(map[string]any{"split_fulfillment": true}), now); err != nil {
				return err
			}
		}
		return s.emit(tx, EventOrderReallocated, o.ID, map[string]any{
			"order_line_id": line.ID, "from_location_id": ev.LocationID, "to_location_id": loc,
		}, now)
	case errors.Is(err, ErrOutOfStock):
	default:
		return err
	}

	if err := s.releaseActive(ctx, tx, o.ID, model.ReservationReleased, "reallocation_failed", SystemLedgerActor, now); err != nil {
		return err
	}
	if o.Status == OrderStatusPaid {
		if err := s.transition(tx, o.ID, OrderStatusPaid, OrderStatusNeedsAction,
			"Stok di-preempt penjualan toko dan realokasi gagal", nil, SystemLedgerActor, now); err != nil {
			return err
		}
		if err := s.orders.PatchMetadata(tx, o.ID, model.MustJSON(map[string]any{
			"needs_action_reason": "preempted_reallocation_failed",
			"resolution_options":  []string{"wait_restock_with_voucher", "full_refund_with_voucher"},
		}), now); err != nil {
			return err
		}
		return s.emit(tx, EventOrderNeedsAction, o.ID, map[string]any{
			"reason": "preempted_reallocation_failed", "conflict_id": ev.ConflictID,
		}, now)
	}
	// Belum dibayar: pakai transisi yang sudah ada PENDING_PAYMENT → EXPIRED,
	// sehingga pembayaran yang tetap datang diproses sebagai bayar telat (coba
	// pesan ulang stok, atau NEEDS_ACTION + refund). Konflik tetap OPEN
	// (diselesaikan Fase 4/ops).
	if err := s.transition(tx, o.ID, OrderStatusPendingPayment, OrderStatusExpired,
		"Stok di-preempt penjualan toko sebelum dibayar; tidak ada lokasi lain", nil, SystemLedgerActor, now); err != nil {
		return err
	}
	if err := s.orders.PatchMetadata(tx, o.ID, model.MustJSON(map[string]any{
		"expired_reason": "preempted_reallocation_failed",
	}), now); err != nil {
		return err
	}
	return s.emit(tx, EventOrderExpired, o.ID, map[string]any{
		"reason": "preempted_reallocation_failed", "conflict_id": ev.ConflictID,
	}, now)
}

// reserveAllLines memesan ulang seluruh line order (bayar telat). Mengembalikan
// lokasi fulfillment baru atau ErrOutOfStock.
func (s *OrderService) reserveAllLines(ctx context.Context, tx *gorm.DB, orderID uuid.UUID, actor LedgerActor, now time.Time) (uuid.UUID, error) {
	lines, err := s.orders.Lines(tx, orderID)
	if err != nil {
		return uuid.Nil, err
	}
	alloc := make([]invAllocLine, len(lines))
	for i, l := range lines {
		alloc[i] = invAllocLine{Index: i, LineID: l.ID, ProductID: l.ProductID, Qty: l.Qty}
	}
	loc, err := s.allocate(ctx, tx, orderID, alloc, nil, actor, now)
	if err != nil {
		return uuid.Nil, err
	}
	return loc, s.orders.SetFulfillmentLocation(tx, orderID, loc, now)
}
