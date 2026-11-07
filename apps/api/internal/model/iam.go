package model

import (
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID               uuid.UUID  `gorm:"primaryKey;type:uuid"`
	PartyID          *uuid.UUID `gorm:"type:uuid"`
	Email            string     `gorm:"not null"`
	Name             string     `gorm:"not null"`
	PasswordHash     string     `gorm:"not null"`
	PosPINHash       *string    `gorm:"column:pos_pin_hash"`
	Status           string     `gorm:"not null;default:ACTIVE"`
	FailedLoginCount int        `gorm:"not null"`
	LockedUntil      *time.Time
	LastLoginAt      *time.Time
	ArchivedAt       *time.Time
	Version          int64 `gorm:"not null"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (User) TableName() string { return "users" }

// Session: token opaque, hanya hash SHA-256 yang disimpan.
type Session struct {
	ID            uuid.UUID `gorm:"primaryKey;type:uuid"`
	UserID        uuid.UUID `gorm:"type:uuid;not null"`
	TokenHash     []byte    `gorm:"not null"`
	IdleExpiresAt time.Time `gorm:"not null"`
	ExpiresAt     time.Time `gorm:"not null"`
	RevokedAt     *time.Time
	LastSeenAt    time.Time `gorm:"not null;default:now()"`
	IPHash        []byte    `gorm:"column:ip_hash"`
	UserAgent     *string
	CreatedAt     time.Time
}

func (Session) TableName() string { return "sessions" }

type Role struct {
	Code        string `gorm:"primaryKey"`
	Name        string `gorm:"not null"`
	Description *string
	CreatedAt   time.Time
}

func (Role) TableName() string { return "roles" }

type Permission struct {
	Code        string `gorm:"primaryKey"`
	Description string `gorm:"not null"`
	CreatedAt   time.Time
}

func (Permission) TableName() string { return "permissions" }

type RolePermission struct {
	RoleCode       string `gorm:"primaryKey"`
	PermissionCode string `gorm:"primaryKey"`
	CreatedAt      time.Time
}

func (RolePermission) TableName() string { return "role_permissions" }

// Nilai user_roles.scope_mode. SELECTED membaca user_role_locations; tabel anak
// yang kosong tidak pernah berarti ALL.
const (
	ScopeAll      = "ALL"
	ScopeSelected = "SELECTED"
)

type UserRole struct {
	ID        uuid.UUID  `gorm:"primaryKey;type:uuid"`
	UserID    uuid.UUID  `gorm:"type:uuid;not null"`
	RoleCode  string     `gorm:"not null"`
	ScopeMode string     `gorm:"not null"`
	GrantedBy *uuid.UUID `gorm:"type:uuid"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (UserRole) TableName() string { return "user_roles" }

type UserRoleLocation struct {
	UserRoleID uuid.UUID `gorm:"primaryKey;type:uuid"`
	LocationID uuid.UUID `gorm:"primaryKey;type:uuid"`
	CreatedAt  time.Time
}

func (UserRoleLocation) TableName() string { return "user_role_locations" }

// Consumer: nomor HP terenkripsi + blind index; tidak ada PII polos.
type Consumer struct {
	ID                 uuid.UUID `gorm:"primaryKey;type:uuid"`
	PhoneEnc           []byte    `gorm:"not null"`
	PhoneBidx          []byte    `gorm:"not null"`
	DisplayName        *string
	Status             string `gorm:"not null;default:ACTIVE"`
	ConsentMarketingAt *time.Time
	ConsentLocationAt  *time.Time
	AnonymizedAt       *time.Time
	Version            int64 `gorm:"not null"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (Consumer) TableName() string { return "consumers" }
