package iamtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"mganik/api/internal/apperror"
	"mganik/api/internal/config"
	"mganik/api/internal/domain"
	"mganik/api/internal/pii"
	"mganik/api/internal/repository"
	"mganik/api/internal/service"
)

// burst menembakkan n login bersamaan (dilepas serentak) dan menghitung status.
func (e *env) burst(n int, mk func(i int) (email, password string, opts []reqOpt)) map[int]int {
	e.t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	statuses := map[int]int{}
	start := make(chan struct{})
	for i := range n {
		email, password, opts := mk(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := e.login(email, password, opts...)
			mu.Lock()
			statuses[rec.Code]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	return statuses
}

// Regresi blocker QA #1: 40 login paralel ke satu email (39 salah + 1 benar
// di akhir) → paling banyak 5 password yang diverifikasi; sisanya 429.
func TestLogin_ParallelBurstOneEmailCapped(t *testing.T) {
	e := newEnv(t)
	statuses := e.burst(40, func(i int) (string, string, []reqOpt) {
		pw := fmt.Sprintf("tebakan-%02d", i)
		if i == 39 {
			pw = demoPassword
		}
		// IP berbeda-beda: yang diuji limit per email.
		return "ops@mganik.test", pw, []reqOpt{fromIP(fmt.Sprintf("198.51.100.%d", i+1))}
	})
	verified := statuses[http.StatusUnauthorized] + statuses[http.StatusOK]
	if verified > 5 {
		t.Fatalf("%d password diverifikasi (limit 5): %v", verified, statuses)
	}
	if statuses[http.StatusTooManyRequests]+statuses[http.StatusServiceUnavailable]+verified != 40 {
		t.Fatalf("status tak terduga: %v", statuses)
	}
	if statuses[http.StatusTooManyRequests] < 35 {
		t.Fatalf("ingin >= 35 penolakan 429: %v", statuses)
	}
	// Setelah burst, tebakan benar pun tetap 429 (kecuali sudah sukses di dalam burst).
	if statuses[http.StatusOK] == 0 {
		expectProblem(t, e.login("ops@mganik.test", demoPassword, fromIP("198.51.100.200")), http.StatusTooManyRequests, "too_many_attempts")
	}
	// Penolakan diaudit sekali per kunci per jendela, bukan per request.
	if n := countRows(t, e.db, `SELECT count(*) FROM audit_logs WHERE action = ? AND after->>'reason' = 'too_many_attempts_email'`,
		service.AuditLoginThrottled); n != 1 {
		t.Fatalf("audit throttled email %d baris, ingin 1", n)
	}
	var actor *string
	if err := e.db.Raw(`SELECT actor_id::text FROM audit_logs WHERE action = ?`, service.AuditLoginThrottled).Scan(&actor).Error; err != nil {
		t.Fatal(err)
	}
	if actor == nil {
		t.Fatal("audit throttled email tanpa actor_id user yang dituju")
	}
}

// Burst lintas email dari satu IP → paling banyak 20 verifikasi.
func TestLogin_ParallelBurstOneIPCapped(t *testing.T) {
	e := newEnv(t)
	statuses := e.burst(40, func(i int) (string, string, []reqOpt) {
		return fmt.Sprintf("acak%02d@mganik.test", i), "salah-salah", []reqOpt{fromIP("192.0.2.66")}
	})
	verified := statuses[http.StatusUnauthorized] + statuses[http.StatusOK]
	if verified > 20 {
		t.Fatalf("%d password diverifikasi (limit IP 20): %v", verified, statuses)
	}
	if statuses[http.StatusTooManyRequests] < 20 || statuses[http.StatusTooManyRequests]+statuses[http.StatusServiceUnavailable]+verified != 40 {
		t.Fatalf("status: %v", statuses)
	}
	if n := countRows(t, e.db, `SELECT count(*) FROM audit_logs WHERE action = ? AND after->>'reason' = 'too_many_attempts_ip'`,
		service.AuditLoginThrottled); n != 1 {
		t.Fatalf("audit throttled ip %d baris, ingin 1", n)
	}
	// IP lain tidak terdampak.
	decodeMe(t, e.login("admin@mganik.test", demoPassword, fromIP("192.0.2.67")))
}

// Login sukses tidak memakan jatah IP (banyak user di balik satu NAT).
func TestLogin_SuccessDoesNotConsumeIPBudget(t *testing.T) {
	e := newEnv(t)
	for range 25 {
		decodeMe(t, e.login("admin@mganik.test", demoPassword, fromIP("192.0.2.80")))
	}
}

// Semaphore argon2: slot penuh lebih lama dari VerifyWait → 503 server_busy,
// dan jatah percobaan yang dipesan dikembalikan (tidak ada verifikasi).
func TestLogin_VerifySemaphoreServerBusy(t *testing.T) {
	e := newEnv(t)
	keys, err := pii.New([]byte(testPIIKey))
	if err != nil {
		t.Fatal(err)
	}
	policy := service.DefaultLoginPolicy()
	policy.PasswordParams = domain.Argon2Params{MemoryKiB: 16 * 1024, Iterations: 4, Parallelism: 1, SaltLen: 16, KeyLen: 32}
	policy.VerifyConcurrency = 1
	policy.VerifyWait = time.Millisecond
	policy.EmailFailLimit = 1
	svc := service.NewAuthService(e.db, repository.NewAuthRepository(e.db), e.clock, keys,
		slog.New(slog.NewTextHandler(io.Discard, nil)), policy)

	var wg sync.WaitGroup
	errs := make([]error, 6)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Login(context.Background(), service.LoginInput{
				Email: fmt.Sprintf("sibuk%d@mganik.test", i), Password: "x", Meta: service.RequestMeta{IP: fmt.Sprintf("203.0.113.%d", i+1)},
			})
		}()
	}
	close(start)
	wg.Wait()
	busy := 0
	var busyIdx int
	for i, err := range errs {
		var ae *apperror.Error
		var ra *service.RetryAfterError
		switch {
		case errors.Is(err, service.ErrInvalidCredentials):
		case errors.As(err, &ae) && ae.Code == "server_busy" && errors.As(err, &ra) && ra.After == time.Second:
			busy++
			busyIdx = i
		default:
			t.Fatalf("error tak terduga: %v", err)
		}
	}
	if busy == 0 {
		t.Fatal("semaphore kapasitas 1 tidak pernah menolak dengan server_busy")
	}
	// Jatah email (limit 1) dikembalikan saat server_busy: percobaan ulang
	// diverifikasi, bukan 429.
	_, err = svc.Login(context.Background(), service.LoginInput{
		Email: fmt.Sprintf("sibuk%d@mganik.test", busyIdx), Password: "x", Meta: service.RequestMeta{IP: "203.0.113.99"},
	})
	if !errors.Is(err, service.ErrInvalidCredentials) {
		t.Fatalf("jatah tidak dikembalikan setelah server_busy: %v", err)
	}
}

