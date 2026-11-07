package model

import (
	"time"

	"github.com/google/uuid"
)

// ------------------------------------------------------------------ lookup

type PackagingLevel struct {
	Code      string `gorm:"primaryKey"`
	Name      string `gorm:"not null"`
	Rank      int16  `gorm:"not null"`
	CreatedAt time.Time
}

func (PackagingLevel) TableName() string { return "packaging_levels" }

type Channel struct {
	Code       string `gorm:"primaryKey"`
	Name       string `gorm:"not null"`
	Type       string `gorm:"not null"`
	Attributes JSON   `gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt  time.Time
}

func (Channel) TableName() string { return "channels" }

type LocationType struct {
	Code      string `gorm:"primaryKey"`
	Name      string `gorm:"not null"`
	IsOwn     bool   `gorm:"not null"`
	IsVirtual bool   `gorm:"not null"`
	CreatedAt time.Time
}

func (LocationType) TableName() string { return "location_types" }

// RoleType: peran party (DISTRIBUTOR, RETAILER, ...), bukan role RBAC staff.
type RoleType struct {
	Code      string `gorm:"primaryKey"`
	Name      string `gorm:"not null"`
	CreatedAt time.Time
}

func (RoleType) TableName() string { return "role_types" }

// ------------------------------------------------------------------ katalog

type Brand struct {
	ID         uuid.UUID `gorm:"primaryKey;type:uuid"`
	Code       string    `gorm:"not null"`
	Name       string    `gorm:"not null"`
	ArchivedAt *time.Time
	Version    int64 `gorm:"not null"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (Brand) TableName() string { return "brands" }

type Product struct {
	ID            uuid.UUID `gorm:"primaryKey;type:uuid"`
	BrandID       uuid.UUID `gorm:"type:uuid;not null"`
	SKU           string    `gorm:"column:sku;not null"`
	GTIN          *string   `gorm:"column:gtin"`
	Name          string    `gorm:"not null"`
	UOM           string    `gorm:"column:uom;not null;default:PCS"`
	ShelfLifeDays *int
	Attributes    JSON `gorm:"type:jsonb;not null;default:'{}'"`
	ArchivedAt    *time.Time
	Version       int64 `gorm:"not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (Product) TableName() string { return "products" }

type PackagingSpec struct {
	ID              uuid.UUID `gorm:"primaryKey;type:uuid"`
	ProductID       uuid.UUID `gorm:"type:uuid;not null"`
	ChildLevel      string    `gorm:"not null"`
	ParentLevel     string    `gorm:"not null"`
	QtyPerParent    int       `gorm:"not null"`
	AllowMixedBatch bool      `gorm:"not null"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (PackagingSpec) TableName() string { return "packaging_specs" }

// ------------------------------------------------------------------ wilayah

// Region memakai kode wilayah Kemendagri sebagai PK (misal "35.78").
type Region struct {
	Code        string `gorm:"primaryKey"`
	Name        string `gorm:"not null"`
	Level       string `gorm:"not null"`
	ParentCode  *string
	CentroidLat *float64
	CentroidLng *float64
	CreatedAt   time.Time
}

func (Region) TableName() string { return "regions" }

// ------------------------------------------------------------------ party

type Party struct {
	ID         uuid.UUID `gorm:"primaryKey;type:uuid"`
	Kind       string    `gorm:"not null"`
	Name       string    `gorm:"not null"`
	LegalID    *string
	Attributes JSON `gorm:"type:jsonb;not null;default:'{}'"`
	ArchivedAt *time.Time
	Version    int64 `gorm:"not null"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (Party) TableName() string { return "parties" }

type PartyRole struct {
	ID        uuid.UUID `gorm:"primaryKey;type:uuid"`
	PartyID   uuid.UUID `gorm:"type:uuid;not null"`
	RoleCode  string    `gorm:"not null"`
	ValidFrom time.Time `gorm:"not null;default:now()"`
	ValidTo   *time.Time
	CreatedAt time.Time
}

func (PartyRole) TableName() string { return "party_roles" }

// ------------------------------------------------------------------ lokasi

// Nilai locations.sync_state.
const (
	SyncNotTracked = "NOT_TRACKED"
	SyncOnline     = "ONLINE"
	SyncOffline    = "OFFLINE"
)

type Location struct {
	ID                       uuid.UUID  `gorm:"primaryKey;type:uuid"`
	Code                     string     `gorm:"not null"`
	Name                     string     `gorm:"not null"`
	TypeCode                 string     `gorm:"not null"`
	ParentID                 *uuid.UUID `gorm:"type:uuid"`
	PartyID                  *uuid.UUID `gorm:"type:uuid"`
	RegionCode               *string
	Address                  *string
	Lat                      *float64
	Lng                      *float64
	Timezone                 string `gorm:"not null;default:Asia/Jakarta"`
	OnlineFulfillmentEnabled bool   `gorm:"not null"`
	SyncState                string `gorm:"not null;default:NOT_TRACKED"`
	LastHeartbeatAt          *time.Time
	ArchivedAt               *time.Time
	Version                  int64 `gorm:"not null"`
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

func (Location) TableName() string { return "locations" }

// ------------------------------------------------------------------ wilayah distributor

type Territory struct {
	ID          uuid.UUID `gorm:"primaryKey;type:uuid"`
	Code        string    `gorm:"not null"`
	Name        string    `gorm:"not null"`
	Description *string
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Territory) TableName() string { return "territories" }

type TerritoryRegion struct {
	TerritoryID uuid.UUID `gorm:"primaryKey;type:uuid"`
	RegionCode  string    `gorm:"primaryKey"`
	CreatedAt   time.Time
}

func (TerritoryRegion) TableName() string { return "territory_regions" }

// Nilai partner_territories.coverage_mode. Pembaca selalu memakai kolom ini,
// tidak pernah menyimpulkan NATIONAL dari territory yang kosong.
const (
	CoverageNational = "NATIONAL"
	CoverageSelected = "SELECTED"
)

type PartnerTerritory struct {
	ID           uuid.UUID  `gorm:"primaryKey;type:uuid"`
	PartyID      uuid.UUID  `gorm:"type:uuid;not null"`
	TerritoryID  *uuid.UUID `gorm:"type:uuid"`
	CoverageMode string     `gorm:"not null"`
	ValidFrom    time.Time  `gorm:"not null;default:now()"`
	ValidTo      *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (PartnerTerritory) TableName() string { return "partner_territories" }

// ------------------------------------------------------------------ harga

type PriceList struct {
	ID          uuid.UUID  `gorm:"primaryKey;type:uuid"`
	Code        string     `gorm:"not null"`
	Name        string     `gorm:"not null"`
	ChannelCode string     `gorm:"not null"`
	LocationID  *uuid.UUID `gorm:"type:uuid"`
	Version     int        `gorm:"not null"`
	Currency    string     `gorm:"not null;default:IDR"`
	ValidFrom   time.Time  `gorm:"not null"`
	ValidTo     *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (PriceList) TableName() string { return "price_lists" }

// PriceListItem: harga dalam rupiah utuh.
type PriceListItem struct {
	PriceListID uuid.UUID `gorm:"primaryKey;type:uuid"`
	ProductID   uuid.UUID `gorm:"primaryKey;type:uuid"`
	Price       int64     `gorm:"not null"`
	CreatedAt   time.Time
}

func (PriceListItem) TableName() string { return "price_list_items" }
