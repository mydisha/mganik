package controller

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/dto"
	"mganik/api/internal/service"
)

// OrderController: buat, baca, batal, fulfill order.
type OrderController struct {
	svc *service.OrderService
	log *slog.Logger
}

func NewOrderController(svc *service.OrderService, log *slog.Logger) *OrderController {
	return &OrderController{svc: svc, log: log}
}

// Create: POST /orders (Idempotency-Key wajib).
func (h *OrderController) Create(c *gin.Context) {
	key := c.GetHeader(IdempotencyKeyHeader)
	if !service.ValidIdempotencyKey(key) {
		apperror.Write(c, h.log, service.ErrIdempotencyKeyRequired)
		return
	}
	var req dto.CreateOrderRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	in := service.CreateOrderInput{ChannelCode: req.ChannelCode, Note: req.Note}
	for _, l := range req.Lines {
		in.Lines = append(in.Lines, service.CreateOrderLine{ProductID: uuid.MustParse(l.ProductID), Qty: l.Qty})
	}
	resp, err := h.svc.CreateIdempotent(c.Request.Context(), auth.FromContext(c.Request.Context()), key, in,
		func(v service.OrderView) any { return OrderResponse(v) })
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	if resp.Replayed {
		c.Header(IdempotentReplayHeader, "true")
	}
	c.Data(resp.Status, "application/json; charset=utf-8", resp.Body)
}

// Get: GET /orders/{id}
func (h *OrderController) Get(c *gin.Context) {
	id, ok := h.orderID(c)
	if !ok {
		return
	}
	v, err := h.svc.Get(c.Request.Context(), auth.FromContext(c.Request.Context()), id)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, OrderResponse(v))
}

// Cancel: POST /orders/{id}/cancel
func (h *OrderController) Cancel(c *gin.Context) {
	id, ok := h.orderID(c)
	if !ok {
		return
	}
	var req dto.CancelOrderRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	v, err := h.svc.Cancel(c.Request.Context(), auth.FromContext(c.Request.Context()), id, req.Reason)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, OrderResponse(v))
}

// Fulfill: POST /orders/{id}/fulfill (body opsional berisi serial per line).
func (h *OrderController) Fulfill(c *gin.Context) {
	id, ok := h.orderID(c)
	if !ok {
		return
	}
	var req dto.FulfillOrderRequest
	if c.Request.ContentLength != 0 {
		if err := BindJSON(c, &req); err != nil {
			apperror.Write(c, h.log, err)
			return
		}
	}
	serials := make([]service.LineSerials, 0, len(req.Serials))
	for _, s := range req.Serials {
		ls := service.LineSerials{OrderLineID: uuid.MustParse(s.OrderLineID)}
		for _, sid := range s.SerialUnitIDs {
			ls.SerialIDs = append(ls.SerialIDs, uuid.MustParse(sid))
		}
		serials = append(serials, ls)
	}
	v, err := h.svc.Fulfill(c.Request.Context(), auth.FromContext(c.Request.Context()), id, serials)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, OrderResponse(v))
}

func (h *OrderController) orderID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		apperror.Write(c, h.log, service.ErrOrderNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// OrderResponse memetakan OrderView ke DTO API.
func OrderResponse(v service.OrderView) dto.OrderResponse {
	o := v.Order
	meta := json.RawMessage(o.Metadata)
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	r := dto.OrderResponse{
		ID: o.ID, ChannelCode: o.ChannelCode, ExternalRef: o.ExternalRef, Status: o.Status, Currency: o.Currency,
		Subtotal: o.Subtotal, Discount: o.Discount, Total: o.Total, PlacedAt: o.PlacedAt, PaidAt: o.PaidAt,
		PaymentExpiresAt: o.PaymentExpiresAt, FulfillmentLocationID: o.FulfillmentLocationID, Metadata: meta,
		Lines:        make([]dto.OrderLineDTO, 0, len(v.Lines)),
		Reservations: make([]dto.ReservationDTO, 0, len(v.Reservations)),
		History:      make([]dto.OrderHistoryDTO, 0, len(v.History)),
		Payments:     make([]dto.OrderPaymentDTO, 0, len(v.Payments)),
	}
	serials := map[uuid.UUID][]uuid.UUID{}
	for _, s := range v.Serials {
		serials[s.OrderLineID] = append(serials[s.OrderLineID], s.SerialUnitID)
	}
	for _, l := range v.Lines {
		ids := serials[l.ID]
		if ids == nil {
			ids = []uuid.UUID{}
		}
		r.Lines = append(r.Lines, dto.OrderLineDTO{
			ID: l.ID, ProductID: l.ProductID, SKU: l.SKU, ProductName: l.ProductName, Qty: l.Qty,
			UnitPrice: l.UnitPrice, Discount: l.Discount, SerialUnitIDs: ids,
		})
	}
	for _, rs := range v.Reservations {
		r.Reservations = append(r.Reservations, dto.ReservationDTO{
			ID: rs.ID, OrderLineID: rs.OrderLineID, LocationID: rs.LocationID, ProductID: rs.ProductID, Qty: rs.Qty,
			Status: rs.Status, ExpiresAt: rs.ExpiresAt, CreatedAt: rs.CreatedAt, ClosedAt: rs.ClosedAt, CloseReason: rs.CloseReason,
		})
	}
	for _, hst := range v.History {
		r.History = append(r.History, dto.OrderHistoryDTO{
			FromStatus: hst.FromStatus, ToStatus: hst.ToStatus, Reason: hst.Reason, ActorType: hst.ActorType, At: hst.At,
		})
	}
	for _, p := range v.Payments {
		r.Payments = append(r.Payments, dto.OrderPaymentDTO{
			ID: p.ID, Provider: p.Provider, ProviderRef: p.ProviderRef, Amount: p.Amount, Currency: p.Currency,
			Status: p.Status, ReceivedAt: p.ReceivedAt,
		})
	}
	return r
}
