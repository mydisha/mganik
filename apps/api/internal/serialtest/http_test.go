package serialtest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/auth"
	"mganik/api/internal/config"
	"mganik/api/internal/seed"
)

func (f *fx) httpEngine(cfg config.Config) (http.Handler, map[string]*auth.Principal) {
	f.t.Helper()
	ps := map[string]*auth.Principal{
		"admin":      f.admin(),
		"noperm":     staffAll("cc.read"),
		"hub":        staffAt([]uuid.UUID{locHub}, allSerialPerms...),
		"consumer":   f.newConsumer(),
		"consumer2":  f.newConsumer(),
		"reviewer":   staffAll("loyalty.review"),
		"production": staffAt([]uuid.UUID{locCentral}, "code.mint", "serial.commission", "serial.receive"),
	}
	return f.engine(cfg, ps), ps
}

func TestHTTP_AuthzMatrix(t *testing.T) {
	f := newFx(t)
	h, _ := f.httpEngine(f.cfg)
	shipID := uuid.Must(uuid.NewV7()).String()
	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/code-batches", map[string]any{"level_code": "UNIT", "qty": 1, "printer_vendor": "V"}},
		{http.MethodPost, "/api/v1/serials/commission", map[string]any{"batch_id": seed.BatchID(), "codes": []string{unit(0).Code}}},
		{http.MethodPost, "/api/v1/serials/receive", map[string]any{"location_id": locCentral, "codes": []string{unit(0).Code}}},
		{http.MethodPost, "/api/v1/serials/aggregate", map[string]any{"parent_code": cartonCode(0), "child_codes": []string{unit(0).Code}}},
		{http.MethodPost, "/api/v1/serials/" + cartonCode(0) + "/unpack", nil},
		{http.MethodGet, "/api/v1/serials/" + unit(0).Code + "/trace", nil},
		{http.MethodPost, "/api/v1/batches/" + seed.BatchID().String() + "/recall", map[string]any{"reason": "uji"}},
		{http.MethodPost, "/api/v1/shipments", map[string]any{"from_location_id": locCentral, "to_location_id": locSulsel, "codes": []string{palletCode()}}},
		{http.MethodPost, "/api/v1/shipments/" + shipID + "/receive", nil},
		{http.MethodPost, "/api/v1/loyalty/claims/" + shipID + "/review", map[string]any{"decision": "APPROVE"}},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			if r := do(h, c.method, c.path, "", c.body); r.Code != http.StatusUnauthorized || r.problem(t).Code != "unauthenticated" {
				t.Fatalf("anonim: %d %s", r.Code, r.Body)
			}
			if r := do(h, c.method, c.path, "noperm", c.body); r.Code != http.StatusForbidden || r.problem(t).Code != "forbidden" {
				t.Fatalf("tanpa permission: %d %s", r.Code, r.Body)
			}
			if r := do(h, c.method, c.path, "consumer", c.body); r.Code != http.StatusForbidden {
				t.Fatalf("konsumen di route staff: %d %s", r.Code, r.Body)
			}
		})
	}
	// Scope lokasi (BOLA) lewat HTTP.
	r := do(h, http.MethodPost, "/api/v1/shipments", "hub",
		map[string]any{"from_location_id": locCentral, "to_location_id": locSulsel, "codes": []string{palletCode()}})
	if r.Code != http.StatusForbidden {
		t.Fatalf("shipment di luar scope: %d %s", r.Code, r.Body)
	}
	r = do(h, http.MethodGet, "/api/v1/serials/"+unit(0).Code+"/trace", "hub", nil)
	if r.Code != http.StatusForbidden {
		t.Fatalf("trace di luar scope: %d %s", r.Code, r.Body)
	}
	// Klaim: anonim 401, staff 403.
	claim := map[string]any{"code": unit(0).Code, "pin": unit(0).PIN}
	if r := do(h, http.MethodPost, "/api/v1/public/claims", "", claim); r.Code != http.StatusUnauthorized {
		t.Fatalf("klaim anonim: %d", r.Code)
	}
	if r := do(h, http.MethodPost, "/api/v1/public/claims", "admin", claim); r.Code != http.StatusForbidden {
		t.Fatalf("klaim oleh staff: %d", r.Code)
	}
	if r := do(h, http.MethodGet, "/api/v1/public/loyalty/balance", "", nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("saldo anonim: %d", r.Code)
	}
}

