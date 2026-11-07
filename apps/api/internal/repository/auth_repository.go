package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// AuthRepository: akses data login staff, sesi, RBAC, device POS, dan audit.
// Metode tulis menerima tx dari service (satu use case = satu transaksi).
type AuthRepository struct {
	db *gorm.DB
}

func NewAuthRepository(db *gorm.DB) *AuthRepository {
	return &AuthRepository{db: db}
}

// LoginUser adalah kolom user yang dibutuhkan untuk memverifikasi login.
type LoginUser struct {
	ID           uuid.UUID
	PasswordHash string
	Status       string
	LockedUntil  *time.Time
}

// FindLoginUser mencari user aktif (belum diarsip) berdasarkan email tanpa
// membedakan huruf besar/kecil (memakai index users_email_unique).
// (nil, nil) kalau tidak ada.
func (r *AuthRepository) FindLoginUser(ctx context.Context, email string) (*LoginUser, error) {
	var u LoginUser
	res := r.db.WithContext(ctx).Raw(`
		SELECT id, password_hash, status, locked_until
		  FROM users
		 WHERE lower(email) = lower(?) AND archived_at IS NULL
		 LIMIT 1`, email).Scan(&u)
	if res.Error != nil {
		return nil, fmt.Errorf("mencari user login: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &u, nil
}

// CreateSession menyimpan sesi staff baru.
func (r *AuthRepository) CreateSession(tx *gorm.DB, s *model.Session) error {
	if err := tx.Create(s).Error; err != nil {
		return fmt.Errorf("membuat sesi: %w", err)
	}
	return nil
}

// RevokedSession adalah sesi yang baru saja dicabut.
type RevokedSession struct {
	ID     uuid.UUID
	UserID uuid.UUID
}

// RevokeSessionByHash mencabut sesi aktif ber-hash tokenHash. (nil, nil)
// kalau tidak ada sesi aktif dengan hash itu (idempotent).
func (r *AuthRepository) RevokeSessionByHash(tx *gorm.DB, tokenHash []byte, now time.Time) (*RevokedSession, error) {
	var out RevokedSession
	res := tx.Raw(`
		UPDATE sessions SET revoked_at = ?
		 WHERE token_hash = ? AND revoked_at IS NULL
		RETURNING id, user_id`, now, tokenHash).Scan(&out)
	if res.Error != nil {
		return nil, fmt.Errorf("mencabut sesi: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &out, nil
}

// MarkLoginSucceeded mencatat waktu login terakhir.
func (r *AuthRepository) MarkLoginSucceeded(tx *gorm.DB, userID uuid.UUID, now time.Time) error {
	err := tx.Exec(`UPDATE users SET last_login_at = ?, failed_login_count = 0, updated_at = ? WHERE id = ?`,
		now, now, userID).Error
	if err != nil {
		return fmt.Errorf("mencatat login: %w", err)
	}
	return nil
}

// InsertAudit menulis satu baris audit_logs (append-only).
func (r *AuthRepository) InsertAudit(tx *gorm.DB, a *model.AuditLog) error {
	if err := tx.Create(a).Error; err != nil {
		return fmt.Errorf("menulis audit: %w", err)
	}
	return nil
}

// StaffSession adalah sesi staff beserta status user dan hak aksesnya.
type StaffSession struct {
	SessionID      uuid.UUID
	UserID         uuid.UUID
	IdleExpiresAt  time.Time
	ExpiresAt      time.Time
	LastSeenAt     time.Time
	RevokedAt      *time.Time
	UserStatus     string
	UserArchivedAt *time.Time
	// PermissionGrants: satu entri per (permission, cakupan) dari pasangan
	// role→permission→scope, berformat "<perm>@*" (role ALL),
	// "<perm>@<location uuid>" (role SELECTED), atau "<perm>@" (role SELECTED
	// tanpa lokasi). Kode permission tidak pernah memuat '@' (CHECK format).
	PermissionGrants model.StringArray
	// ScopeAll true kalau minimal satu role ber-scope_mode ALL.
	ScopeAll bool
	// LocationIDs dari user_role_locations milik role ber-scope SELECTED.
	LocationIDs model.StringArray
}

// StaffSessionByHash memuat sesi, user, permission, dan scope lokasi dalam
// SATU query (subquery agregat, tanpa N+1). (nil, nil) kalau hash tidak ada.
// Scope dibaca dari user_roles.scope_mode; user_role_locations hanya dibaca
// untuk role SELECTED, dan daftar kosong tidak pernah berarti semua lokasi.
func (r *AuthRepository) StaffSessionByHash(ctx context.Context, tokenHash []byte) (*StaffSession, error) {
	var s StaffSession
	res := r.db.WithContext(ctx).Raw(`
		SELECT s.id AS session_id, s.user_id, s.idle_expires_at, s.expires_at, s.last_seen_at, s.revoked_at,
		       u.status AS user_status, u.archived_at AS user_archived_at,
		       COALESCE((SELECT array_agg(DISTINCT rp.permission_code || '@' ||
		                          CASE WHEN ur.scope_mode = 'ALL' THEN '*'
		                               ELSE COALESCE(url.location_id::text, '') END)
		                   FROM user_roles ur
		                   JOIN role_permissions rp ON rp.role_code = ur.role_code
		                   LEFT JOIN user_role_locations url
		                          ON url.user_role_id = ur.id AND ur.scope_mode = 'SELECTED'
		                  WHERE ur.user_id = s.user_id), '{}') AS permission_grants,
		       COALESCE((SELECT bool_or(ur.scope_mode = 'ALL') FROM user_roles ur WHERE ur.user_id = s.user_id),
		                false) AS scope_all,
		       COALESCE((SELECT array_agg(DISTINCT url.location_id::text)
		                   FROM user_roles ur
		                   JOIN user_role_locations url ON url.user_role_id = ur.id
		                  WHERE ur.user_id = s.user_id AND ur.scope_mode = 'SELECTED'), '{}') AS location_ids
		  FROM sessions s
		  JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ?`, tokenHash).Scan(&s)
	if res.Error != nil {
		return nil, fmt.Errorf("memuat sesi staff: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &s, nil
}

// TouchSession memperpanjang idle timeout sesi yang masih aktif.
func (r *AuthRepository) TouchSession(ctx context.Context, id uuid.UUID, now, idleExpiresAt time.Time) error {
	err := r.db.WithContext(ctx).Exec(`
		UPDATE sessions SET last_seen_at = ?, idle_expires_at = ?
		 WHERE id = ? AND revoked_at IS NULL`, now, idleExpiresAt, id).Error
	if err != nil {
		return fmt.Errorf("memperpanjang sesi: %w", err)
	}
	return nil
}

// UserProfile: identitas user untuk /auth/me.
type UserProfile struct {
	ID    uuid.UUID
	Email string
	Name  string
}

// FindUserProfile memuat profil user. (nil, nil) kalau tidak ada.
func (r *AuthRepository) FindUserProfile(ctx context.Context, userID uuid.UUID) (*UserProfile, error) {
	var p UserProfile
	res := r.db.WithContext(ctx).Raw(`SELECT id, email, name FROM users WHERE id = ?`, userID).Scan(&p)
	if res.Error != nil {
		return nil, fmt.Errorf("memuat profil user: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &p, nil
}

// UserRoleLocationRow: satu baris role user; lokasi diisi untuk role
// SELECTED (satu baris per lokasi), NULL untuk role ALL atau SELECTED tanpa
// lokasi.
type UserRoleLocationRow struct {
	RoleCode     string
	RoleName     string
	ScopeMode    string
	LocationID   *uuid.UUID
	LocationCode *string
	LocationName *string
}

// ListUserRoles memuat role user beserta lokasinya dalam satu query.
func (r *AuthRepository) ListUserRoles(ctx context.Context, userID uuid.UUID) ([]UserRoleLocationRow, error) {
	var rows []UserRoleLocationRow
	err := r.db.WithContext(ctx).Raw(`
		SELECT ur.role_code, ro.name AS role_name, ur.scope_mode,
		       l.id AS location_id, l.code AS location_code, l.name AS location_name
		  FROM user_roles ur
		  JOIN roles ro ON ro.code = ur.role_code
		  LEFT JOIN user_role_locations url ON url.user_role_id = ur.id AND ur.scope_mode = 'SELECTED'
		  LEFT JOIN locations l ON l.id = url.location_id
		 WHERE ur.user_id = ?
		 ORDER BY ur.role_code, l.code`, userID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("memuat role user: %w", err)
	}
	return rows, nil
}

// ListUserPermissions memuat permission gabungan semua role user (urut).
func (r *AuthRepository) ListUserPermissions(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var perms []string
	err := r.db.WithContext(ctx).Raw(`
		SELECT DISTINCT rp.permission_code
		  FROM user_roles ur
		  JOIN role_permissions rp ON rp.role_code = ur.role_code
		 WHERE ur.user_id = ?
		 ORDER BY rp.permission_code`, userID).Scan(&perms).Error
	if err != nil {
		return nil, fmt.Errorf("memuat permission user: %w", err)
	}
	return perms, nil
}

// DeviceAuth adalah kolom device POS untuk autentikasi token.
type DeviceAuth struct {
	ID         uuid.UUID
	LocationID uuid.UUID
	Status     string
	RevokedAt  *time.Time
}

// DeviceByTokenHash mencari device berdasarkan hash token. (nil, nil) kalau
// tidak ada.
func (r *AuthRepository) DeviceByTokenHash(ctx context.Context, tokenHash []byte) (*DeviceAuth, error) {
	var d DeviceAuth
	res := r.db.WithContext(ctx).Raw(`
		SELECT id, location_id, status, revoked_at FROM pos_devices WHERE token_hash = ?`, tokenHash).Scan(&d)
	if res.Error != nil {
		return nil, fmt.Errorf("memuat device: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &d, nil
}

// CashierAllowedAtLocation: user aktif punya role yang memberi pos.sync
// DAN role itu ber-scope ALL atau SELECTED yang memuat lokasi tersebut.
// Permission dan scope dicek pada role yang sama.
func (r *AuthRepository) CashierAllowedAtLocation(ctx context.Context, userID, locationID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.WithContext(ctx).Raw(`
		SELECT EXISTS (
		  SELECT 1
		    FROM users u
		    JOIN user_roles ur ON ur.user_id = u.id
		    JOIN role_permissions rp ON rp.role_code = ur.role_code AND rp.permission_code = 'pos.sync'
		   WHERE u.id = ? AND u.status = 'ACTIVE' AND u.archived_at IS NULL
		     AND (ur.scope_mode = 'ALL'
		          OR (ur.scope_mode = 'SELECTED' AND EXISTS (
		                SELECT 1 FROM user_role_locations url
		                 WHERE url.user_role_id = ur.id AND url.location_id = ?))))`,
		userID, locationID).Scan(&ok).Error
	if err != nil {
		return false, fmt.Errorf("memeriksa scope kasir: %w", err)
	}
	return ok, nil
}
