package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/repository"
)

// Path yang memakai sesi konsumen. Di luar prefix ini hanya sesi staff
// yang dibaca; di dalamnya hanya sesi konsumen (staff tidak butuh identitas
// staff untuk endpoint publik).
const publicPathPrefix = "/api/v1/public/"

// Path yang harus tetap bisa dipakai walau cookie sesi staff sudah
// kedaluwarsa/dicabut (kalau tidak, user dengan cookie basi tidak bisa login
// ulang atau logout). Di path ini sesi tidak valid = anonim, bukan 401.
var staffCredentialOptionalPaths = map[string]struct{}{
	"/api/v1/auth/login":  {},
	"/api/v1/auth/logout": {},
}

// Nilai status aktif (text + CHECK) di users, consumers, pos_devices.
const (
	userStatusActive     = "ACTIVE"
	consumerStatusActive = "ACTIVE"
	deviceStatusActive   = "ACTIVE"
)

// maxDeviceTokenLen membatasi token Bearer sebelum di-hash.
const maxDeviceTokenLen = 512

// CashierHeader: id kasir yang sedang login di device POS (opsional).
const CashierHeader = "X-Cashier-Id"

// DevicePermission: satu-satunya permission principal device.
const DevicePermission = "pos.sync"

type staffSessionStore interface {
	StaffSessionByHash(ctx context.Context, tokenHash []byte) (*repository.StaffSession, error)
	TouchSession(ctx context.Context, id uuid.UUID, now, idleExpiresAt time.Time) error
	DeviceByTokenHash(ctx context.Context, tokenHash []byte) (*repository.DeviceAuth, error)
	CashierAllowedAtLocation(ctx context.Context, userID, locationID uuid.UUID) (bool, error)
}

type consumerSessionStore interface {
	ConsumerSessionByHash(ctx context.Context, tokenHash []byte) (*repository.ConsumerSessionAuth, error)
	TouchConsumerSession(ctx context.Context, id uuid.UUID, now, idleExpiresAt time.Time) error
}

// AuthResolver mengimplementasikan auth.Resolver: token Bearer device POS,
// cookie sesi staff, dan cookie sesi konsumen.
type AuthResolver struct {
	staff    staffSessionStore
	consumer consumerSessionStore
	clock    clock.Clock
	csrf     *CSRFGuard
	log      *slog.Logger
}

var _ auth.Resolver = (*AuthResolver)(nil)

func NewAuthResolver(staff staffSessionStore, consumer consumerSessionStore, c clock.Clock, csrf *CSRFGuard, log *slog.Logger) *AuthResolver {
	return &AuthResolver{staff: staff, consumer: consumer, clock: c, csrf: csrf, log: log}
}

// Resolve: (nil, nil) tanpa kredensial; error untuk kredensial yang ada
// tetapi tidak valid, CSRF yang gagal, atau kegagalan sistem.
func (a *AuthResolver) Resolve(ctx context.Context, r *http.Request) (*auth.Principal, error) {
	// Bearer didahulukan dan tidak terkena CSRF (tidak dikirim otomatis browser).
	if h := r.Header.Get("Authorization"); h != "" {
		return a.resolveDevice(ctx, r, h)
	}

	if strings.HasPrefix(r.URL.Path, publicPathPrefix) {
		c, err := r.Cookie(ConsumerSessionCookie)
		if err != nil || c.Value == "" {
			return nil, nil
		}
		if err := a.csrf.Check(r); err != nil {
			return nil, err
		}
		p, err := a.resolveConsumer(ctx, c.Value)
		// Endpoint publik sebagian besar boleh anonim (verifikasi kode), jadi
		// sesi konsumen basi = anonim. Endpoint yang butuh konsumen menolak
		// lewat RequireKind (401 unauthenticated).
		if errors.Is(err, errSessionInvalid) {
			return nil, nil
		}
		return p, err
	}

	c, err := r.Cookie(StaffSessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	if err := a.csrf.Check(r); err != nil {
		return nil, err
	}
	p, err := a.resolveStaff(ctx, c.Value)
	if errors.Is(err, errSessionInvalid) {
		if _, ok := staffCredentialOptionalPaths[r.URL.Path]; ok {
			return nil, nil
		}
		return nil, ErrSessionExpired
	}
	return p, err
}

func (a *AuthResolver) resolveStaff(ctx context.Context, token string) (*auth.Principal, error) {
	hash, ok := hashSessionToken(token)
	if !ok {
		return nil, errSessionInvalid
	}
	s, err := a.staff.StaffSessionByHash(ctx, hash[:])
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errSessionInvalid
	}
	now := a.clock.Now()
	if s.RevokedAt != nil || !now.Before(s.IdleExpiresAt) || !now.Before(s.ExpiresAt) ||
		s.UserStatus != userStatusActive || s.UserArchivedAt != nil {
		return nil, errSessionInvalid
	}
	if now.Sub(s.LastSeenAt) >= SessionTouchInterval {
		idle := minTime(now.Add(StaffIdleTimeout), s.ExpiresAt)
		if err := a.staff.TouchSession(ctx, s.SessionID, now, idle); err != nil {
			// Gagal memperpanjang tidak menggagalkan request; sesi masih sah.
			a.log.WarnContext(ctx, "memperpanjang sesi staff gagal", "error", err)
		}
	}

	perms, scopes, err := parsePermissionGrants(s.PermissionGrants)
	if err != nil {
		return nil, err
	}
	p := &auth.Principal{
		Kind:             auth.KindStaff,
		ID:               s.UserID,
		ActorID:          s.UserID,
		Permissions:      perms,
		ScopeAll:         s.ScopeAll,
		PermissionScopes: scopes,
	}
	if !s.ScopeAll && len(s.LocationIDs) > 0 {
		p.LocationIDs = make([]uuid.UUID, 0, len(s.LocationIDs))
		for _, raw := range s.LocationIDs {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, err
			}
			p.LocationIDs = append(p.LocationIDs, id)
		}
	}
	return p, nil
}