func TestHTTP_ValidationEdges(t *testing.T) {
	f := newFx(t)
	h, _ := f.httpEngine(f.cfg)
	post := func(path string, body any) httpResp { return do(h, http.MethodPost, path, "admin", body) }

	// Kosong.
	r := post("/api/v1/shipments", map[string]any{"from_location_id": locCentral, "to_location_id": locSulsel, "codes": []string{}})
	if r.Code != http.StatusUnprocessableEntity || r.problem(t).Errors[0].Field != "codes" {
		t.Fatalf("codes kosong: %d %s", r.Code, r.Body)
	}
	// Duplikat setelah normalisasi (huruf kecil + tanda hubung = kode yang sama).
	dup := strings.ToLower(unit(0).Code[:8]) + "-" + strings.ToLower(unit(0).Code[8:])
	r = post("/api/v1/serials/commission", map[string]any{"batch_id": seed.BatchID(), "codes": []string{unit(0).Code, dup}})
	p := r.problem(t)
	if r.Code != http.StatusUnprocessableEntity || p.Code != "duplicate_code" || len(p.Errors) != 1 ||
		p.Errors[0].Field != "codes[1]" || p.Errors[0].Code != "duplicate_code" {
		t.Fatalf("duplikat: %d %s", r.Code, r.Body)
	}
	// Format salah.
	r = post("/api/v1/serials/aggregate", map[string]any{"parent_code": "BUKAN", "child_codes": []string{unit(0).Code}})
	if r.Code != http.StatusUnprocessableEntity || r.problem(t).Errors[0].Code != "invalid_format" {
		t.Fatalf("format salah: %d %s", r.Code, r.Body)
	}
	// Field tak dikenal (mass assignment) dan UUID salah.
	r = post("/api/v1/code-batches", map[string]any{"level_code": "UNIT", "qty": 1, "printer_vendor": "V", "status": "COMMISSIONED"})
	if r.Code != http.StatusBadRequest {
		t.Fatalf("field tak dikenal: %d %s", r.Code, r.Body)
	}
	r = post("/api/v1/code-batches", map[string]any{"level_code": "UNIT", "qty": 6000, "printer_vendor": "V", "product_id": "bukan-uuid"})
	if r.Code != http.StatusUnprocessableEntity || len(r.problem(t).Errors) != 2 {
		t.Fatalf("qty & uuid: %d %s", r.Code, r.Body)
	}
	// Lat tanpa lng.
	r = do(h, http.MethodPost, "/api/v1/public/verify", "", map[string]any{"code": unit(0).Code, "lat": -6.2})
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lat tanpa lng: %d %s", r.Code, r.Body)
	}
	// Path dengan kode/UUID salah → 404 tanpa DB.
	if r := do(h, http.MethodGet, "/api/v1/serials/xyz/trace", "admin", nil); r.Code != http.StatusNotFound || r.problem(t).Code != "serial_not_found" {
		t.Fatalf("trace kode salah: %d %s", r.Code, r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/shipments/bukan-uuid/receive", "admin", nil); r.Code != http.StatusNotFound {
		t.Fatalf("shipment id salah: %d", r.Code)
	}
	// Review: keputusan tidak dikenal.
	r = do(h, http.MethodPost, "/api/v1/loyalty/claims/"+uuid.NewString()+"/review", "reviewer", map[string]any{"decision": "MAYBE"})
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("decision: %d %s", r.Code, r.Body)
	}
}

