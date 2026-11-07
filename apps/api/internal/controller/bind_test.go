package controller

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/middleware"
)

type bindLine struct {
	SKU string `json:"sku" binding:"required"`
	Qty int64  `json:"qty" binding:"required,gt=0,lte=1000"`
}

type bindRequest struct {
	Channel string     `json:"channel" binding:"required,oneof=D2C_WEB POS"`
	Note    string     `json:"note" binding:"max=10"`
	Lines   []bindLine `json:"lines" binding:"required,min=1,dive"`
}

func bindEngine(limit int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	SetupValidator()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := gin.New()
	r.Use(middleware.RequestID(), middleware.BodyLimit(limit))
	r.POST("/", func(c *gin.Context) {
		var req bindRequest
		if err := BindJSON(c, &req); err != nil {
			apperror.Write(c, log, err)
			return
		}
		c.JSON(http.StatusOK, req)
	})
	return r
}

func post(r *gin.Engine, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	var rdr io.Reader = strings.NewReader(body)
	if body == "" {
		rdr = nil
	}
	req := httptest.NewRequest(http.MethodPost, "/", rdr)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec
}

func TestBindJSON(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		limit      int64
		wantStatus int
		wantCode   string
		wantFields []string
	}{
		{name: "valid", body: `{"channel":"POS","lines":[{"sku":"A","qty":2}]}`, wantStatus: 200},
		{name: "valid unicode", body: `{"channel":"POS","note":"kué ☕","lines":[{"sku":"Ä","qty":1}]}`, wantStatus: 200},
		{name: "body kosong", body: "", wantStatus: 400, wantCode: "invalid_request"},
		{name: "json rusak", body: `{"channel":`, wantStatus: 400, wantCode: "invalid_request"},
		{name: "field tak dikenal ditolak", body: `{"channel":"POS","status":"PAID","lines":[{"sku":"A","qty":1}]}`, wantStatus: 400, wantCode: "invalid_request"},
		{name: "dua objek JSON", body: `{"channel":"POS","lines":[{"sku":"A","qty":1}]}{}`, wantStatus: 400, wantCode: "invalid_request"},
		{name: "tipe salah", body: `{"channel":"POS","lines":[{"sku":"A","qty":"dua"}]}`, wantStatus: 422, wantCode: "validation_failed", wantFields: []string{"lines[0].qty"}},
		{name: "validasi bersarang", body: `{"channel":"X","lines":[{"sku":"","qty":0}]}`, wantStatus: 422, wantCode: "validation_failed", wantFields: []string{"channel", "lines[0].sku", "lines[0].qty"}},
		{name: "array kosong", body: `{"channel":"POS","lines":[]}`, wantStatus: 422, wantCode: "validation_failed", wantFields: []string{"lines"}},
		{name: "batas atas", body: `{"channel":"POS","lines":[{"sku":"A","qty":1001}]}`, wantStatus: 422, wantCode: "validation_failed", wantFields: []string{"lines[0].qty"}},
		{name: "body terlalu besar", body: `{"channel":"POS","lines":[{"sku":"AAAAAAAAAAAAAAAAAAAA","qty":1}]}`, limit: 20, wantStatus: 413, wantCode: "body_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit := tt.limit
			if limit == 0 {
				limit = 1 << 20
			}
			rec := post(bindEngine(limit), tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, ingin %d (body %s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantCode == "" {
				return
			}
			var p apperror.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != tt.wantCode {
				t.Fatalf("code %q, ingin %q", p.Code, tt.wantCode)
			}
			got := map[string]bool{}
			for _, f := range p.Errors {
				got[f.Field] = true
				if f.Message == "" || f.Code == "" {
					t.Fatalf("field error tanpa code/pesan: %+v", f)
				}
			}
			for _, f := range tt.wantFields {
				if !got[f] {
					t.Fatalf("field %q tidak dilaporkan; dapat %+v", f, p.Errors)
				}
			}
		})
	}
}

func TestBindJSON_ReturnsAppErrors(t *testing.T) {
	// Semua error dari BindJSON harus *apperror.Error supaya tidak jatuh ke 500.
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`[1,2]`))
	var req bindRequest
	err := BindJSON(c, &req)
	var appErr *apperror.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("ingin *apperror.Error, dapat %T %v", err, err)
	}
}