func (a *AuthResolver) resolveDevice(ctx context.Context, r *http.Request, header string) (*auth.Principal, error) {
	scheme, token, ok := strings.Cut(header, " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > maxDeviceTokenLen {
		return nil, ErrInvalidToken
	}
	hash := hashDeviceToken(token)
	d, err := a.staff.DeviceByTokenHash(ctx, hash[:])
	if err != nil {
		return nil, err
	}
	if d == nil || d.Status != deviceStatusActive || d.RevokedAt != nil {
		return nil, ErrInvalidToken
	}

	p := &auth.Principal{
		Kind:        auth.KindDevice,
		ID:          d.ID,
		Permissions: map[string]struct{}{DevicePermission: {}},
		LocationIDs: []uuid.UUID{d.LocationID},
		// pos.sync hanya di lokasi device itu.
		PermissionScopes: map[string]auth.PermScope{
			DevicePermission: {LocationIDs: []uuid.UUID{d.LocationID}},
		},
	}
	if raw := r.Header.Get(CashierHeader); raw != "" {
		cashierID, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil || cashierID == uuid.Nil {
			return nil, ErrCashierNotAllowed
		}
		allowed, err := a.staff.CashierAllowedAtLocation(ctx, cashierID, d.LocationID)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, ErrCashierNotAllowed
		}
		p.ActorID = cashierID
	}
	return p, nil
}

func (a *AuthResolver) resolveConsumer(ctx context.Context, token string) (*auth.Principal, error) {
	hash, ok := hashSessionToken(token)
	if !ok {
		return nil, errSessionInvalid
	}
	s, err := a.consumer.ConsumerSessionByHash(ctx, hash[:])
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errSessionInvalid
	}
	now := a.clock.Now()
	if s.RevokedAt != nil || !now.Before(s.IdleExpiresAt) || !now.Before(s.ExpiresAt) ||
		s.ConsumerStatus != consumerStatusActive {
		return nil, errSessionInvalid
	}
	if now.Sub(s.LastSeenAt) >= SessionTouchInterval {
		idle := minTime(now.Add(ConsumerIdleTimeout), s.ExpiresAt)
		if err := a.consumer.TouchConsumerSession(ctx, s.SessionID, now, idle); err != nil {
			a.log.WarnContext(ctx, "memperpanjang sesi konsumen gagal", "error", err)
		}
	}
	// ActorID dibiarkan uuid.Nil: ActorID selalu users.id; id konsumen ada di ID.
	return &auth.Principal{
		Kind:             auth.KindConsumer,
		ID:               s.ConsumerID,
		Permissions:      map[string]struct{}{},
		PermissionScopes: map[string]auth.PermScope{},
	}, nil
}

// parsePermissionGrants mengubah entri "<perm>@<cakupan>" menjadi daftar
// permission gabungan (kontrak lama) dan scope per permission (untuk CanAt).
func parsePermissionGrants(grants []string) (map[string]struct{}, map[string]auth.PermScope, error) {
	perms := make(map[string]struct{}, len(grants))
	scopes := make(map[string]auth.PermScope, len(grants))
	for _, g := range grants {
		perm, target, ok := strings.Cut(g, "@")
		if !ok || perm == "" {
			return nil, nil, fmt.Errorf("entri permission tidak dikenal: %q", g)
		}
		perms[perm] = struct{}{}
		sc := scopes[perm]
		switch target {
		case "*":
			sc.All = true
		case "":
			// role SELECTED tanpa lokasi: permission ada, lokasi tidak ada.
		default:
			id, err := uuid.Parse(target)
			if err != nil {
				return nil, nil, fmt.Errorf("lokasi permission tidak valid: %w", err)
			}
			sc.LocationIDs = append(sc.LocationIDs, id)
		}
		scopes[perm] = sc
	}
	return perms, scopes, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