func TestHTTP_MintCommissionAggregateShipFlow(t *testing.T) {
	f := newFx(t)
	h, _ := f.httpEngine(f.cfg)
	note := "Label batch Ramadan 🌙 — cetak ulang"
	r := do(h, http.MethodPost, "/api/v1/code-batches", "production", map[string]any{
		"level_code": "UNIT", "qty": 2, "product_id": seed.ProductID(seed.SKU1), "printer_vendor": "Vendor Ü", "notes": note,
	})
	if r.Code != http.StatusCreated || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("mint: %d %s", r.Code, r.Body)
	}
	var mint struct {
		CodeBatch struct {
			Notes string `json:"notes"`
		} `json:"code_batch"`
		Codes []struct{ Code, PIN string } `json:"codes"`
	}
	if err := json.Unmarshal(r.Body, &mint); err != nil || len(mint.Codes) != 2 || mint.Codes[0].PIN == "" || mint.CodeBatch.Notes != note {
		t.Fatalf("respons mint %s", r.Body)
	}
	codes := []string{mint.Codes[0].Code, mint.Codes[1].Code}
	if r := do(h, http.MethodPost, "/api/v1/serials/commission", "production", map[string]any{"batch_id": seed.BatchID(), "codes": codes}); r.Code != http.StatusOK {
		t.Fatalf("commission: %d %s", r.Code, r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/serials/receive", "production", map[string]any{"location_id": locCentral, "codes": codes}); r.Code != http.StatusOK {
		t.Fatalf("receive: %d %s", r.Code, r.Body)
	}
	// Karton 0 penuh (12): unit baru ditolak dengan 409.
	r = do(h, http.MethodPost, "/api/v1/serials/aggregate", "admin", map[string]any{"parent_code": cartonCode(0), "child_codes": codes[:1]})
	if r.Code != http.StatusConflict || r.problem(t).Code != "capacity_exceeded" {
		t.Fatalf("aggregate penuh: %d %s", r.Code, r.Body)
	}
	r = do(h, http.MethodPost, "/api/v1/shipments", "admin", map[string]any{
		"from_location_id": locCentral, "to_location_id": locSulsel, "program_code": "SUBSIDI_SULSEL_Q4", "codes": []string{palletCode()},
	})
	if r.Code != http.StatusCreated {
		t.Fatalf("shipment: %d %s", r.Code, r.Body)
	}
	var sh struct {
		ID    string `json:"id"`
		Units int    `json:"units"`
	}
	_ = json.Unmarshal(r.Body, &sh)
	if sh.Units != 48 {
		t.Fatalf("shipment %s", r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/shipments/"+sh.ID+"/receive", "admin", nil); r.Code != http.StatusOK {
		t.Fatalf("terima: %d %s", r.Code, r.Body)
	}
	r = do(h, http.MethodGet, "/api/v1/serials/"+strings.ToLower(unit(0).Code)+"/trace", "admin", nil)
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"via_code":"`+palletCode()+`"`) {
		t.Fatalf("trace: %d %s", r.Code, r.Body)
	}
}

// Respons verifikasi publik hanya memuat field yang aman, men-set cookie
// anonim, dan tidak membocorkan status internal.
func TestHTTP_VerifyResponseAndCookie(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	h, _ := f.httpEngine(f.cfg)
	r := do(h, http.MethodPost, "/api/v1/public/verify", "", map[string]any{"code": unit(0).Code, "lat": geoMakassar.Lat, "lng": geoMakassar.Lng})
	if r.Code != http.StatusOK {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	var body map[string]any
	_ = json.Unmarshal(r.Body, &body)
	for k := range body {
		switch k {
		case "result", "product", "first_verified_at", "first_verified_city":
		default:
			t.Fatalf("field %q tidak boleh ada di respons publik: %s", k, r.Body)
		}
	}
	if body["result"] != "GENUINE_FIRST" || strings.Contains(string(r.Body), "AT_PARTNER") {
		t.Fatalf("respons %s", r.Body)
	}
	cookie := r.Header.Get("Set-Cookie")
	if !strings.Contains(cookie, "mg_vid=") || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
		t.Fatalf("cookie anonim %q", cookie)
	}
	// Cookie yang sama dipakai ulang (fingerprint stabil, tidak di-set ulang).
	vid := strings.TrimPrefix(strings.Split(cookie, ";")[0], "mg_vid=")
	r = do(h, http.MethodPost, "/api/v1/public/verify", "", map[string]any{"code": unit(0).Code}, func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: "mg_vid", Value: vid})
	})
	if r.Header.Get("Set-Cookie") != "" {
		t.Fatal("cookie valid tidak boleh di-set ulang")
	}
	if n := f.count(`SELECT count(DISTINCT actor_fingerprint) FROM scan_events WHERE raw_code = ?`, unit(0).Code); n != 1 {
		t.Fatalf("fingerprint berbeda %d, ingin 1 (cookie sama)", n)
	}
	// NOT_FOUND: hanya result.
	r = do(h, http.MethodPost, "/api/v1/public/verify", "", map[string]any{"code": "kode-ngawur"})
	if r.Code != http.StatusOK || strings.TrimSpace(string(r.Body)) != `{"result":"NOT_FOUND"}` {
		t.Fatalf("NOT_FOUND: %s", r.Body)
	}
}

func TestHTTP_TestIPGeoHeaderIgnoredInProduction(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	dev, _ := f.httpEngine(f.cfg)
	setGeo := func(req *http.Request) { req.Header.Set("X-Test-IP-Geo", "-5.1477,119.4327") }
	do(dev, http.MethodPost, "/api/v1/public/verify", "", map[string]any{"code": unit(0).Code}, setGeo)
	prod := f.cfg
	prod.AppEnv = config.EnvProduction
	ph, _ := f.httpEngine(prod)
	do(ph, http.MethodPost, "/api/v1/public/verify", "", map[string]any{"code": unit(1).Code}, setGeo)
	if n := f.count(`SELECT count(*) FROM scan_events WHERE raw_code = ? AND geo_source = 'IP' AND region_code = '73.71'`, unit(0).Code); n != 1 {
		t.Fatal("header uji tidak dipakai di non-production")
	}
	if n := f.count(`SELECT count(*) FROM scan_events WHERE raw_code = ? AND geo_source = 'NONE'`, unit(1).Code); n != 1 {
		t.Fatal("header uji dipakai di production")
	}
}

func TestHTTP_RateLimitPerIP(t *testing.T) {
	f := newFx(t)
	cfg := f.cfg
	cfg.Serialization.VerifyRatePerMinute = 3
	h, _ := f.httpEngine(cfg)
	from := func(ip string) func(*http.Request) {
		return func(req *http.Request) { req.RemoteAddr = ip + ":40000" }
	}
	body := map[string]any{"code": "kode-ngawur"}
	for i := range 3 {
		if r := do(h, http.MethodPost, "/api/v1/public/verify", "", body, from("192.0.2.1")); r.Code != http.StatusOK {
			t.Fatalf("request ke-%d: %d", i+1, r.Code)
		}
	}
	r := do(h, http.MethodPost, "/api/v1/public/verify", "", body, from("192.0.2.1"))
	if r.Code != http.StatusTooManyRequests || r.problem(t).Code != "rate_limited" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("ke-4: %d %s", r.Code, r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/public/verify", "", body, from("192.0.2.2")); r.Code != http.StatusOK {
		t.Fatalf("IP lain ikut dibatasi: %d", r.Code)
	}
	f.clk.Advance(time.Minute)
	if r := do(h, http.MethodPost, "/api/v1/public/verify", "", body, from("192.0.2.1")); r.Code != http.StatusOK {
		t.Fatalf("setelah jendela berlalu: %d", r.Code)
	}
}

func TestHTTP_ClaimAndReviewFlow(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	h, _ := f.httpEngine(f.cfg)
	claim := map[string]any{"code": unit(20).Code, "pin": strings.ToLower(unit(20).PIN)}
	r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer", claim)
	if r.Code != http.StatusCreated {
		t.Fatalf("klaim: %d %s", r.Code, r.Body)
	}
	var cl map[string]any
	_ = json.Unmarshal(r.Body, &cl)
	if cl["status"] != "PENDING" || cl["risk_score"] != nil {
		t.Fatalf("respons klaim %s", r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer2", claim); r.Code != http.StatusConflict || r.problem(t).Code != "already_claimed" {
		t.Fatalf("klaim kedua: %d %s", r.Code, r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer", claim); r.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", r.Code, r.Body)
	}
	r = do(h, http.MethodPost, "/api/v1/loyalty/claims/"+cl["claim_id"].(string)+"/review", "reviewer",
		map[string]any{"decision": "REJECT", "note": "Bukti struk tidak cocok — ditolak ✋"})
	if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"status":"REJECTED"`) {
		t.Fatalf("review: %d %s", r.Code, r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer2", claim); r.Code != http.StatusCreated {
		t.Fatalf("pembeli asli setelah tolak: %d %s", r.Code, r.Body)
	}
	r = do(h, http.MethodGet, "/api/v1/public/loyalty/balance", "consumer2", nil)
	if r.Code != http.StatusOK || strings.TrimSpace(string(r.Body)) != `{"available":0,"pending":10}` {
		t.Fatalf("saldo: %d %s", r.Code, r.Body)
	}
}
