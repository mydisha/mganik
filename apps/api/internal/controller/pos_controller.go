package controller

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/dto"
	"mganik/api/internal/pagination"
	"mganik/api/internal/repository"
	"mganik/api/internal/service"
)

// PosController: provisioning device (staff), bootstrap/heartbeat/sync
// (device), dan resolusi konflik alokasi (staff).
type PosController struct {
	svc *service.PosService
	log *slog.Logger
}

func NewPosController(svc *service.PosService, log *slog.Logger) *PosController {
	return &PosController{svc: svc, log: log}
}

// noStore: respons berisi rahasia (token, signing key) atau hash PIN kasir
// tidak boleh disimpan cache mana pun.
func posNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
}

func posDeviceDTO(d repository.PosDeviceRow) dto.PosDevice {
	return dto.PosDevice{
		ID: d.ID, LocationID: d.LocationID, LocationCode: d.LocationCode, LocationName: d.LocationName, Name: d.Name,
		Status: d.Status, LastSeenAt: posUTCPtr(d.LastSeenAt), LastAckedSeq: d.LastAckedSeq, ProvisionedBy: d.ProvisionedBy,
		RevokedAt: posUTCPtr(d.RevokedAt), CreatedAt: d.CreatedAt.UTC(),
	}
}

func posUTCPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// hasBody: body opsional (heartbeat, revoke) boleh kosong.
func posHasBody(c *gin.Context) bool {
	return c.Request.Body != nil && c.Request.Body != http.NoBody && c.Request.ContentLength != 0
}

func posPathUUID(c *gin.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, apperror.Validation(apperror.FieldError{Field: name, Code: "uuid", Message: "harus UUID yang valid"})
	}
	return id, nil
}

// ProvisionDevice: POST /pos/devices.
func (h *PosController) ProvisionDevice(c *gin.Context) {
	var req dto.PosDeviceCreateRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out, err := h.svc.ProvisionDevice(c.Request.Context(), auth.FromContext(c.Request.Context()), service.PosProvisionInput{
		LocationID: req.LocationID, Name: req.Name, RequestID: c.GetString(apperror.RequestIDKey),
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	posNoStore(c)
	c.JSON(http.StatusCreated, dto.PosDeviceProvisioned{Device: posDeviceDTO(out.Device), Token: out.Token, SigningKey: out.SigningKeyHex})
}

// ListDevices: GET /pos/devices?location_id=&limit=&cursor=
func (h *PosController) ListDevices(c *gin.Context) {
	q := service.PosDeviceQuery{Limit: c.Query("limit"), Cursor: c.Query("cursor")}
	if v := c.Query("location_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			apperror.Write(c, h.log, apperror.Validation(apperror.FieldError{Field: "location_id", Code: "uuid", Message: "harus UUID yang valid"}))
			return
		}
		q.LocationID = &id
	}
	page, err := h.svc.ListDevices(c.Request.Context(), auth.FromContext(c.Request.Context()), q)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	resp := dto.PosDevicePage{Items: make([]dto.PosDevice, len(page.Items)), NextCursor: page.NextCursor}
	for i, d := range page.Items {
		resp.Items[i] = posDeviceDTO(d)
	}
	c.JSON(http.StatusOK, resp)
}

// RevokeDevice: POST /pos/devices/{id}/revoke.
func (h *PosController) RevokeDevice(c *gin.Context) {
	id, err := posPathUUID(c, "id")
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	var req dto.PosDeviceRevokeRequest
	if posHasBody(c) {
		if err := BindJSON(c, &req); err != nil {
			apperror.Write(c, h.log, err)
			return
		}
	}
	d, err := h.svc.RevokeDevice(c.Request.Context(), auth.FromContext(c.Request.Context()), id, req.Reason,
		c.GetString(apperror.RequestIDKey))
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, posDeviceDTO(d))
}

func posSerialPage(p pagination.Page[repository.PosSerialRow]) dto.PosSerialPageBody {
	out := dto.PosSerialPageBody{Items: make([]dto.PosSerial, len(p.Items)), NextCursor: p.NextCursor}
	for i, s := range p.Items {
		out.Items[i] = dto.PosSerial{Code: s.Code, ProductID: s.ProductID}
	}
	return out
}

