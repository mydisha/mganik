package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/pii"
	"mganik/api/internal/repository"
)

// Error login yang dikirim ke klien. Pesan gagal sengaja sama untuk email
// tidak dikenal maupun password salah.
var (
	ErrInvalidCredentials = apperror.New(http.StatusUnauthorized, "invalid_credentials",
		"Gagal masuk", "Email atau password salah.")
	ErrAccountDisabled = apperror.New(http.StatusForbidden, "account_disabled",
		"Akun nonaktif", "Akun ini dinonaktifkan. Hubungi admin.")
)

// Aksi audit IAM (audit_logs.action).
const (
	AuditLoginSucceeded = "auth.login_succeeded"
	AuditLoginFailed    = "auth.login_failed"
	AuditLoginThrottled = "auth.login_throttled"
	AuditLogout         = "auth.logout"
	AuditConsumerLogin  = "consumer.login_succeeded"
	AuditConsumerLogout = "consumer.logout"
)

// LoginPolicy: batas percobaan login gagal (in-memory, per proses).
type LoginPolicy struct {
	EmailFailLimit int
	IPFailLimit    int
	FailWindow     time.Duration
	// MaxTrackedKeys membatasi memori limiter.
	MaxTrackedKeys int
	// PasswordParams untuk hash dummy (email tidak dikenal) supaya waktu
	// verifikasi sama dengan user sungguhan.
	PasswordParams domain.Argon2Params
	// VerifyConcurrency: verifikasi argon2 bersamaan maksimal (semaphore
	// global per proses). VerifyWait: lama menunggu slot sebelum 503.
	VerifyConcurrency int
	VerifyWait        time.Duration
}

// DefaultLoginPolicy: 5 percobaan gagal per email dan 20 per IP dalam 15
// menit; argon2 bersamaan maksimal 2 x GOMAXPROCS.
func DefaultLoginPolicy() LoginPolicy {
	return LoginPolicy{
		EmailFailLimit:    5,
		IPFailLimit:       20,
		FailWindow:        15 * time.Minute,
		MaxTrackedKeys:    100_000,
		PasswordParams:    domain.OWASPArgon2id,
		VerifyConcurrency: runtime.GOMAXPROCS(0) * 2,
		VerifyWait:        2 * time.Second,
	}
}

type authStore interface {
	FindLoginUser(ctx context.Context, email string) (*repository.LoginUser, error)
	CreateSession(tx *gorm.DB, s *model.Session) error
	RevokeSessionByHash(tx *gorm.DB, tokenHash []byte, now time.Time) (*repository.RevokedSession, error)
	MarkLoginSucceeded(tx *gorm.DB, userID uuid.UUID, now time.Time) error
	InsertAudit(tx *gorm.DB, a *model.AuditLog) error
	FindUserProfile(ctx context.Context, userID uuid.UUID) (*repository.UserProfile, error)
	ListUserRoles(ctx context.Context, userID uuid.UUID) ([]repository.UserRoleLocationRow, error)
	ListUserPermissions(ctx context.Context, userID uuid.UUID) ([]string, error)
}

// AuthService: login, logout, dan profil staff.
type AuthService struct {
	db     *gorm.DB
	store  authStore
	clock  clock.Clock
	keys   *pii.Keys
	log    *slog.Logger
	policy LoginPolicy
	byMail *attemptLimiter
	byIP   *attemptLimiter
	// verifySem membatasi verifikasi argon2 bersamaan.
	verifySem chan struct{}

	dummyOnce sync.Once
	dummyHash string
}

func NewAuthService(db *gorm.DB, store authStore, c clock.Clock, keys *pii.Keys, log *slog.Logger, policy LoginPolicy) *AuthService {
	if policy.VerifyConcurrency < 1 {
		policy.VerifyConcurrency = 1
	}
	return &AuthService{
		db: db, store: store, clock: c, keys: keys, log: log, policy: policy,
		byMail:    newAttemptLimiter(c, policy.EmailFailLimit, policy.FailWindow, policy.MaxTrackedKeys),
		byIP:      newAttemptLimiter(c, policy.IPFailLimit, policy.FailWindow, policy.MaxTrackedKeys),
		verifySem: make(chan struct{}, policy.VerifyConcurrency),
	}
}

