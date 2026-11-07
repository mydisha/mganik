package controller

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mganik/api/internal/apperror"
	"mganik/api/internal/dto"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
)

// PaymentController menerima webhook pembayaran (signature sudah diverifikasi
// middleware VerifyWebhook).
type PaymentController struct {
	svc *service.PaymentService
	log *slog.Logger
}

func NewPaymentController(svc *service.PaymentService, log *slog.Logger) *PaymentController {
	return &PaymentController{svc: svc, log: log}
}

// MockWebhook: POST /webhooks/payments/mock
func (h *PaymentController) MockWebhook(c *gin.Context) {
	var req dto.PaymentWebhookRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	in := service.PaymentWebhookInput{
		ProviderRef: req.ProviderRef, OrderID: uuid.MustParse(req.OrderID), Amount: req.Amount,
		Currency: req.Currency, Status: req.Status, Raw: model.JSON(raw),
	}
	if req.Method != "" {
		in.Method = &req.Method
	}
	res, err := h.svc.HandleMockWebhook(c.Request.Context(), in)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	resp := dto.PaymentWebhookResponse{
		OrderID: res.OrderID, OrderStatus: res.OrderStatus, Outcome: res.Outcome, Duplicate: res.Duplicate,
	}
	if res.PaymentID != uuid.Nil {
		resp.PaymentID = &res.PaymentID
	}
	c.JSON(http.StatusOK, resp)
}
