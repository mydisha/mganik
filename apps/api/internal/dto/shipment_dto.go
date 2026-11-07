package dto

import "time"

// CreateShipmentRequest: POST /api/v1/shipments. codes berisi palet, karton,
// atau unit; seluruh turunannya ikut terkirim.
type CreateShipmentRequest struct {
	FromLocationID string   `json:"from_location_id" binding:"required,uuid"`
	ToLocationID   string   `json:"to_location_id" binding:"required,uuid"`
	ToPartyID      *string  `json:"to_party_id" binding:"omitempty,uuid"`
	ProgramCode    *string  `json:"program_code" binding:"omitempty,min=1,max=64"`
	Codes          []string `json:"codes" binding:"required,min=1,max=500,dive,required,max=64"`
}

type ShipmentItem struct {
	SerialUnitID string `json:"serial_unit_id"`
	ProductID    string `json:"product_id"`
	Qty          int    `json:"qty"`
}

type ShipmentResponse struct {
	ID              string         `json:"id"`
	Code            string         `json:"code"`
	Status          string         `json:"status"`
	FromLocationID  string         `json:"from_location_id"`
	ToLocationID    string         `json:"to_location_id"`
	ToPartyID       *string        `json:"to_party_id"`
	ProgramCode     *string        `json:"program_code"`
	ShippedAt       *time.Time     `json:"shipped_at"`
	ReceivedAt      *time.Time     `json:"received_at"`
	Items           []ShipmentItem `json:"items"`
	Units           int64          `json:"units"`
	Serials         int64          `json:"serials"`
	Skipped         int64          `json:"skipped"`
	AlreadyReceived bool           `json:"already_received"`
}
