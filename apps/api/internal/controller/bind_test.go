package controller

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/dto"
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

// Pesan field memakai satuan sesuai jenis field dan nama tipe JSON, bukan
// nama tipe Go.
func TestBindJSON_FieldMessages(t *testing.T) {
	tests := []struct {
		name, body, field, want string
	}{
		{"teks terlalu panjang", `{"channel":"POS","note":"12345678901","lines":[{"sku":"A","qty":1}]}`, "note", "maksimal 10 karakter"},
		{"daftar kosong", `{"channel":"POS","lines":[]}`, "lines", "minimal 1 item"},
		{"angka di atas batas", `{"channel":"POS","lines":[{"sku":"A","qty":1001}]}`, "lines[0].qty", "maksimal 1.000"},
		{"enum", `{"channel":"X","lines":[{"sku":"A","qty":1}]}`, "channel", "harus salah satu dari: D2C_WEB, POS"},
		{"tipe angka", `{"channel":"POS","lines":[{"sku":"A","qty":"dua"}]}`, "lines[0].qty", "harus berupa bilangan bulat"},
		{"tipe teks", `{"channel":5,"lines":[{"sku":"A","qty":1}]}`, "channel", "harus berupa teks"},
		{"tipe daftar", `{"channel":"POS","lines":{}}`, "lines", "harus berupa daftar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(bindEngine(1<<20), tt.body)
			var p apperror.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != "validation_failed" {
				t.Fatalf("code %q (%s)", p.Code, rec.Body)
			}
			for _, f := range p.Errors {
				if f.Field == tt.field {
					if f.Message != tt.want {
						t.Fatalf("pesan %q, ingin %q", f.Message, tt.want)
					}
					return
				}
			}
			t.Fatalf("field %q tidak dilaporkan: %+v", tt.field, p.Errors)
		})
	}
}

// Aturan validator di DTO publik/staff tidak boleh bocor sebagai nama tag
// atau nama field Go, dan angka nol pada field wajib dijelaskan dengan benar.
func TestBindJSON_DTOFieldMessages(t *testing.T) {
	bindTo := func(t *testing.T, dst any, body string) apperror.Problem {
		t.Helper()
		gin.SetMode(gin.TestMode)
		SetupValidator()
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var ae *apperror.Error
		if err := BindJSON(c, dst); !errors.As(err, &ae) || ae.Code != "validation_failed" {
			t.Fatalf("ingin validation_failed, dapat %v", err)
		}
		return apperror.Problem{Code: ae.Code, Errors: ae.Fields}
	}
	messageOf := func(t *testing.T, p apperror.Problem, field string) string {
		t.Helper()
		for _, f := range p.Errors {
			if f.Field == field {
				return f.Message
			}
		}
		t.Fatalf("field %q tidak dilaporkan: %+v", field, p.Errors)
		return ""
	}

	// required_with: lat tanpa lng (konsumen, /public/verify).
	p := bindTo(t, &dto.VerifyRequest{}, `{"code":"ABCDEFGHJKMNPQRS","lat":-6.2}`)
	if got := messageOf(t, p, "lng"); got != "wajib diisi bersama lat" {
		t.Fatalf("required_with: %q", got)
	}
	// required pada angka yang diisi 0.
	adj := `{"location_id":"0190a6f1-0000-7000-8000-000000000001","product_id":"0190a6f1-0000-7000-8000-000000000002","reason":"opname"`
	p = bindTo(t, &dto.AdjustmentRequest{}, adj+`,"qty_delta":0}`)
	if got := messageOf(t, p, "qty_delta"); got != "wajib diisi dan tidak boleh 0" {
		t.Fatalf("required angka: %q", got)
	}
	// Batas angka memakai pengelompokan id-ID.
	p = bindTo(t, &dto.AdjustmentRequest{}, adj+`,"qty_delta":2000000}`)
	if got := messageOf(t, p, "qty_delta"); got != "maksimal 1.000.000" {
		t.Fatalf("max angka: %q", got)
	}
	p = bindTo(t, &dto.AdjustmentRequest{}, adj+`,"qty_delta":-2000000}`)
	if got := messageOf(t, p, "qty_delta"); got != "minimal -1.000.000" {
		t.Fatalf("min angka: %q", got)
	}
}

func TestValidationHelpers(t *testing.T) {
	if got := goFieldsToJSON("Lng ProgramCode"); got != "lng, program_code" {
		t.Errorf("goFieldsToJSON: %q", got)
	}
	for in, want := range map[string]string{"0": "0", "1000000": "1.000.000", "-90": "-90", "D2C_WEB POS": "D2C_WEB POS"} {
		if got := formatParam(in); got != want {
			t.Errorf("formatParam(%q) = %q, ingin %q", in, got, want)
		}
	}
}

// Tag ne (dipakai qty_delta) punya pesan sendiri, tidak jatuh ke default.
type bindNE struct {
	Qty int64 `json:"qty" binding:"ne=5"`
}

func TestBindJSON_NotEqualMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetupValidator()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"qty":5}`))
	var ae *apperror.Error
	if err := BindJSON(c, &bindNE{}); !errors.As(err, &ae) || len(ae.Fields) != 1 || ae.Fields[0].Message != "tidak boleh 5" {
		t.Fatalf("ne: %v %+v", err, ae)
	}
}

func TestJSONTypeName(t *testing.T) {
	for typ, want := range map[reflect.Type]string{
		reflect.TypeFor[int64]():          "bilangan bulat",
		reflect.TypeFor[*int]():           "bilangan bulat",
		reflect.TypeFor[float64]():        "angka",
		reflect.TypeFor[bool]():           "true atau false",
		reflect.TypeFor[[]string]():       "daftar",
		reflect.TypeFor[bindLine]():       "objek",
		reflect.TypeFor[uuid.UUID]():      "teks",
		reflect.TypeFor[time.Time]():      "teks",
		reflect.TypeFor[chan int]():       "nilai yang valid",
		reflect.TypeFor[map[string]int](): "objek",
	} {
		if got := jsonTypeName(typ); got != want {
			t.Errorf("%v: %q, ingin %q", typ, got, want)
		}
	}
	if got := jsonTypeName(nil); got != "nilai yang valid" {
		t.Errorf("nil: %q", got)
	}
}
