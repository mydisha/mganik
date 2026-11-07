package controller

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/dto"
	"mganik/api/internal/service"
)

type consumerService interface {
	RequestOTP(ctx context.Context, rawPhone string, meta service.RequestMeta) (service.OTPRequested, error)
	VerifyOTP(ctx context.Context, rawPhone, code, previousToken string, meta service.RequestMeta) (service.ConsumerLogin, error)
	Logout(ctx context.Context, token string, meta service.RequestMeta) error
	Profile(ctx context.Context, consumerID uuid.UUID) (service.ConsumerProfile, error)
}

// ConsumerController: login OTP konsumen dan sesi konsumen.
type ConsumerController struct {
	svc consumerService
	log *slog.Logger
}

func NewConsumerController(svc consumerService, log *slog.Logger) *ConsumerController {
	return &ConsumerController{svc: svc, log: log}
}

// RequestOTP: POST /public/otp/request → 202.
func (h *ConsumerController) RequestOTP(c *gin.Context) {
	var req dto.OTPRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.RequestOTP(c.Request.Context(), req.Phone, requestMeta(c))
	if err != nil {
		writeAuthError(c, h.log, err)
		return
	}
	c.JSON(http.StatusAccepted, dto.OTPRequestResponse{
		ExpiresAt:        res.ExpiresAt,
		ExpiresInSeconds: int(res.TTL.Seconds()),
	})
}

// VerifyOTP: POST /public/otp/verify → cookie sesi konsumen + profil.
func (h *ConsumerController) VerifyOTP(c *gin.Context) {
	var req dto.OTPVerifyRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	res, err := h.svc.VerifyOTP(c.Request.Context(), req.Phone, req.Code,
		cookieValue(c, service.ConsumerSessionCookie), requestMeta(c))
	if err != nil {
		writeAuthError(c, h.log, err)
		return
	}
	setSessionCookie(c, service.ConsumerSessionCookie, res.Token, service.ConsumerAbsoluteTimeout)
	c.JSON(http.StatusOK, dto.ConsumerMeResponse{ID: res.ConsumerID, PhoneMasked: res.PhoneMasked})
}

// Me: GET /public/consumer/me (butuh principal konsumen).
func (h *ConsumerController) Me(c *gin.Context) {
	p := auth.FromContext(c.Request.Context())
	if p == nil || p.Kind != auth.KindConsumer {
		apperror.Write(c, h.log, apperror.ErrUnauthenticated)
		return
	}
	prof, err := h.svc.Profile(c.Request.Context(), p.ID)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.ConsumerMeResponse{ID: prof.ID, PhoneMasked: prof.PhoneMasked})
}

// Logout: POST /public/consumer/logout. Selalu 204 (idempotent).
func (h *ConsumerController) Logout(c *gin.Context) {
	if err := h.svc.Logout(c.Request.Context(), cookieValue(c, service.ConsumerSessionCookie), requestMeta(c)); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	clearSessionCookie(c, service.ConsumerSessionCookie)
	c.Status(http.StatusNoContent)
}
