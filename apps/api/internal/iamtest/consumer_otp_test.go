package iamtest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/config"
	"mganik/api/internal/model"
	"mganik/api/internal/pii"
	"mganik/api/internal/service"
)

const testPhone = "081234567890"

// Bagian nomor yang tidak boleh muncul di log/audit/DB polos.
const phoneDigits = "81234567890"

func (e *env) requestOTP(phone string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/public/otp/request", map[string]string{"phone": phone}, opts...)
}

func (e *env) verifyOTP(phone, code string, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/public/otp/verify", map[string]string{"phone": phone, "code": code}, opts...)
}

type consumerBody struct {
	ID          uuid.UUID `json:"id"`
	PhoneMasked string    `json:"phone_masked"`
}

func TestOTP_E2E_LoginProfileLogout(t *testing.T) {
	e := newEnv(t)
	req := e.requestOTP(testPhone)
	if req.Code != http.StatusAccepted {
		t.Fatalf("request OTP %d: %s", req.Code, req.Body)
	}
	var reqBody struct {
		ExpiresAt        time.Time `json:"expires_at"`
		ExpiresInSeconds int       `json:"expires_in_seconds"`
	}
	if err := json.Unmarshal(req.Body.Bytes(), &reqBody); err != nil {
		t.Fatal(err)
	}
	if !reqBody.ExpiresAt.Equal(t0.Add(5*time.Minute)) || reqBody.ExpiresInSeconds != 300 {
		t.Fatalf("respons request OTP: %s", req.Body)
	}
	if strings.Contains(req.Body.String(), config.DemoOTPCode) {
		t.Fatal("kode OTP dikembalikan di respons")
	}

	wrong := expectProblem(t, e.verifyOTP(testPhone, "000000"), http.StatusUnauthorized, "otp_invalid")
	if !strings.Contains(wrong.Detail, "Sisa 4 percobaan") {
		t.Fatalf("detail: %q", wrong.Detail)
	}

	ok := e.verifyOTP("+62 812-3456-7890", config.DemoOTPCode) // format berbeda, nomor sama
	if ok.Code != http.StatusOK {
		t.Fatalf("verify %d: %s", ok.Code, ok.Body)
	}
	var cb consumerBody
	if err := json.Unmarshal(ok.Body.Bytes(), &cb); err != nil {
		t.Fatal(err)
	}
	if cb.PhoneMasked != "+62812*****890" || strings.Contains(ok.Body.String(), phoneDigits) {
		t.Fatalf("nomor tidak disamarkan: %s", ok.Body)
	}
	c := cookieNamed(ok, service.ConsumerSessionCookie)
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 7*24*3600 {
		t.Fatalf("cookie konsumen: %+v", c)
	}
	if cookieNamed(ok, service.StaffSessionCookie) != nil {
		t.Fatal("verifikasi OTP memasang cookie staff")
	}

	// Enkripsi: ciphertext ≠ plaintext, blind index = HMAC kunci aktif, stabil.
	var cons model.Consumer
	if err := e.db.First(&cons, "id = ?", cb.ID).Error; err != nil {
		t.Fatal(err)
	}
	keys, err := pii.New([]byte(testPIIKey))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cons.PhoneEnc, []byte(phoneDigits)) {
		t.Fatal("phone_enc memuat nomor polos")
	}
	if got, err := keys.DecryptPhone(cons.PhoneEnc, cons.PhoneBidx); err != nil || got != "+6281234567890" {
		t.Fatalf("dekripsi phone_enc: %q %v", got, err)
	}
	// Ciphertext terikat ke baris: tukar phone_enc ke konsumen lain → tidak
	// bisa dibuka (profil gagal, bukan menampilkan nomor orang lain).
	other := model.Consumer{ID: uuid.Must(uuid.NewV7()), PhoneEnc: cons.PhoneEnc, PhoneBidx: keys.PhoneIndex("+6289999999999"), Status: "ACTIVE"}
	if err := e.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := keys.DecryptPhone(other.PhoneEnc, other.PhoneBidx); err == nil {
		t.Fatal("ciphertext yang ditukar ke baris lain masih bisa dibuka")
	}
	if !bytes.Equal(cons.PhoneBidx, keys.PhoneIndex("+6281234567890")) {
		t.Fatal("blind index tidak cocok dengan HMAC nomor E.164")
	}

	me := e.do(http.MethodGet, "/api/v1/public/consumer/me", nil, withCookie(c))
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), cb.ID.String()) || !strings.Contains(me.Body.String(), "+62812*****890") {
		t.Fatalf("consumer me %d: %s", me.Code, me.Body)
	}
	// Cookie konsumen tidak memberi akses staff.
	expectProblem(t, e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)), http.StatusUnauthorized, "unauthenticated")

	// Login kedua dengan nomor yang sama (format lain) → konsumen yang sama.
	if r := e.requestOTP("6281234567890"); r.Code != http.StatusAccepted {
		t.Fatalf("request ulang %d", r.Code)
	}
	again := e.verifyOTP("6281234567890", config.DemoOTPCode, withCookie(c))
	var cb2 consumerBody
	_ = json.Unmarshal(again.Body.Bytes(), &cb2)
	if again.Code != http.StatusOK || cb2.ID != cb.ID {
		t.Fatalf("konsumen ganda: %s", again.Body)
	}
	if n := countRows(t, e.db, `SELECT count(*) FROM consumers WHERE id <> ?`, other.ID); n != 1 {
		t.Fatalf("%d konsumen, ingin 1", n)
	}
	// Sesi lama dicabut saat login ulang dari browser yang sama.
	expectProblem(t, e.do(http.MethodGet, "/api/v1/public/consumer/me", nil, withCookie(c)), http.StatusUnauthorized, "unauthenticated")
	c2 := cookieNamed(again, service.ConsumerSessionCookie)

	out := e.do(http.MethodPost, "/api/v1/public/consumer/logout", nil, withCookie(c2))
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout konsumen %d", out.Code)
	}
	expectProblem(t, e.do(http.MethodGet, "/api/v1/public/consumer/me", nil, withCookie(c2)), http.StatusUnauthorized, "unauthenticated")

	// Audit konsumen dan log tanpa nomor HP.
	if n := countRows(t, e.db, `SELECT count(*) FROM audit_logs WHERE actor_type = 'CONSUMER' AND action = ?`, service.AuditConsumerLogin); n != 2 {
		t.Fatalf("audit login konsumen %d", n)
	}
	if n := countRows(t, e.db, `SELECT count(*) FROM audit_logs WHERE after::text LIKE ?`, "%"+phoneDigits+"%"); n != 0 {
		t.Fatal("audit memuat nomor HP")
	}
	if n := countRows(t, e.db, `SELECT count(*) FROM otp_challenges WHERE consumed_at IS NULL`); n != 0 {
		t.Fatalf("%d challenge masih aktif setelah dipakai", n)
	}
	if strings.Contains(e.logs.String(), phoneDigits) {
		t.Fatalf("log memuat nomor HP:\n%s", e.logs.String())
	}
}