// Bootstrap: GET /pos/bootstrap (device).
func (h *PosController) Bootstrap(c *gin.Context) {
	b, err := h.svc.Bootstrap(c.Request.Context(), auth.FromContext(c.Request.Context()))
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	cfg := h.svc.Config()
	resp := dto.PosBootstrapResponse{
		ServerTime: b.Now.UTC(),
		Device:     posDeviceDTO(b.Device),
		Location: dto.PosLocation{ID: b.Location.ID, Code: b.Location.Code, Name: b.Location.Name,
			Timezone: b.Location.Timezone, SyncState: b.Location.SyncState},
		Limits: dto.PosLimits{
			OfflineLimitSeconds:       int64(cfg.OfflineLimit / time.Second),
			HeartbeatIntervalSeconds:  int64(cfg.HeartbeatInterval / time.Second),
			HeartbeatTimeoutSeconds:   int64(cfg.HeartbeatTimeout / time.Second),
			ClockSkewToleranceSeconds: int64(cfg.ClockSkewTolerance / time.Second),
			SyncMaxBatch:              cfg.SyncMaxBatch,
			SyncMaxBodyBytes:          cfg.SyncMaxBodyBytes,
			MaxSerialsPerTxn:          cfg.MaxSerialsPerTxn,
			HighDiscountPercent:       cfg.HighDiscountPercent,
			OfflineValueLimit:         cfg.OfflineValueLimit,
		},
		Products: make([]dto.PosProduct, len(b.Products)),
		Stock:    make([]dto.PosStock, len(b.Stock)),
		Cashiers: make([]dto.PosCashier, len(b.Cashiers)),
		Serials:  posSerialPage(b.Serials),
	}
	for i, p := range b.Products {
		resp.Products[i] = dto.PosProduct{ID: p.ID, SKU: p.SKU, GTIN: p.GTIN, Name: p.Name, UOM: p.UOM}
	}
	for i, s := range b.Stock {
		resp.Stock[i] = dto.PosStock{ProductID: s.ProductID, PhysicalQty: s.PhysicalQty, ReservedQty: s.ReservedQty,
			QuarantineQty: s.QuarantineQty, AvailableQty: s.AvailableQty}
	}
	for i, u := range b.Cashiers {
		resp.Cashiers[i] = dto.PosCashier{ID: u.ID, Name: u.Name, PosPinHash: u.PosPinHash}
	}
	if pl := b.PriceList; pl != nil {
		resp.PriceList = &dto.PosPriceList{ID: pl.ID, Code: pl.Code, Version: pl.Version, LocationID: pl.LocationID,
			Currency: pl.Currency, ValidFrom: pl.ValidFrom.UTC(), ValidTo: posUTCPtr(pl.ValidTo), Items: make([]dto.PosPriceRow, len(pl.Items))}
		for i, it := range pl.Items {
			resp.PriceList.Items[i] = dto.PosPriceRow{ProductID: it.ProductID, Price: it.Price}
		}
	}
	posNoStore(c)
	c.JSON(http.StatusOK, resp)
}

// BootstrapSerials: GET /pos/bootstrap/serials?cursor= (device).
func (h *PosController) BootstrapSerials(c *gin.Context) {
	page, err := h.svc.BootstrapSerials(c.Request.Context(), auth.FromContext(c.Request.Context()), c.Query("cursor"))
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	posNoStore(c)
	c.JSON(http.StatusOK, posSerialPage(page))
}

// Heartbeat: POST /pos/heartbeat (device, body opsional).
func (h *PosController) Heartbeat(c *gin.Context) {
	var req dto.PosHeartbeatRequest
	if posHasBody(c) {
		if err := BindJSON(c, &req); err != nil {
			apperror.Write(c, h.log, err)
			return
		}
	}
	if req.PendingCount != nil && *req.PendingCount < 0 {
		apperror.Write(c, h.log, apperror.Validation(apperror.FieldError{Field: "pending_count", Code: "min", Message: "minimal 0"}))
		return
	}
	out, err := h.svc.Heartbeat(c.Request.Context(), auth.FromContext(c.Request.Context()), req.ClientTime, req.PendingCount)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	cfg := h.svc.Config()
	resp := dto.PosHeartbeatResponse{
		ServerTime: out.Now.UTC(), LocationSyncState: out.SyncState, LastAckedSeq: out.LastAckedSeq,
		HeartbeatIntervalSeconds: int64(cfg.HeartbeatInterval / time.Second),
		HeartbeatTimeoutSeconds:  int64(cfg.HeartbeatTimeout / time.Second), ClockSkewMs: out.ClockSkewMs,
	}
	if out.OnlineBlockedReason != "" {
		resp.OnlineBlockedReason = &out.OnlineBlockedReason
	}
	c.JSON(http.StatusOK, resp)
}

// Sync: POST /pos/sync (device).
func (h *PosController) Sync(c *gin.Context) {
	var req dto.PosSyncRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out, err := h.svc.Sync(c.Request.Context(), auth.FromContext(c.Request.Context()), service.PosSyncInput{
		ClientTime: req.ClientTime, Transactions: req.Transactions,
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	resp := dto.PosSyncResponse{
		ServerTime: out.ServerTime.UTC(), AckUpToSeq: out.AckUpToSeq, MissingSeqs: out.MissingSeqs,
		MissingSeqsTruncated: out.MissingSeqsTruncated, Results: make([]dto.PosSyncResult, len(out.Results)),
	}
	for i, r := range out.Results {
		resp.Results[i] = dto.PosSyncResult{Index: r.Index, ClientTxnID: r.ClientTxnID, DeviceSeq: r.DeviceSeq,
			Status: r.Status, Result: r.Result, Detail: r.Detail}
	}
	c.JSON(http.StatusOK, resp)
}

// ResolveConflict: POST /cc/conflicts/{id}/resolve (staff, conflict.resolve).
func (h *PosController) ResolveConflict(c *gin.Context) {
	id, err := posPathUUID(c, "id")
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	var req dto.ConflictResolveRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	row, err := h.svc.ResolveConflict(c.Request.Context(), auth.FromContext(c.Request.Context()), service.ConflictResolveInput{
		ID: id, Resolution: req.Resolution, Note: req.Note, RequestID: c.GetString(apperror.RequestIDKey),
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.CCConflictItem{
		ID: row.ID, Kind: row.Kind, Status: row.Status, LocationID: row.LocationID, LocationCode: row.LocationCode,
		LocationName: row.LocationName, ProductID: row.ProductID, SKU: row.SKU, ProductName: row.ProductName, Qty: row.Qty,
		PosTransactionID: row.PosTransactionID, ReservationID: row.ReservationID, OrderID: row.OrderID,
		SerialUnitID: row.SerialUnitID, Resolution: row.Resolution, Details: json.RawMessage(row.Details),
		ResolvedBy: row.ResolvedBy, ResolvedAt: posUTCPtr(row.ResolvedAt), CreatedAt: row.CreatedAt.UTC(),
	})
}
