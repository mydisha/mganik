package controller

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/domain"
	"mganik/api/internal/dto"
	"mganik/api/internal/repository"
	"mganik/api/internal/service"
)

// SerialController menangani endpoint staff produksi, agregasi, shipment,
// trace, dan recall. Permission dicek middleware per route; scope lokasi
// (BOLA) dicek di service.
type SerialController struct {
	svc *service.SerialService
	log *slog.Logger
}

func NewSerialController(svc *service.SerialService, log *slog.Logger) *SerialController {
	return &SerialController{svc: svc, log: log}
}

// normalizeSerialCodes menormalkan kode (Crockford) lalu menolak format salah
// (422 validation_failed) dan duplikat setelah normalisasi (422
// duplicate_code), sebelum menyentuh DB.
func normalizeSerialCodes(field string, raw []string) ([]string, error) {
	out := make([]string, len(raw))
	var fields []apperror.FieldError
	for i, r := range raw {
		code, ok := domain.NormalizeSerialCode(r)
		if !ok {
			fields = append(fields, apperror.FieldError{Field: fmt.Sprintf("%s[%d]", field, i), Code: "invalid_format",
				Message: "format kode serial tidak valid (16 karakter Crockford Base32)"})
			continue
		}
		out[i] = code
	}
	if len(fields) > 0 {
		return nil, apperror.Validation(fields...)
	}
	if err := service.RejectDuplicateCodes(field, out); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeOneSerialCode(field, raw string) (string, error) {
	code, ok := domain.NormalizeSerialCode(raw)
	if !ok {
		return "", apperror.Validation(apperror.FieldError{Field: field, Code: "invalid_format",
			Message: "format kode serial tidak valid (16 karakter Crockford Base32)"})
	}
	return code, nil
}

// pathUUID membaca parameter path UUID; format salah = 404 (tanpa DB).
func serialPathUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	return id, err == nil
}

func serialUUIDString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// mustUUID mengurai UUID yang sudah divalidasi tag binding uuid.
func serialMustUUID(s string) uuid.UUID { return uuid.MustParse(s) }

func serialOptUUID(s *string) *uuid.UUID {
	if s == nil {
		return nil
	}
	id := uuid.MustParse(*s)
	return &id
}

// Mint: POST /api/v1/code-batches.
func (h *SerialController) Mint(c *gin.Context) {
	var req dto.MintCodesRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.Mint(c.Request.Context(), auth.FromContext(c.Request.Context()), service.MintInput{
		LevelCode: req.LevelCode, Qty: req.Qty, ProductID: serialOptUUID(req.ProductID),
		PrinterVendor: req.PrinterVendor, Notes: req.Notes,
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	codes := make([]dto.MintedCode, len(res.Codes))
	for i, m := range res.Codes {
		codes[i] = dto.MintedCode{Code: m.Code, PIN: m.PIN}
	}
	cb := res.CodeBatch
	// Respons memuat PIN plaintext: jangan pernah disimpan di cache perantara.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, dto.MintCodesResponse{
		CodeBatch: dto.CodeBatchResponse{
			ID: cb.ID.String(), LevelCode: cb.LevelCode, ProductID: serialUUIDString(cb.ProductID), Qty: cb.Qty,
			PrinterVendor: cb.PrinterVendor, Notes: cb.Notes, IssuedAt: cb.IssuedAt,
		},
		Codes: codes,
	})
}

// Commission: POST /api/v1/serials/commission.
func (h *SerialController) Commission(c *gin.Context) {
	var req dto.CommissionRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	codes, err := normalizeSerialCodes("codes", req.Codes)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.Commission(c.Request.Context(), auth.FromContext(c.Request.Context()), serialMustUUID(req.BatchID), codes)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.CommissionResponse{
		BatchID: res.BatchID.String(), ProductID: res.ProductID.String(), Commissioned: res.Count,
	})
}