func TestOTP_TooManyAttemptsPerCode(t *testing.T) {
	e := newEnv(t)
	if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
		t.Fatal(r.Code)
	}
	for i := 0; i < 4; i++ {
		expectProblem(t, e.verifyOTP(testPhone, "111111"), http.StatusUnauthorized, "otp_invalid")
	}
	// Percobaan ke-5 salah → jatah habis.
	expectProblem(t, e.verifyOTP(testPhone, "111111"), http.StatusTooManyRequests, "too_many_attempts")
	// Kode benar setelah jatah habis tetap ditolak; harus minta kode baru.
	expectProblem(t, e.verifyOTP(testPhone, config.DemoOTPCode), http.StatusTooManyRequests, "too_many_attempts")
	if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
		t.Fatal(r.Code)
	}
	if r := e.verifyOTP(testPhone, config.DemoOTPCode); r.Code != http.StatusOK {
		t.Fatalf("kode baru ditolak: %d %s", r.Code, r.Body)
	}
}

func TestOTP_ConcurrentAttemptsNeverExceedMax(t *testing.T) {
	e := newEnv(t)
	if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
		t.Fatal(r.Code)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code := "999999"
			r := e.verifyOTP(testPhone, code)
			mu.Lock()
			statuses[r.Code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if statuses[http.StatusUnauthorized]+statuses[http.StatusTooManyRequests] != 20 || statuses[http.StatusUnauthorized] > 5 {
		t.Fatalf("status: %v", statuses)
	}
	if n := countRows(t, e.db, `SELECT max(attempts) FROM otp_challenges`); n != 5 {
		t.Fatalf("attempts %d, ingin tepat 5", n)
	}
}

func TestOTP_ConcurrentCorrectCodeOneSession(t *testing.T) {
	e := newEnv(t)
	if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
		t.Fatal(r.Code)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.verifyOTP(testPhone, config.DemoOTPCode)
			if r.Code == http.StatusOK {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 1 {
		t.Fatalf("%d verifikasi sukses untuk satu kode, ingin 1", okCount)
	}
	if n := countRows(t, e.db, `SELECT count(*) FROM consumer_sessions`); n != 1 {
		t.Fatalf("%d sesi konsumen", n)
	}
}

func TestOTP_RequestLimitPerPhoneAndExpiry(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
			t.Fatalf("request ke-%d: %d", i+1, r.Code)
		}
		e.clock.Advance(time.Minute)
	}
	rec := e.requestOTP("+6281234567890") // nomor sama, format lain
	p := expectProblem(t, rec, http.StatusTooManyRequests, "too_many_attempts")
	// Jendela 15 menit dari permintaan pertama (t0), sekarang t0+5m → 10 menit.
	if !strings.Contains(p.Detail, "10 menit") || rec.Header().Get("Retry-After") != "600" {
		t.Fatalf("detail %q retry %q", p.Detail, rec.Header().Get("Retry-After"))
	}
	// Nomor lain tidak terpengaruh.
	if r := e.requestOTP("085711112222"); r.Code != http.StatusAccepted {
		t.Fatalf("nomor lain: %d", r.Code)
	}
	// Hanya kode terbaru yang aktif; kode kedaluwarsa setelah 5 menit.
	if n := countRows(t, e.db, `SELECT count(*) FROM otp_challenges WHERE consumed_at IS NULL AND phone_bidx = (SELECT phone_bidx FROM otp_challenges ORDER BY created_at LIMIT 1)`); n != 1 {
		t.Fatalf("%d challenge aktif untuk satu nomor", n)
	}
	e.clock.Advance(5 * time.Minute)
	expectProblem(t, e.verifyOTP(testPhone, config.DemoOTPCode), http.StatusUnauthorized, "otp_invalid")
	// Jendela lewat → boleh minta lagi.
	e.clock.Advance(10 * time.Minute)
	if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
		t.Fatalf("setelah jendela: %d %s", r.Code, r.Body)
	}
}