// IP klien (#2): XFF hanya dipercaya dari proxy di TRUSTED_PROXIES.
func TestClientIP_TrustedProxyOnly(t *testing.T) {
	const proxy = "172.31.250.10"
	e := newEnv(t, func(c *config.Config) { c.TrustedProxies = []string{proxy} })
	keys, err := pii.New([]byte(testPIIKey))
	if err != nil {
		t.Fatal(err)
	}
	lastIPHash := func() []byte {
		// Struct, bukan []byte: GORM membaca []byte sebagai daftar baris.
		var row struct{ IPHash []byte }
		if err := e.db.Raw(`SELECT ip_hash FROM audit_logs ORDER BY at DESC, id DESC LIMIT 1`).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row.IPHash
	}
	// Proxy tepercaya (nginx, XFF ditimpa $remote_addr): XFF = IP asli.
	e.login("admin@mganik.test", "salah", fromIP(proxy), withHeader("X-Forwarded-For", "203.0.113.5"))
	if string(lastIPHash()) != string(keys.HashIP("203.0.113.5")) {
		t.Fatal("XFF dari proxy tepercaya tidak dipakai")
	}
	// Peer tidak tepercaya (mis. host lewat gateway docker ke port API): XFF diabaikan.
	e.login("admin@mganik.test", "salah", fromIP("172.31.250.1"), withHeader("X-Forwarded-For", "203.0.113.5"))
	if string(lastIPHash()) != string(keys.HashIP("172.31.250.1")) {
		t.Fatal("XFF palsu dari peer tidak tepercaya dipakai")
	}
	e.login("admin@mganik.test", "salah", fromIP("172.31.250.1"), withHeader("X-Real-IP", "203.0.113.6"))
	if string(lastIPHash()) != string(keys.HashIP("172.31.250.1")) {
		t.Fatal("X-Real-IP palsu dari peer tidak tepercaya dipakai")
	}

	// Limit per IP tidak bisa dilewati dengan memutar XFF dari peer tidak tepercaya.
	e2 := newEnv(t, func(c *config.Config) { c.TrustedProxies = []string{proxy} })
	for i := range 20 {
		e2.login(fmt.Sprintf("putar%02d@mganik.test", i), "salah", fromIP("172.31.250.1"),
			withHeader("X-Forwarded-For", fmt.Sprintf("198.18.0.%d", i+1)))
	}
	expectProblem(t, e2.login("putar99@mganik.test", "salah", fromIP("172.31.250.1"), withHeader("X-Forwarded-For", "198.18.1.1")),
		http.StatusTooManyRequests, "too_many_attempts")
}
