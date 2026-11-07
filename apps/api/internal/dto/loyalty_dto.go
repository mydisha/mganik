package dto

import "time"

// ClaimRequest: POST /api/v1/public/claims (sesi konsumen).
type ClaimRequest struct {
	Code      string   `json:"code" binding:"required,max=64"`
	PIN       string   `json:"pin" binding:"required,max=64"`
	Lat       *float64 `json:"lat" binding:"required_with=Lng,omitempty,min=-90,max=90"`
	Lng       *float64 `json:"lng" binding:"required_with=Lat,omitempty,min=-180,max=180"`
	AccuracyM *int     `json:"accuracy_m" binding:"omitempty,min=0,max=100000"`
}

// ClaimResponse tidak memuat skor/alasan risiko (tidak membantu pelaku fraud
// menyetel perilakunya).
type ClaimResponse struct {
	ClaimID     string    `json:"claim_id"`
	Status      string    `json:"status"`
	Points      int64     `json:"points"`
	AvailableAt time.Time `json:"available_at"`
	Replayed    bool      `json:"replayed"`
}

// LoyaltyBalanceResponse: GET /api/v1/public/loyalty/balance.
type LoyaltyBalanceResponse struct {
	Available int64 `json:"available"`
	Pending   int64 `json:"pending"`
}

// ReviewClaimRequest: POST /api/v1/loyalty/claims/{id}/review. APPROVE/REJECT
// untuk klaim PENDING, REVERSE (note wajib) untuk klaim APPROVED.
type ReviewClaimRequest struct {
	Decision string  `json:"decision" binding:"required,oneof=APPROVE REJECT REVERSE"`
	Note     *string `json:"note" binding:"omitempty,max=1000"`
}

type ClaimRiskReason struct {
	Rule   string `json:"rule"`
	Weight int    `json:"weight"`
}

type ReviewClaimResponse struct {
	ClaimID        string            `json:"claim_id"`
	Status         string            `json:"status"`
	Points         int64             `json:"points"`
	RiskScore      int               `json:"risk_score"`
	RiskReasons    []ClaimRiskReason `json:"risk_reasons"`
	ReviewRequired bool              `json:"review_required"`
	AvailableAt    time.Time         `json:"available_at"`
	DecidedAt      *time.Time        `json:"decided_at"`
}