func TestOTP_Validation(t *testing.T) {
	e := newEnv(t)
	for _, phone := range []string{"", "12345", "0212345678", "+6581234567", "08１２34567890", strings.Repeat("8", 33)} {
		rec := e.requestOTP(phone)
		p := expectProblem(t, rec, http.StatusUnprocessableEntity, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "phone" {
			t.Fatalf("%q: errors %+v", phone, p.Errors)
		}
	}
	for _, code := range []string{"12345", "1234567", "12a456", "１２３４５６", ""} {
		p := expectProblem(t, e.verifyOTP(testPhone, code), http.StatusUnprocessableEntity, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "code" {
			t.Fatalf("%q: errors %+v", code, p.Errors)
		}
	}
	// Verifikasi tanpa pernah meminta kode.
	expectProblem(t, e.verifyOTP(testPhone, config.DemoOTPCode), http.StatusUnauthorized, "otp_invalid")
	// CSRF juga berlaku tanpa cookie.
	expectProblem(t, e.requestOTP(testPhone, withHeader("Origin", "https://evil.test")), http.StatusForbidden, "csrf_rejected")
	expectProblem(t, e.requestOTP(testPhone, withHeader("Content-Type", "text/plain")),
		http.StatusUnsupportedMediaType, "unsupported_media_type")
}

func TestOTP_RequestLimitPerIP(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 20; i++ {
		phone := "08571111" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "00"
		if r := e.requestOTP(phone, fromIP("192.0.2.50")); r.Code != http.StatusAccepted {
			t.Fatalf("request ke-%d: %d %s", i+1, r.Code, r.Body)
		}
	}
	expectProblem(t, e.requestOTP("081299998888", fromIP("192.0.2.50")), http.StatusTooManyRequests, "too_many_attempts")
	if r := e.requestOTP("081299998888", fromIP("192.0.2.51")); r.Code != http.StatusAccepted {
		t.Fatalf("IP lain: %d", r.Code)
	}
}

func TestOTP_DisabledWithoutMockCode(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OTPMockCode = "" })
	expectProblem(t, e.requestOTP(testPhone), http.StatusServiceUnavailable, "otp_unavailable")
	expectProblem(t, e.verifyOTP(testPhone, config.DemoOTPCode), http.StatusServiceUnavailable, "otp_unavailable")
}

func TestOTP_BlockedConsumerRejected(t *testing.T) {
	e := newEnv(t)
	if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
		t.Fatal(r.Code)
	}
	if r := e.verifyOTP(testPhone, config.DemoOTPCode); r.Code != http.StatusOK {
		t.Fatal(r.Code)
	}
	if err := e.db.Exec(`UPDATE consumers SET status = 'BLOCKED'`).Error; err != nil {
		t.Fatal(err)
	}
	if r := e.requestOTP(testPhone); r.Code != http.StatusAccepted {
		t.Fatal(r.Code) // respons request tidak membocorkan status akun
	}
	expectProblem(t, e.verifyOTP(testPhone, config.DemoOTPCode), http.StatusForbidden, "consumer_blocked")
}