// Receive: POST /api/v1/serials/receive.
func (h *SerialController) Receive(c *gin.Context) {
	var req dto.ReceiveSerialsRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	codes, err := normalizeSerialCodes("codes", req.Codes)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.ReceiveProduction(c.Request.Context(), auth.FromContext(c.Request.Context()), serialMustUUID(req.LocationID), codes)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.ReceiveSerialsResponse{LocationID: res.LocationID.String(), Roots: res.Roots, Serials: res.Serials})
}

// Aggregate: POST /api/v1/serials/aggregate.
func (h *SerialController) Aggregate(c *gin.Context) {
	var req dto.AggregateRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	parent, err := normalizeOneSerialCode("parent_code", req.ParentCode)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	children, err := normalizeSerialCodes("child_codes", req.ChildCodes)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.Aggregate(c.Request.Context(), auth.FromContext(c.Request.Context()), parent, children)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.AggregateResponse{
		ParentCode: res.ParentCode, Added: res.Added, AlreadyInParent: res.AlreadyInParent,
		ChildCount: res.ChildCount, Capacity: res.Capacity,
	})
}

// Unpack: POST /api/v1/serials/{code}/unpack.
func (h *SerialController) Unpack(c *gin.Context) {
	code, ok := domain.NormalizeSerialCode(c.Param("code"))
	if !ok {
		apperror.Write(c, h.log, service.ErrSerialNotFound)
		return
	}
	res, err := h.svc.Unpack(c.Request.Context(), auth.FromContext(c.Request.Context()), code)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.UnpackResponse{Code: res.Code, Unpacked: res.Unpacked})
}

// CreateShipment: POST /api/v1/shipments.
func (h *SerialController) CreateShipment(c *gin.Context) {
	var req dto.CreateShipmentRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	codes, err := normalizeSerialCodes("codes", req.Codes)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.CreateShipment(c.Request.Context(), auth.FromContext(c.Request.Context()), service.CreateShipmentInput{
		FromLocationID: serialMustUUID(req.FromLocationID), ToLocationID: serialMustUUID(req.ToLocationID),
		ToPartyID: serialOptUUID(req.ToPartyID), ProgramCode: req.ProgramCode, Codes: codes,
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusCreated, serialShipmentResponse(res))
}

// ReceiveShipment: POST /api/v1/shipments/{id}/receive.
func (h *SerialController) ReceiveShipment(c *gin.Context) {
	id, ok := serialPathUUID(c, "id")
	if !ok {
		apperror.Write(c, h.log, service.ErrShipmentNotFound)
		return
	}
	res, err := h.svc.ReceiveShipment(c.Request.Context(), auth.FromContext(c.Request.Context()), id)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, serialShipmentResponse(res))
}

func serialShipmentResponse(r service.ShipmentResult) dto.ShipmentResponse {
	sh := r.Shipment
	items := make([]dto.ShipmentItem, len(r.Items))
	for i, it := range r.Items {
		items[i] = dto.ShipmentItem{SerialUnitID: it.SerialUnitID.String(), ProductID: it.ProductID.String(), Qty: it.Qty}
	}
	return dto.ShipmentResponse{
		ID: sh.ID.String(), Code: sh.Code, Status: sh.Status,
		FromLocationID: sh.FromLocationID.String(), ToLocationID: sh.ToLocationID.String(),
		ToPartyID: serialUUIDString(sh.ToPartyID), ProgramCode: sh.ProgramCode,
		ShippedAt: sh.ShippedAt, ReceivedAt: sh.ReceivedAt, Items: items,
		Units: r.Units, Serials: r.Serials, Skipped: r.Skipped, AlreadyReceived: r.AlreadyReceived,
	}
}

// Recall: POST /api/v1/batches/{id}/recall.
func (h *SerialController) Recall(c *gin.Context) {
	id, ok := serialPathUUID(c, "id")
	if !ok {
		apperror.Write(c, h.log, service.ErrSerialBatchNotFound)
		return
	}
	var req dto.RecallRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.RecallBatch(c.Request.Context(), auth.FromContext(c.Request.Context()), id, req.Reason)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.RecallResponse{
		BatchID: res.BatchID.String(), Status: "RECALLED", RecalledAt: res.RecalledAt,
		Quarantined: res.Quarantined, AlreadyRecalled: res.AlreadyRecalled,
	})
}

