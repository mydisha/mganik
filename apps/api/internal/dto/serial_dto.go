package dto

import "time"

// ---------------------------------------------------------------- produksi

// MintCodesRequest: POST /api/v1/code-batches.
type MintCodesRequest struct {
	LevelCode     string  `json:"level_code" binding:"required,max=32"`
	Qty           int     `json:"qty" binding:"required,min=1,max=5000"`
	ProductID     *string `json:"product_id" binding:"omitempty,uuid"`
	PrinterVendor string  `json:"printer_vendor" binding:"required,max=200"`
	Notes         *string `json:"notes" binding:"omitempty,max=1000"`
}

// CodeBatchResponse adalah job cetak label.
type CodeBatchResponse struct {
	ID            string    `json:"id"`
	LevelCode     string    `json:"level_code"`
	ProductID     *string   `json:"product_id"`
	Qty           int       `json:"qty"`
	PrinterVendor string    `json:"printer_vendor"`
	Notes         *string   `json:"notes"`
	IssuedAt      time.Time `json:"issued_at"`
}

// MintedCode adalah satu kode untuk file cetak. PIN hanya untuk level UNIT.
type MintedCode struct {
	Code string `json:"code"`
	PIN  string `json:"pin,omitempty"`
}

// MintCodesResponse: kode dan PIN plaintext hanya dikirim sekali di sini.
type MintCodesResponse struct {
	CodeBatch CodeBatchResponse `json:"code_batch"`
	Codes     []MintedCode      `json:"codes"`
}

// CommissionRequest: POST /api/v1/serials/commission.
type CommissionRequest struct {
	BatchID string   `json:"batch_id" binding:"required,uuid"`
	Codes   []string `json:"codes" binding:"required,min=1,max=5000,dive,required,max=64"`
}

type CommissionResponse struct {
	BatchID      string `json:"batch_id"`
	ProductID    string `json:"product_id"`
	Commissioned int    `json:"commissioned"`
}

// ReceiveSerialsRequest: POST /api/v1/serials/receive.
type ReceiveSerialsRequest struct {
	LocationID string   `json:"location_id" binding:"required,uuid"`
	Codes      []string `json:"codes" binding:"required,min=1,max=5000,dive,required,max=64"`
}

type ReceiveSerialsResponse struct {
	LocationID string `json:"location_id"`
	Roots      int    `json:"roots"`
	Serials    int64  `json:"serials"`
}

// ---------------------------------------------------------------- agregasi

// AggregateRequest: POST /api/v1/serials/aggregate.
type AggregateRequest struct {
	ParentCode string   `json:"parent_code" binding:"required,max=64"`
	ChildCodes []string `json:"child_codes" binding:"required,min=1,max=1000,dive,required,max=64"`
}

type AggregateResponse struct {
	ParentCode      string `json:"parent_code"`
	Added           int    `json:"added"`
	AlreadyInParent int    `json:"already_in_parent"`
	ChildCount      int64  `json:"child_count"`
	Capacity        int    `json:"capacity"`
}

type UnpackResponse struct {
	Code     string `json:"code"`
	Unpacked int64  `json:"unpacked"`
}

// ---------------------------------------------------------------- recall

// RecallRequest: POST /api/v1/batches/{id}/recall.
type RecallRequest struct {
	Reason string `json:"reason" binding:"required,max=1000"`
}

type RecallResponse struct {
	BatchID         string    `json:"batch_id"`
	Status          string    `json:"status"`
	RecalledAt      time.Time `json:"recalled_at"`
	Quarantined     int64     `json:"quarantined"`
	AlreadyRecalled bool      `json:"already_recalled"`
}

// ---------------------------------------------------------------- trace

type TraceResponse struct {
	Serial      TraceSerial       `json:"serial"`
	Ancestors   []TraceAncestor   `json:"ancestors"`
	Descendants []TraceDescendant `json:"descendants"`
	Events      []TraceEvent      `json:"events"`
	Shipments   []TraceShipment   `json:"shipments"`
	Scans       []TraceScan       `json:"scans"`
}

type TraceRef struct {
	ID   *string `json:"id"`
	Code *string `json:"code"`
	Name *string `json:"name"`
}

type TraceSerial struct {
	ID                string     `json:"id"`
	Code              string     `json:"code"`
	LevelCode         string     `json:"level_code"`
	Status            string     `json:"status"`
	ParentID          *string    `json:"parent_id"`
	Product           *TraceRef  `json:"product"`
	Batch             *TraceLot  `json:"batch"`
	Location          *TraceRef  `json:"location"`
	Custodian         *TraceRef  `json:"custodian"`
	CommissionedAt    *time.Time `json:"commissioned_at"`
	VerifyCount       int        `json:"verify_count"`
	FirstVerifiedAt   *time.Time `json:"first_verified_at"`
	FirstVerifiedCity *string    `json:"first_verified_city"`
}

type TraceLot struct {
	ID      string     `json:"id"`
	LotNo   *string    `json:"lot_no"`
	Status  *string    `json:"status"`
	ExpDate *time.Time `json:"exp_date"`
}

type TraceAncestor struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	LevelCode string `json:"level_code"`
	Status    string `json:"status"`
	Depth     int    `json:"depth"`
}

type TraceDescendant struct {
	LevelCode string `json:"level_code"`
	Status    string `json:"status"`
	Count     int64  `json:"count"`
}

type TraceEvent struct {
	ID           string    `json:"id"`
	BizStep      string    `json:"biz_step"`
	FromStatus   *string   `json:"from_status"`
	ToStatus     *string   `json:"to_status"`
	ParentCode   *string   `json:"parent_code"`
	LocationCode *string   `json:"location_code"`
	PartyName    *string   `json:"party_name"`
	ActorType    string    `json:"actor_type"`
	RefType      *string   `json:"ref_type"`
	RefID        *string   `json:"ref_id"`
	OccurredAt   time.Time `json:"occurred_at"`
}

type TraceShipment struct {
	ID               string     `json:"id"`
	Code             string     `json:"code"`
	Status           string     `json:"status"`
	ProgramCode      *string    `json:"program_code"`
	FromLocationCode string     `json:"from_location_code"`
	ToLocationCode   string     `json:"to_location_code"`
	ToPartyName      *string    `json:"to_party_name"`
	ViaCode          string     `json:"via_code"`
	ShippedAt        *time.Time `json:"shipped_at"`
	ReceivedAt       *time.Time `json:"received_at"`
}

type TraceScan struct {
	ID         string    `json:"id"`
	Context    string    `json:"context"`
	Result     string    `json:"result"`
	GeoSource  string    `json:"geo_source"`
	RegionCode *string   `json:"region_code"`
	RegionName *string   `json:"region_name"`
	RuleHits   []string  `json:"rule_hits"`
	OccurredAt time.Time `json:"occurred_at"`
}
