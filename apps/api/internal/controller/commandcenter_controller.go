package controller

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/dto"
	"mganik/api/internal/pagination"
	"mganik/api/internal/service"
)

type commandCenterService interface {
	Summary(ctx context.Context) (dto.CCSummaryResponse, error)
	Stock(ctx context.Context, in service.StockQuery) (pagination.Page[dto.CCStockItem], error)
	Runout(ctx context.Context, in service.RunoutQuery) (dto.CCRunoutResponse, error)
	Anomalies(ctx context.Context, in service.AnomalyQuery) (pagination.Page[dto.CCAnomalyItem], error)
	UpdateAnomaly(ctx context.Context, in service.AnomalyUpdate) (dto.CCAnomalyItem, error)
	Conflicts(ctx context.Context, in service.ConflictQuery) (pagination.Page[dto.CCConflictItem], error)
	Leakage(ctx context.Context, in service.LeakageQuery) (dto.CCLeakageResponse, error)
}

// CommandCenterController menangani /api/v1/cc/*. Semua query param
// divalidasi di sini (format, enum, batas); otorisasi dan scope di service.
type CommandCenterController struct {
	svc commandCenterService
	log *slog.Logger
}

func NewCommandCenterController(svc commandCenterService, log *slog.Logger) *CommandCenterController {
	return &CommandCenterController{svc: svc, log: log}
}

var (
	ccRunoutLevels  = []string{"CRITICAL", "WARNING", "INSUFFICIENT_DATA", "OK"}
	ccAnomalyStatus = []string{"OPEN", "ACK", "RESOLVED", "FALSE_POSITIVE"}
	ccSeverities    = []string{"LOW", "MEDIUM", "HIGH", "CRITICAL"}
	ccConflictStat  = []string{"OPEN", "AUTO_RESOLVED", "RESOLVED"}
	ccConflictKinds = []string{"OFFLINE_SALE_PREEMPT", "SERIAL_DOUBLE_SALE", "STOCK_DISCREPANCY"}
	// Tipe lokasi dan kode aturan anomali adalah data lookup (location_types,
	// anomaly_rules), jadi hanya format yang dicek; kode tak dikenal = hasil kosong.
	ccLookupCodeFormat = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// queryReader mengumpulkan semua kesalahan query param untuk satu 422.
type queryReader struct {
	c      *gin.Context
	fields []apperror.FieldError
}

func (q *queryReader) fail(field, code, msg string) {
	q.fields = append(q.fields, apperror.FieldError{Field: field, Code: code, Message: msg})
}

// raw mengambil satu nilai; param yang dikirim lebih dari sekali ditolak.
func (q *queryReader) raw(name string) (string, bool) {
	vals, ok := q.c.GetQueryArray(name)
	if !ok {
		return "", false
	}
	if len(vals) > 1 {
		q.fail(name, "duplicate", "hanya boleh satu nilai")
		return "", false
	}
	v := strings.TrimSpace(vals[0])
	return v, v != ""
}

func (q *queryReader) str(name string) string {
	v, _ := q.raw(name)
	return v
}

func (q *queryReader) uuid(name string) *uuid.UUID {
	v, ok := q.raw(name)
	if !ok {
		return nil
	}
	id, err := uuid.Parse(v)
	if err != nil || id == uuid.Nil {
		q.fail(name, "uuid", "harus UUID yang valid")
		return nil
	}
	return &id
}

func (q *queryReader) enum(name string, allowed []string) *string {
	v, ok := q.raw(name)
	if !ok {
		return nil
	}
	for _, a := range allowed {
		if v == a {
			return &v
		}
	}
	q.fail(name, "oneof", "harus salah satu dari: "+strings.Join(allowed, ", "))
	return nil
}

func (q *queryReader) pattern(name string, re *regexp.Regexp) *string {
	v, ok := q.raw(name)
	if !ok {
		return nil
	}
	if !re.MatchString(v) {
		q.fail(name, "format", "harus berupa kode dari huruf besar, angka, atau garis bawah")
		return nil
	}
	return &v
}

func (q *queryReader) intRange(name string, lo, hi int) int {
	v, ok := q.raw(name)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		q.fail(name, "invalid_type", "harus bilangan bulat")
		return 0
	}
	if n < lo || n > hi {
		q.fail(name, "out_of_range", fmt.Sprintf("harus antara %d dan %d", lo, hi))
		return 0
	}
	return n
}

// page membaca limit dan cursor. Limit dicek di sini supaya ikut dalam satu
// 422 bersama param lain; cursor di-decode service (butuh tipe key endpoint).
func (q *queryReader) page() service.Page {
	limit := q.str(pagination.FieldLimit)
	if _, ferr := pagination.ParseLimit(limit); ferr != nil {
		q.fields = append(q.fields, *ferr)
	}
	return service.Page{Limit: limit, Cursor: q.str(pagination.FieldCursor)}
}

