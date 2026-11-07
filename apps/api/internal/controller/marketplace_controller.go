package controller

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/dto"
	"mganik/api/internal/service"
)

// MarketplaceController menerima impor order marketplace (signature sudah
// diverifikasi middleware VerifyWebhook).
type MarketplaceController struct {
	svc *service.MarketplaceService
	log *slog.Logger
}

func NewMarketplaceController(svc *service.MarketplaceService, log *slog.Logger) *MarketplaceController {
	return &MarketplaceController{svc: svc, log: log}
}

// Import: POST /channels/marketplace/orders. 201 order baru, 200 duplikat.
func (h *MarketplaceController) Import(c *gin.Context) {
	var req dto.MarketplaceOrderRequest
	if err := BindJSON(c, &req); err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	in := service.MarketplaceOrderInput{
		ChannelCode: req.ChannelCode, ExternalRef: req.ExternalRef, PlacedAt: req.PlacedAt, Currency: req.Currency,
	}
	for _, l := range req.Lines {
		in.Lines = append(in.Lines, service.MarketplaceLine{SKU: l.SKU, Qty: l.Qty, UnitPrice: l.UnitPrice, Discount: l.Discount})
	}
	res, err := h.svc.Import(c.Request.Context(), in)
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
	}
	c.JSON(status, dto.MarketplaceImportResponse{Duplicate: res.Duplicate, Order: OrderResponse(res.View)})
}
