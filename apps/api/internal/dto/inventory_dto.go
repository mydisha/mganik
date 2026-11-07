package dto

import (
	"time"

	"github.com/google/uuid"
)

// ------------------------------------------------------------------ saldo

// BalanceItem adalah saldo satu (lokasi, produk). online_atp = stok yang
// boleh dijanjikan ke channel online (0 kalau POS lokasi OFFLINE).
type BalanceItem struct {
	LocationID      uuid.UUID `json:"location_id"`
	LocationCode    string    `json:"location_code"`
	LocationName    string    `json:"location_name"`
	LocationType    string    `json:"location_type"`
	SyncState       string    `json:"sync_state"`
	ProductID       uuid.UUID `json:"product_id"`
	SKU             string    `json:"sku"`
	ProductName     string    `json:"product_name"`
	PhysicalQty     int64     `json:"physical_qty"`
	ReservedQty     int64     `json:"reserved_qty"`
	QuarantineQty   int64     `json:"quarantine_qty"`
	AvailableQty    int64     `json:"available_qty"`
	OnlineBufferQty int64     `json:"online_buffer_qty"`
	OnlineATP       int64     `json:"online_atp"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type BalanceListResponse struct {
	Items []BalanceItem `json:"items"`
	// NextCursor null = halaman terakhir.
	NextCursor *string `json:"next_cursor"`
}

// ------------------------------------------------------------------ adjustment & karantina

// AdjustmentRequest: stock opname. qty_delta bertanda (≠ 0).
type AdjustmentRequest struct {
	LocationID string `json:"location_id" binding:"required,uuid"`
	ProductID  string `json:"product_id" binding:"required,uuid"`
	QtyDelta   int64  `json:"qty_delta" binding:"required,ne=0,min=-1000000,max=1000000"`
	Reason     string `json:"reason" binding:"required,max=2000"`
}

// QuarantineRequest: direction IN = masuk karantina, OUT = rilis.
type QuarantineRequest struct {
	LocationID string `json:"location_id" binding:"required,uuid"`
	ProductID  string `json:"product_id" binding:"required,uuid"`
	Qty        int64  `json:"qty" binding:"required,gt=0,lte=1000000"`
	Direction  string `json:"direction" binding:"required,oneof=IN OUT"`
	Reason     string `json:"reason" binding:"required,max=2000"`
}

type MovementDTO struct {
	ID              uuid.UUID  `json:"id"`
	LocationID      uuid.UUID  `json:"location_id"`
	ProductID       uuid.UUID  `json:"product_id"`
	MovementType    string     `json:"movement_type"`
	PhysicalDelta   int64      `json:"physical_delta"`
	ReservedDelta   int64      `json:"reserved_delta"`
	QuarantineDelta int64      `json:"quarantine_delta"`
	RefType         string     `json:"ref_type"`
	RefID           uuid.UUID  `json:"ref_id"`
	Reason          *string    `json:"reason"`
	ActorType       string     `json:"actor_type"`
	ActorID         *uuid.UUID `json:"actor_id"`
	OccurredAt      time.Time  `json:"occurred_at"`
	RecordedAt      time.Time  `json:"recorded_at"`
}

type StockBalanceDTO struct {
	LocationID    uuid.UUID `json:"location_id"`
	ProductID     uuid.UUID `json:"product_id"`
	PhysicalQty   int64     `json:"physical_qty"`
	ReservedQty   int64     `json:"reserved_qty"`
	QuarantineQty int64     `json:"quarantine_qty"`
	AvailableQty  int64     `json:"available_qty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// StockChangeResponse: duplicate=true berarti Idempotency-Key sudah pernah
// dipakai dengan isi sama; tidak ada mutasi baru.
type StockChangeResponse struct {
	Movement  MovementDTO     `json:"movement"`
	Balance   StockBalanceDTO `json:"balance"`
	Duplicate bool            `json:"duplicate"`
}