// RequestMeta: konteks request untuk audit (tanpa data rahasia).
type RequestMeta struct {
	IP        string
	UserAgent string
	RequestID string
}

// LoginInput: kredensial plus token sesi lama (kalau ada) yang dicabut saat
// login berhasil, supaya tidak ada sesi ganda dari browser yang sama.
type LoginInput struct {
	Email         string
	Password      string
	PreviousToken string
	Meta          RequestMeta
}

// LoginResult: token hanya dikirim sebagai cookie, tidak pernah di body.
type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	UserID    uuid.UUID
}

func (s *AuthService) dummyPasswordHash() string {
	s.dummyOnce.Do(func() {
		h, err := domain.HashPassword("dummy-password-penyeimbang-waktu", s.policy.PasswordParams)
		if err != nil {
			// crypto/rand gagal: biarkan kosong, VerifyPassword gagal cepat.
			s.log.Error("membuat hash dummy gagal", "error", err)
		}
		s.dummyHash = h
	})
	return s.dummyHash
}

// Login memverifikasi email + password dan membuat sesi baru.
func (s *AuthService) Login(ctx context.Context, in LoginInput) (LoginResult, error) {
	email := strings.TrimSpace(in.Email)
	if email == "" {
		return LoginResult{}, apperror.Validation(apperror.FieldError{Field: "email", Code: "required", Message: "wajib diisi"})
	}
	mailKey := strings.ToLower(email)
	ipKey := in.Meta.IP
	// Jatah percobaan dipesan atomik SEBELUM argon2: burst paralel tidak bisa
	// melewati limit (cek dan penambahan counter di bawah satu lock).
	if r := s.byMail.Reserve(mailKey); !r.OK {
		s.auditThrottled(ctx, r, "email", email, in.Meta)
		return LoginResult{}, tooManyAttempts(r.RetryAfter, "percobaan masuk")
	}
	if r := s.byIP.Reserve(ipKey); !r.OK {
		s.byMail.Release(mailKey) // password tidak diverifikasi
		s.auditThrottled(ctx, r, "ip", "", in.Meta)
		return LoginResult{}, tooManyAttempts(r.RetryAfter, "percobaan masuk")
	}
	// releaseBoth: batal sebelum verifikasi password, jatah dikembalikan.
	releaseBoth := func() {
		s.byMail.Release(mailKey)
		s.byIP.Release(ipKey)
	}

	u, err := s.store.FindLoginUser(ctx, email)
	if err != nil {
		releaseBoth()
		return LoginResult{}, err
	}
	// Email tidak dikenal tetap menjalankan argon2id terhadap hash dummy
	// supaya waktu respons tidak membedakan keduanya.
	hash := s.dummyPasswordHash()
	if u != nil {
		hash = u.PasswordHash
	}
	if err := s.acquireVerifySlot(ctx); err != nil {
		releaseBoth()
		return LoginResult{}, err
	}
	ok, verr := domain.VerifyPassword(in.Password, hash)
	<-s.verifySem
	if verr != nil && u != nil {
		s.log.ErrorContext(ctx, "hash password user rusak", "user_id", u.ID, "error", verr)
	}
	now := s.clock.Now()
	if u == nil || !ok || verr != nil {
		var uid *uuid.UUID
		if u != nil {
			uid = &u.ID
		}
		if err := s.audit(s.db.WithContext(ctx), AuditLoginFailed, uid, "user", uid, "invalid_credentials", in.Meta, now); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, ErrInvalidCredentials
	}
	// Status baru diungkap setelah password terbukti benar.
	if u.Status != userStatusActive || (u.LockedUntil != nil && now.Before(*u.LockedUntil)) {
		// Password benar: bukan tebakan dari IP ini; jatah email tetap terpakai.
		s.byIP.Release(ipKey)
		if err := s.audit(s.db.WithContext(ctx), AuditLoginFailed, &u.ID, "user", &u.ID, "account_disabled", in.Meta, now); err != nil {
			return LoginResult{}, err
		}
		return LoginResult{}, ErrAccountDisabled
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	sess := &model.Session{
		ID:            uuid.Must(uuid.NewV7()),
		UserID:        u.ID,
		TokenHash:     tokenHash,
		IdleExpiresAt: now.Add(StaffIdleTimeout),
		ExpiresAt:     now.Add(StaffAbsoluteTimeout),
		LastSeenAt:    now,
		IPHash:        s.keys.HashIP(in.Meta.IP),
		UserAgent:     truncatedUA(in.Meta.UserAgent),
		CreatedAt:     now,
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if old, ok := hashSessionToken(in.PreviousToken); ok {
			if _, err := s.store.RevokeSessionByHash(tx, old[:], now); err != nil {
				return err
			}
		}
		if err := s.store.CreateSession(tx, sess); err != nil {
			return err
		}
		if err := s.store.MarkLoginSucceeded(tx, u.ID, now); err != nil {
			return err
		}
		return s.audit(tx, AuditLoginSucceeded, &u.ID, "session", &sess.ID, "", in.Meta, now)
	})
	if err != nil {
		return LoginResult{}, err
	}
	// Sukses: counter email direset, dan percobaan ini tidak memakan jatah
	// IP (kantor di balik satu NAT tetap bisa login banyak user).
	s.byMail.Reset(mailKey)
	s.byIP.Release(ipKey)
	return LoginResult{Token: token, ExpiresAt: sess.ExpiresAt, UserID: u.ID}, nil
}

