package dto

import "time"

// VerifyRequest: POST /api/v1/public/verify (tanpa login). Koordinat opsional
// (izin lokasi browser); lat dan lng wajib berpasangan.
type VerifyRequest struct {
	Code      string   `json:"code" binding:"required,max=512"`
	Lat       *float64 `json:"lat" binding:"required_with=Lng,omitempty,min=-90,max=90"`
	Lng       *float64 `json:"lng" binding:"required_with=Lat,omitempty,min=-180,max=180"`
	AccuracyM *int     `json:"accuracy_m" binding:"omitempty,min=0,max=100000"`
}

// VerifyProduct hanya dikirim untuk hasil GENUINE_* dan RECALLED.
type VerifyProduct struct {
	Name    *string    `json:"name"`
	LotNo   *string    `json:"lot_no"`
	ExpDate *time.Time `json:"exp_date"`
}

// VerifyResponse tidak pernah memuat status internal serial.
type VerifyResponse struct {
	// Result: GENUINE_FIRST, GENUINE_REPEAT, SUSPICIOUS, NOT_FOUND, RECALLED.
	Result            string         `json:"result"`
	Product           *VerifyProduct `json:"product,omitempty"`
	FirstVerifiedAt   *time.Time     `json:"first_verified_at,omitempty"`
	FirstVerifiedCity *string        `json:"first_verified_city,omitempty"`
}
