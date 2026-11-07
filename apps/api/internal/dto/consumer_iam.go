package dto

import (
	"time"

	"github.com/google/uuid"
)

// OTPRequest: POST /public/otp/request.
type OTPRequest struct {
	Phone string `json:"phone" binding:"required,max=32"`
}

// OTPRequestResponse: kode dikirim (mock: kode tetap demo, tidak pernah
// dikembalikan di respons).
type OTPRequestResponse struct {
	ExpiresAt        time.Time `json:"expires_at"`
	ExpiresInSeconds int       `json:"expires_in_seconds"`
}

// OTPVerifyRequest: POST /public/otp/verify.
type OTPVerifyRequest struct {
	Phone string `json:"phone" binding:"required,max=32"`
	Code  string `json:"code" binding:"required,max=16"`
}

// ConsumerMeResponse: konsumen yang sedang login. Nomor selalu disamarkan.
type ConsumerMeResponse struct {
	ID          uuid.UUID `json:"id"`
	PhoneMasked string    `json:"phone_masked"`
}
