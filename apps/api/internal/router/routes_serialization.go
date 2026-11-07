package router

import (
	"time"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/auth"
	"mganik/api/internal/controller"
	"mganik/api/internal/middleware"
	"mganik/api/internal/service"
)

// registerSerialization mendaftarkan route produksi (mint kode, commission),
// agregasi, shipment, trace, serta endpoint publik verifikasi dan klaim.
func registerSerialization(api *gin.RouterGroup, d Deps) { RegisterSerializationRoutes(api, d) }

// RegisterSerializationRoutes dipisah (exported) supaya test integrasi bisa
// memasang route yang sama persis di bawah middleware principal buatan test.
// Tidak menyentuh DB saat dipasang (router test memakai DB nil).
func RegisterSerializationRoutes(api *gin.RouterGroup, d Deps) {
	cfg := d.Config.Serialization.WithDefaults()
	serials := service.NewSerialService(d.DB, d.Clock, d.Config.ClaimPepper)
	verify := service.NewVerifyService(d.DB, d.Clock, d.Config.ClaimPepper, service.UnknownIPGeo{})
	loyalty := service.NewLoyaltyService(d.DB, d.Clock, d.Config.ClaimPepper, cfg, serials)

	sc := controller.NewSerialController(serials, d.Log)
	pc := controller.NewPublicController(verify, loyalty, d.Log, d.Config.IsProduction())
	lc := controller.NewLoyaltyController(loyalty, d.Log)
	perm := func(code string) gin.HandlerFunc { return middleware.RequirePermission(d.Log, code) }

	// Produksi & agregasi
	api.POST("/code-batches", perm("code.mint"), sc.Mint)
	api.POST("/serials/commission", perm("serial.commission"), sc.Commission)
	api.POST("/serials/receive", perm("serial.receive"), sc.Receive)
	api.POST("/serials/aggregate", perm("serial.aggregate"), sc.Aggregate)
	api.POST("/serials/:code/unpack", perm("serial.aggregate"), sc.Unpack)
	api.GET("/serials/:code/trace", perm("serial.trace"), sc.Trace)
	api.POST("/batches/:id/recall", perm("batch.recall"), sc.Recall)

	// Shipment
	api.POST("/shipments", perm("shipment.create"), sc.CreateShipment)
	api.POST("/shipments/:id/receive", perm("shipment.receive"), sc.ReceiveShipment)

	// Review klaim berisiko
	api.POST("/loyalty/claims/:id/review", perm("loyalty.review"), lc.Review)

	// Publik: rate limit per IP dipasang sebelum body dibaca.
	verifyLimit := controller.NewPublicRateLimiter(cfg.VerifyRatePerMinute, time.Minute, d.Clock)
	claimLimit := controller.NewPublicRateLimiter(cfg.ClaimRatePerMinute, time.Minute, d.Clock)
	claimPerConsumer := controller.NewPublicRateLimiter(cfg.ClaimRatePerConsumerPerHour, time.Hour, d.Clock)
	consumer := middleware.RequireKind(d.Log, auth.KindConsumer)
	pub := api.Group("/public")
	pub.POST("/verify", controller.RateLimitByIP(verifyLimit, d.Log), pc.Verify)
	pub.POST("/claims", controller.RateLimitByIP(claimLimit, d.Log), consumer,
		controller.RateLimitByConsumer(claimPerConsumer, d.Log), pc.Claim)
	pub.GET("/loyalty/balance", consumer, pc.Balance)
}