func (q *queryReader) err() error {
	if len(q.fields) == 0 {
		return nil
	}
	return apperror.Validation(q.fields...)
}

// hasControlChars melaporkan karakter kontrol C0/C1 dan DEL selain \n dan \t.
// NUL tidak bisa disimpan di kolom text Postgres (sebelumnya jadi 500), dan
// karakter kontrol lain merusak tampilan/log. Ini satu-satunya input teks
// bebas di /cc; param lain dibatasi enum, UUID, atau pola kode.
func hasControlChars(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

// Summary: GET /cc/summary
func (h *CommandCenterController) Summary(c *gin.Context) {
	out, err := h.svc.Summary(c.Request.Context())
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Stock: GET /cc/stock?location_type=&location_id=&product_id=&limit=&cursor=
func (h *CommandCenterController) Stock(c *gin.Context) {
	q := &queryReader{c: c}
	in := service.StockQuery{
		Page:         q.page(),
		LocationType: q.pattern("location_type", ccLookupCodeFormat),
		LocationID:   q.uuid("location_id"),
		ProductID:    q.uuid("product_id"),
	}
	if err := q.err(); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out, err := h.svc.Stock(c.Request.Context(), in)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Runout: GET /cc/runout?location_type=&location_id=&product_id=&level=&limit=&cursor=
func (h *CommandCenterController) Runout(c *gin.Context) {
	q := &queryReader{c: c}
	in := service.RunoutQuery{
		Page:         q.page(),
		LocationType: q.pattern("location_type", ccLookupCodeFormat),
		LocationID:   q.uuid("location_id"),
		ProductID:    q.uuid("product_id"),
		Level:        q.enum("level", ccRunoutLevels),
	}
	if err := q.err(); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out, err := h.svc.Runout(c.Request.Context(), in)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Anomalies: GET /cc/anomalies?status=&severity=&rule=&party_id=&limit=&cursor=
func (h *CommandCenterController) Anomalies(c *gin.Context) {
	q := &queryReader{c: c}
	in := service.AnomalyQuery{
		Page:     q.page(),
		Status:   q.enum("status", ccAnomalyStatus),
		Severity: q.enum("severity", ccSeverities),
		RuleCode: q.pattern("rule", ccLookupCodeFormat),
		PartyID:  q.uuid("party_id"),
	}
	if err := q.err(); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out, err := h.svc.Anomalies(c.Request.Context(), in)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// UpdateAnomaly: PATCH /cc/anomalies/{id} {status, note}
func (h *CommandCenterController) UpdateAnomaly(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		apperror.Write(c, h.log, apperror.Validation(apperror.FieldError{
			Field: "id", Code: "uuid", Message: "harus UUID yang valid"}))
		return
	}
	var req dto.CCAnomalyUpdateRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	var note *string
	if req.Note != nil {
		if hasControlChars(*req.Note) {
			apperror.Write(c, h.log, apperror.Validation(apperror.FieldError{
				Field: "note", Code: "invalid_characters",
				Message: "memuat karakter tak terlihat yang tidak diizinkan (baris baru dan tab boleh)"}))
			return
		}
		if n := strings.TrimSpace(*req.Note); n != "" {
			note = &n
		}
	}
	out, err := h.svc.UpdateAnomaly(c.Request.Context(), service.AnomalyUpdate{
		ID: id, Status: req.Status, Note: note, RequestID: c.GetString(apperror.RequestIDKey),
	})
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Conflicts: GET /cc/conflicts?status=&kind=&location_id=&limit=&cursor=
func (h *CommandCenterController) Conflicts(c *gin.Context) {
	q := &queryReader{c: c}
	in := service.ConflictQuery{
		Page:       q.page(),
		Status:     q.enum("status", ccConflictStat),
		Kind:       q.enum("kind", ccConflictKinds),
		LocationID: q.uuid("location_id"),
	}
	if err := q.err(); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out, err := h.svc.Conflicts(c.Request.Context(), in)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Leakage: GET /cc/distribution-leakage?days=&party_id=&limit=&cursor=
func (h *CommandCenterController) Leakage(c *gin.Context) {
	q := &queryReader{c: c}
	in := service.LeakageQuery{
		Page:    q.page(),
		Days:    q.intRange("days", 1, service.LeakageMaxDays),
		PartyID: q.uuid("party_id"),
	}
	if err := q.err(); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	out, err := h.svc.Leakage(c.Request.Context(), in)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
