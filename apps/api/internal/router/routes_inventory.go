package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
	"mganik/api/internal/controller"
	"mganik/api/internal/middleware"
	"mganik/api/internal/service"
)

// registerInventory mendaftarkan route saldo stok, adjustment, karantina,
// order omnichannel, impor marketplace, dan webhook pembayaran.
func registerInventory(api *gin.RouterGroup, d Deps) { MountInventory(api, d) }

var errWebhookNotConfigured = apperror.New(http.StatusServiceUnavailable, "webhook_not_configured",
	"Webhook belum dikonfigurasi", "WEBHOOK_SECRET belum diisi; webhook ditolak.")

// MountInventory memasang route domain stok & order di grup /api/v1.
// Diekspor supaya test integrasi bisa memasangnya di engine dengan principal
// buatan (login dibangun stream IAM).
//
//	GET  /inventory/balances              inventory.read (scope lokasi dicek di service)
//	POST /inventory/adjustments           inventory.adjust     + Idempotency-Key
//	POST /inventory/quarantine            inventory.quarantine + Idempotency-Key
//	POST /orders                          order.create         + Idempotency-Key
//	GET  /orders/:id                      order.read
//	POST /orders/:id/cancel               order.cancel
//	POST /orders/:id/fulfill              order.fulfill
//	POST /webhooks/payments/mock          signature HMAC (tanpa sesi)
//	POST /channels/marketplace/orders     signature HMAC (tanpa sesi)
func MountInventory(api *gin.RouterGroup, d Deps) {
	clk := d.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	invSvc := service.NewInventoryService(d.DB, clk)
	idem := service.NewIdempotencyService(d.DB, clk)
	orders := service.NewOrderService(d.DB, clk, invSvc, idem, d.Config.Inventory.ReservationTTL)
	// Fulfillment menandai serial unit SOLD di transaksi yang sama (I2).
	orders.SetSerialSaleHook(service.NewOrderSerialSale(service.NewSerialService(d.DB, clk, d.Config.ClaimPepper)))

	invCtl := controller.NewInventoryController(invSvc, d.Log)
	orderCtl := controller.NewOrderController(orders, d.Log)
	payCtl := controller.NewPaymentController(service.NewPaymentService(orders), d.Log)
	mktCtl := controller.NewMarketplaceController(service.NewMarketplaceService(orders), d.Log)

	perm := func(p string) gin.HandlerFunc { return middleware.RequirePermission(d.Log, p) }

	inv := api.Group("/inventory")
	inv.GET("/balances", perm("inventory.read"), invCtl.ListBalances)
	inv.POST("/adjustments", perm("inventory.adjust"), invCtl.Adjust)
	inv.POST("/quarantine", perm("inventory.quarantine"), invCtl.Quarantine)

	ord := api.Group("/orders")
	ord.POST("", perm("order.create"), orderCtl.Create)
	ord.GET("/:id", perm("order.read"), orderCtl.Get)
	ord.POST("/:id/cancel", perm("order.cancel"), orderCtl.Cancel)
	ord.POST("/:id/fulfill", perm("order.fulfill"), orderCtl.Fulfill)

	// Tanpa secret, signature HMAC dengan key kosong bisa dipalsukan siapa
	// pun: webhook ditolak seluruhnya. Config production mewajibkan secret.
	verify := func(c *gin.Context) { apperror.Write(c, d.Log, errWebhookNotConfigured) }
	if len(d.Config.Inventory.WebhookSecret) > 0 {
		verify = middleware.VerifyWebhook(middleware.NewWebhookSigner(d.Config.Inventory.WebhookSecret), clk, d.Log)
	}
	api.POST("/webhooks/payments/mock", verify, payCtl.MockWebhook)
	api.POST("/channels/marketplace/orders", verify, mktCtl.Import)
}
