package controller

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/dto"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
)

// IdempotencyKeyHeader adalah header idempotensi untuk POST pembuat data.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotentReplayHeader bernilai "true" kalau respons adalah replay.
const IdempotentReplayHeader = "Idempotent-Replayed"

// InventoryController: saldo stok, adjustment, karantina.
type InventoryController struct {
	svc *service.InventoryService
	log *slog.Logger
}

func NewInventoryController(svc *service.InventoryService, log *slog.Logger) *InventoryController {
	return &InventoryController{svc: svc, log: log}
}

// ListBalances: GET /inventory/balances?location_id=&product_id=&limit=&cursor=
func (h *InventoryController) ListBalances(c *gin.Context) {
	var q service.BalanceQuery
	var fields []apperror.FieldError
	if v := c.Query("location_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fields = append(fields, apperror.FieldError{Field: "location_id", Code: "uuid", Message: "harus UUID yang valid"})
		}
		q.LocationID = &id
	}
	if v := c.Query("product_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			fields = append(fields, apperror.FieldError{Field: "product_id", Code: "uuid", Message: "harus UUID yang valid"})
		}
		q.ProductID = &id
	}
	if v := c.Query("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > service.MaxBalancePageSize {
			fields = append(fields, apperror.FieldError{Field: "limit", Code: "range", Message: "harus antara 1 dan " + strconv.Itoa(service.MaxBalancePageSize)})
		}
		q.Limit = n
	}
	if v := c.Query("cursor"); len(v) > 512 {
		fields = append(fields, apperror.FieldError{Field: "cursor", Code: "max", Message: "posisi halaman tidak valid; muat ulang dari halaman pertama"})
	} else {
		q.Cursor = v
	}
	if len(fields) > 0 {
		apperror.Write(c, h.log, apperror.Validation(fields...))
		return
	}
	page, err := h.svc.ListBalances(c.Request.Context(), auth.FromContext(c.Request.Context()), q)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	resp := dto.BalanceListResponse{Items: make([]dto.BalanceItem, 0, len(page.Items))}
	for _, b := range page.Items {
		resp.Items = append(resp.Items, dto.BalanceItem{
			LocationID: b.LocationID, LocationCode: b.LocationCode, LocationName: b.LocationName,
			LocationType: b.LocationType, SyncState: b.SyncState, ProductID: b.ProductID, SKU: b.SKU,
			ProductName: b.ProductName, PhysicalQty: b.PhysicalQty, ReservedQty: b.ReservedQty,
			QuarantineQty: b.QuarantineQty, AvailableQty: b.AvailableQty, OnlineBufferQty: b.OnlineBufferQty,
			OnlineATP: b.OnlineATP, UpdatedAt: b.UpdatedAt,
		})
	}
	if page.NextCursor != "" {
		resp.NextCursor = &page.NextCursor
	}
	c.JSON(http.StatusOK, resp)
}

// Adjust: POST /inventory/adjustments (Idempotency-Key wajib, alasan wajib, diaudit).
func (h *InventoryController) Adjust(c *gin.Context) {
	key := c.GetHeader(IdempotencyKeyHeader)
	if !service.ValidIdempotencyKey(key) {
		apperror.Write(c, h.log, service.ErrIdempotencyKeyRequired)
		return
	}
	var req dto.AdjustmentRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.Adjust(c.Request.Context(), auth.FromContext(c.Request.Context()), service.StockChangeInput{
		LocationID: uuid.MustParse(req.LocationID), ProductID: uuid.MustParse(req.ProductID), Qty: req.QtyDelta,
		Reason: req.Reason, IdempotencyKey: key, RequestID: c.GetString(apperror.RequestIDKey),
	})
	h.writeStockChange(c, res, err)
}

// Quarantine: POST /inventory/quarantine (direction IN/OUT).
func (h *InventoryController) Quarantine(c *gin.Context) {
	key := c.GetHeader(IdempotencyKeyHeader)
	if !service.ValidIdempotencyKey(key) {
		apperror.Write(c, h.log, service.ErrIdempotencyKeyRequired)
		return
	}
	var req dto.QuarantineRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.Quarantine(c.Request.Context(), auth.FromContext(c.Request.Context()), service.StockChangeInput{
		LocationID: uuid.MustParse(req.LocationID), ProductID: uuid.MustParse(req.ProductID), Qty: req.Qty,
		Reason: req.Reason, IdempotencyKey: key, RequestID: c.GetString(apperror.RequestIDKey),
	}, req.Direction == "IN")
	h.writeStockChange(c, res, err)
}

func (h *InventoryController) writeStockChange(c *gin.Context, res service.StockChangeResult, err error) {
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
		c.Header(IdempotentReplayHeader, "true")
	}
	c.JSON(status, dto.StockChangeResponse{
		Movement: invMovementDTO(res.Movement), Balance: invStockBalanceDTO(res.Balance), Duplicate: res.Duplicate,
	})
}

func invMovementDTO(m model.InventoryMovement) dto.MovementDTO {
	return dto.MovementDTO{
		ID: m.ID, LocationID: m.LocationID, ProductID: m.ProductID, MovementType: m.MovementType,
		PhysicalDelta: m.PhysicalDelta, ReservedDelta: m.ReservedDelta, QuarantineDelta: m.QuarantineDelta,
		RefType: m.RefType, RefID: m.RefID, Reason: m.Reason, ActorType: m.ActorType, ActorID: m.ActorID,
		OccurredAt: m.OccurredAt, RecordedAt: m.RecordedAt,
	}
}

func invStockBalanceDTO(b model.InventoryBalance) dto.StockBalanceDTO {
	return dto.StockBalanceDTO{
		LocationID: b.LocationID, ProductID: b.ProductID, PhysicalQty: b.PhysicalQty, ReservedQty: b.ReservedQty,
		QuarantineQty: b.QuarantineQty, AvailableQty: b.AvailableQty, UpdatedAt: b.UpdatedAt,
	}
}