// Trace: GET /api/v1/serials/{code}/trace.
func (h *SerialController) Trace(c *gin.Context) {
	code, ok := domain.NormalizeSerialCode(c.Param("code"))
	if !ok {
		apperror.Write(c, h.log, service.ErrSerialNotFound)
		return
	}
	res, err := h.svc.Trace(c.Request.Context(), auth.FromContext(c.Request.Context()), code)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, traceResponse(res))
}

func traceRef(id *uuid.UUID, code, name *string) *dto.TraceRef {
	if id == nil {
		return nil
	}
	return &dto.TraceRef{ID: serialUUIDString(id), Code: code, Name: name}
}

func traceResponse(r service.TraceResult) dto.TraceResponse {
	u := r.Unit
	out := dto.TraceResponse{
		Serial: dto.TraceSerial{
			ID: u.ID.String(), Code: u.Code, LevelCode: u.LevelCode, Status: u.Status,
			ParentID:       serialUUIDString(u.ParentID),
			Product:        traceRef(u.ProductID, u.ProductSKU, u.ProductName),
			Location:       traceRef(u.LocationID, u.LocationCode, u.LocationName),
			Custodian:      traceRef(u.CustodianPartyID, nil, u.CustodianName),
			CommissionedAt: u.CommissionedAt, VerifyCount: u.VerifyCount,
			FirstVerifiedAt: u.FirstVerifiedAt, FirstVerifiedCity: u.FirstVerifiedRegionName,
		},
		Ancestors:   make([]dto.TraceAncestor, len(r.Ancestors)),
		Descendants: make([]dto.TraceDescendant, len(r.Descendants)),
		Events:      make([]dto.TraceEvent, len(r.Events)),
		Shipments:   make([]dto.TraceShipment, len(r.Shipments)),
		Scans:       make([]dto.TraceScan, len(r.Scans)),
	}
	if u.BatchID != nil {
		out.Serial.Batch = &dto.TraceLot{ID: u.BatchID.String(), LotNo: u.LotNo, Status: u.BatchStatus, ExpDate: u.ExpDate}
	}
	for i, a := range r.Ancestors {
		out.Ancestors[i] = dto.TraceAncestor{ID: a.ID.String(), Code: a.Code, LevelCode: a.LevelCode, Status: a.Status, Depth: a.Depth}
	}
	for i, d := range r.Descendants {
		out.Descendants[i] = dto.TraceDescendant{LevelCode: d.LevelCode, Status: d.Status, Count: d.Count}
	}
	for i, e := range r.Events {
		out.Events[i] = dto.TraceEvent{
			ID: e.ID.String(), BizStep: e.BizStep, FromStatus: e.FromStatus, ToStatus: e.ToStatus,
			ParentCode: e.ParentCode, LocationCode: e.LocationCode, PartyName: e.PartyName,
			ActorType: e.ActorType, RefType: e.RefType, RefID: serialUUIDString(e.RefID), OccurredAt: e.OccurredAt,
		}
	}
	for i, s := range r.Shipments {
		out.Shipments[i] = traceShipment(s)
	}
	for i, s := range r.Scans {
		hits := []string(s.RuleHits)
		if hits == nil {
			hits = []string{}
		}
		out.Scans[i] = dto.TraceScan{
			ID: s.ID.String(), Context: s.Context, Result: s.Result, GeoSource: s.GeoSource,
			RegionCode: s.RegionCode, RegionName: s.RegionName, RuleHits: hits, OccurredAt: s.OccurredAt,
		}
	}
	return out
}

func traceShipment(s repository.TraceShipment) dto.TraceShipment {
	return dto.TraceShipment{
		ID: s.ID.String(), Code: s.Code, Status: s.Status, ProgramCode: s.ProgramCode,
		FromLocationCode: s.FromLocationCode, ToLocationCode: s.ToLocationCode, ToPartyName: s.ToPartyName,
		ViaCode: s.ViaCode, ShippedAt: s.ShippedAt, ReceivedAt: s.ReceivedAt,
	}
}
