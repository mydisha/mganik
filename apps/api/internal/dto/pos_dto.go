package dto

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ------------------------------------------------------------------ device

// PosDeviceCreateRequest: POST /pos/devices.
type PosDeviceCreateRequest struct {
	LocationID uuid.UUID `json:"location_id" binding:"required"`
	Name       string    `json:"name" binding:"required"`
}

// PosDeviceRevokeRequest: POST /pos/devices/{id}/revoke (body opsional).
type PosDeviceRevokeRequest struct {
	Reason string `json:"reason"`
}

// PosDevice adalah device tanpa rahasia apa pun.
type PosDevice struct {
	ID            uuid.UUID  `json:"id"`
	LocationID    uuid.UUID  `json:"location_id"`
	LocationCode  string     `json:"location_code"`
	LocationName  string     `json:"location_name"`
	Name          string     `json:"name"`
	Status        string     `json:"status"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
	LastAckedSeq  int64      `json:"last_acked_seq"`
	ProvisionedBy *uuid.UUID `json:"provisioned_by"`
	RevokedAt     *time.Time `json:"revoked_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

// PosDeviceProvisioned adalah respons provisioning: token dan signing key
// hanya muncul di sini, sekali.
type PosDeviceProvisioned struct {
	Device PosDevice `json:"device"`
	// Token bearer device (Authorization: Bearer <token>).
	Token string `json:"token"`
	// SigningKey: hex huruf kecil 32 byte, kunci HMAC-SHA256 transaksi.
	SigningKey string `json:"signing_key"`
}

// PosDevicePage adalah halaman GET /pos/devices.
type PosDevicePage struct {
	Items      []PosDevice `json:"items"`
	NextCursor *string     `json:"next_cursor"`
}

// ------------------------------------------------------------------ bootstrap

type PosBootstrapResponse struct {
	ServerTime time.Time         `json:"server_time"`
	Device     PosDevice         `json:"device"`
	Location   PosLocation       `json:"location"`
	Limits     PosLimits         `json:"limits"`
	Products   []PosProduct      `json:"products"`
	PriceList  *PosPriceList     `json:"price_list"`
	Stock      []PosStock        `json:"stock"`
	Cashiers   []PosCashier      `json:"cashiers"`
	Serials    PosSerialPageBody `json:"serials"`
}

type PosLocation struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Timezone  string    `json:"timezone"`
	SyncState string    `json:"sync_state"`
}

// PosLimits adalah aturan offline dari config server.
type PosLimits struct {
	OfflineLimitSeconds       int64 `json:"offline_limit_seconds"`
	HeartbeatIntervalSeconds  int64 `json:"heartbeat_interval_seconds"`
	HeartbeatTimeoutSeconds   int64 `json:"heartbeat_timeout_seconds"`
	ClockSkewToleranceSeconds int64 `json:"clock_skew_tolerance_seconds"`
	SyncMaxBatch              int   `json:"sync_max_batch"`
	SyncMaxBodyBytes          int64 `json:"sync_max_body_bytes"`
	MaxSerialsPerTxn          int   `json:"max_serials_per_txn"`
	HighDiscountPercent       int   `json:"high_discount_percent"`
	OfflineValueLimit         int64 `json:"offline_value_limit"`
}

type PosProduct struct {
	ID   uuid.UUID `json:"id"`
	SKU  string    `json:"sku"`
	GTIN *string   `json:"gtin"`
	Name string    `json:"name"`
	UOM  string    `json:"uom"`
}

type PosPriceList struct {
	ID         uuid.UUID     `json:"id"`
	Code       string        `json:"code"`
	Version    int64         `json:"version"`
	LocationID *uuid.UUID    `json:"location_id"`
	Currency   string        `json:"currency"`
	ValidFrom  time.Time     `json:"valid_from"`
	ValidTo    *time.Time    `json:"valid_to"`
	Items      []PosPriceRow `json:"items"`
}

type PosPriceRow struct {
	ProductID uuid.UUID `json:"product_id"`
	Price     int64     `json:"price"`
}

type PosStock struct {
	ProductID     uuid.UUID `json:"product_id"`
	PhysicalQty   int64     `json:"physical_qty"`
	ReservedQty   int64     `json:"reserved_qty"`
	QuarantineQty int64     `json:"quarantine_qty"`
	AvailableQty  int64     `json:"available_qty"`
}

// PosCashier: pos_pin_hash ikut bootstrap untuk login lokal (trade-off
// PLANNING §6.2, lihat komentar di PosDeviceService.Bootstrap).
type PosCashier struct {
	ID         uuid.UUID `json:"id"`
	Name       string    `json:"name"`
	PosPinHash string    `json:"pos_pin_hash"`
}

type PosSerial struct {
	Code      string    `json:"code"`
	ProductID uuid.UUID `json:"product_id"`
}

// PosSerialPageBody: halaman serial UNIT IN_STOCK (keyset per kode).
type PosSerialPageBody struct {
	Items      []PosSerial `json:"items"`
	NextCursor *string     `json:"next_cursor"`
}

// ------------------------------------------------------------------ heartbeat

// PosHeartbeatRequest: body opsional.
type PosHeartbeatRequest struct {
	ClientTime   *time.Time `json:"client_time"`
	PendingCount *int64     `json:"pending_count"`
}

type PosHeartbeatResponse struct {
	ServerTime               time.Time `json:"server_time"`
	LocationSyncState        string    `json:"location_sync_state"`
	LastAckedSeq             int64     `json:"last_acked_seq"`
	HeartbeatIntervalSeconds int64     `json:"heartbeat_interval_seconds"`
	HeartbeatTimeoutSeconds  int64     `json:"heartbeat_timeout_seconds"`
	ClockSkewMs              *int64    `json:"clock_skew_ms"`
	// OnlineBlockedReason: null = heartbeat ini menjadikan toko ONLINE;
	// selain itu pending_count_missing, pending_outbox, atau missing_seqs.
	OnlineBlockedReason *string `json:"online_blocked_reason"`
}

// ------------------------------------------------------------------ sync

// PosSyncRequest: transaksi dibiarkan mentah (json.RawMessage) supaya
// signature dihitung atas objek persis seperti yang dikirim klien.
type PosSyncRequest struct {
	ClientTime   *time.Time        `json:"client_time"`
	Transactions []json.RawMessage `json:"transactions"`
}

// PosSyncResult adalah hasil satu transaksi. Detail adalah result_detail
// tersimpan apa adanya (identik antara pemrosesan pertama dan DUPLICATE).
type PosSyncResult struct {
	// Index: posisi transaksi di request (untuk item yang identitasnya rusak).
	Index       int             `json:"index"`
	ClientTxnID uuid.UUID       `json:"client_txn_id"`
	DeviceSeq   int64           `json:"device_seq"`
	Status      string          `json:"status"`
	Result      string          `json:"result"`
	Detail      json.RawMessage `json:"detail"`
}

type PosSyncResponse struct {
	ServerTime           time.Time       `json:"server_time"`
	AckUpToSeq           int64           `json:"ack_up_to_seq"`
	MissingSeqs          []int64         `json:"missing_seqs"`
	MissingSeqsTruncated bool            `json:"missing_seqs_truncated"`
	Results              []PosSyncResult `json:"results"`
}

// ------------------------------------------------------------------ konflik

// ConflictResolveRequest: POST /cc/conflicts/{id}/resolve.
type ConflictResolveRequest struct {
	Resolution string `json:"resolution" binding:"required"`
	Note       string `json:"note" binding:"required"`
}