// acquireVerifySlot mengambil slot semaphore argon2. Kalau penuh lebih lama
// dari VerifyWait, request ditolak 503 server_busy (tidak menunggu tanpa
// batas): argon2id memakan ~19 MiB per verifikasi, jadi konkurensinya dibatasi.
func (s *AuthService) acquireVerifySlot(ctx context.Context) error {
	select {
	case s.verifySem <- struct{}{}:
		return nil
	default:
	}
	timer := time.NewTimer(s.policy.VerifyWait)
	defer timer.Stop()
	select {
	case s.verifySem <- struct{}{}:
		return nil
	case <-timer.C:
		return errServerBusy()
	case <-ctx.Done():
		return errServerBusy()
	}
}

func errServerBusy() error {
	return &RetryAfterError{
		Err: apperror.New(http.StatusServiceUnavailable, "server_busy", "Server sibuk",
			"Terlalu banyak permintaan masuk pada saat yang sama. Coba lagi dalam beberapa detik."),
		After: time.Second,
	}
}

// auditThrottled mencatat penolakan 429 sekali per kunci per jendela (bukan
// per request) supaya burst tidak membanjiri audit_logs. Gagal menulis audit
// hanya di-log; respons tetap 429.
func (s *AuthService) auditThrottled(ctx context.Context, r Reservation, scope, email string, meta RequestMeta) {
	if !r.FirstRejection {
		return
	}
	var uid *uuid.UUID
	if email != "" {
		if u, err := s.store.FindLoginUser(ctx, email); err == nil && u != nil {
			uid = &u.ID
		}
	}
	err := s.audit(s.db.WithContext(ctx), AuditLoginThrottled, uid, "user", uid, "too_many_attempts_"+scope, meta, s.clock.Now())
	if err != nil {
		s.log.ErrorContext(ctx, "audit login throttled gagal", "error", err)
	}
}

// Logout mencabut sesi milik token. Idempotent: token kosong, tidak dikenal,
// atau sudah dicabut tetap sukses (cookie tetap dihapus controller).
func (s *AuthService) Logout(ctx context.Context, token string, meta RequestMeta) error {
	hash, ok := hashSessionToken(token)
	if !ok {
		return nil
	}
	now := s.clock.Now()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rev, err := s.store.RevokeSessionByHash(tx, hash[:], now)
		if err != nil || rev == nil {
			return err
		}
		return s.audit(tx, AuditLogout, &rev.UserID, "session", &rev.ID, "", meta, now)
	})
}

// audit menulis audit IAM. Password dan token tidak pernah masuk; IP hanya
// sebagai prefix HMAC.
func (s *AuthService) audit(tx *gorm.DB, action string, actorID *uuid.UUID, entity string, entityID *uuid.UUID,
	reason string, meta RequestMeta, now time.Time) error {
	return insertAudit(tx, s.store, s.keys, model.ActorUser, action, actorID, entity, entityID, reason, meta, now)
}

type auditWriter interface {
	InsertAudit(tx *gorm.DB, a *model.AuditLog) error
}

func insertAudit(tx *gorm.DB, w auditWriter, keys *pii.Keys, actorType, action string, actorID *uuid.UUID,
	entity string, entityID *uuid.UUID, reason string, meta RequestMeta, now time.Time) error {
	after := map[string]string{"outcome": "succeeded"}
	if reason != "" {
		after = map[string]string{"outcome": "failed", "reason": reason}
	}
	a := &model.AuditLog{
		ID:        uuid.Must(uuid.NewV7()),
		ActorType: actorType,
		ActorID:   actorID,
		Action:    action,
		Entity:    entity,
		EntityID:  entityID,
		After:     model.MustJSON(after),
		IPHash:    keys.HashIP(meta.IP),
		At:        now,
	}
	if meta.RequestID != "" {
		a.RequestID = &meta.RequestID
	}
	return w.InsertAudit(tx, a)
}

// maxUserAgentLen membatasi user agent yang disimpan di sesi.
const maxUserAgentLen = 256

func truncatedUA(ua string) *string {
	if ua == "" {
		return nil
	}
	if len(ua) > maxUserAgentLen {
		ua = strings.ToValidUTF8(ua[:maxUserAgentLen], "")
	}
	return &ua
}

// ------------------------------------------------------------------ profil

// Location adalah referensi lokasi untuk profil.
type Location struct {
	ID   uuid.UUID
	Code string
	Name string
}

// UserRole adalah role user beserta scope-nya.
type UserRole struct {
	Code      string
	Name      string
	ScopeMode string
	Locations []Location
}

// Me adalah profil staff yang sedang login.
type Me struct {
	ID          uuid.UUID
	Email       string
	Name        string
	Roles       []UserRole
	Permissions []string
	// ScopeAll dari scope_mode eksplisit; kalau false, Locations adalah
	// satu-satunya lokasi yang boleh disentuh (bisa kosong = tidak ada).
	ScopeAll  bool
	Locations []Location
}

var errUserGone = errors.New("user sesi tidak ditemukan")

// Me memuat profil, role, permission, dan scope user dalam tiga query.
func (s *AuthService) Me(ctx context.Context, userID uuid.UUID) (Me, error) {
	prof, err := s.store.FindUserProfile(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	if prof == nil {
		return Me{}, apperror.Wrap(errUserGone, http.StatusUnauthorized, ErrSessionExpired.Code, ErrSessionExpired.Title, ErrSessionExpired.Detail)
	}
	rows, err := s.store.ListUserRoles(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	perms, err := s.store.ListUserPermissions(ctx, userID)
	if err != nil {
		return Me{}, err
	}
	me := Me{ID: prof.ID, Email: prof.Email, Name: prof.Name, Permissions: perms, Roles: []UserRole{}, Locations: []Location{}}
	if me.Permissions == nil {
		me.Permissions = []string{}
	}
	seenLoc := map[uuid.UUID]bool{}
	for _, r := range rows {
		if n := len(me.Roles); n == 0 || me.Roles[n-1].Code != r.RoleCode {
			me.Roles = append(me.Roles, UserRole{Code: r.RoleCode, Name: r.RoleName, ScopeMode: r.ScopeMode, Locations: []Location{}})
		}
		if r.ScopeMode == model.ScopeAll {
			me.ScopeAll = true
		}
		if r.LocationID == nil {
			continue
		}
		loc := Location{ID: *r.LocationID, Code: deref(r.LocationCode), Name: deref(r.LocationName)}
		cur := &me.Roles[len(me.Roles)-1]
		cur.Locations = append(cur.Locations, loc)
		if !seenLoc[loc.ID] {
			seenLoc[loc.ID] = true
			me.Locations = append(me.Locations, loc)
		}
	}
	if me.ScopeAll {
		// Mode ALL: daftar lokasi tidak relevan; jangan dicampur.
		me.Locations = []Location{}
	}
	return me, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
