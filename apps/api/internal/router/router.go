// Package router membuat engine Gin, memasang middleware, dan merangkai
// dependency controller → service → repository secara manual.
package router

import (
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/controller"
	"mganik/api/internal/middleware"
	"mganik/api/internal/repository"
	"mganik/api/internal/service"
)

type Deps struct {
	Config config.Config
	DB     *gorm.DB
	Log    *slog.Logger
	// Clock opsional; default jam sistem. Test memakai clock.Fake.
	Clock clock.Clock
}

func New(d Deps) (*gin.Engine, error) {
	if d.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	controller.SetupValidator()

	r := gin.New()
	r.HandleMethodNotAllowed = true
	// IP klien hanya diambil dari X-Forwarded-For kalau request datang dari
	// proxy tepercaya (nginx di compose). Daftar kosong = tidak percaya siapa pun.
	if err := r.SetTrustedProxies(d.Config.TrustedProxies); err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES tidak valid: %w", err)
	}

	r.Use(
		middleware.RequestID(),
		middleware.AccessLog(d.Log),
		middleware.Recovery(d.Log),
		middleware.SecurityHeaders(d.Config.IsProduction()),
		middleware.BodyLimit(d.Config.MaxBodyBytes),
	)
	r.NoRoute(func(c *gin.Context) { apperror.Write(c, d.Log, apperror.ErrNotFound) })
	r.NoMethod(func(c *gin.Context) { apperror.Write(c, d.Log, apperror.ErrMethodNotAllowed) })

	// Health
	healthRepo := repository.NewHealthRepository(d.DB)
	healthSvc := service.NewHealthService(healthRepo)
	healthCtl := controller.NewHealthController(healthSvc, d.Log)

	r.GET("/healthz", healthCtl.Live)
	r.GET("/readyz", healthCtl.Ready)

	// Autentikasi: resolver dibangun di routes_iam.go. Request tanpa kredensial
	// diteruskan sebagai anonim; RequirePermission per route yang menolaknya.
	resolver, err := newAuthResolver(d)
	if err != nil {
		return nil, err
	}
	api := r.Group("/api/v1", middleware.Authenticate(resolver, d.Log))

	// Satu file per domain supaya pekerjaan paralel tidak saling tabrak.
	// Setiap fungsi mendaftarkan route domainnya sendiri di bawah /api/v1.
	registerIAM(api, d)           // routes_iam.go
	registerSerialization(api, d) // routes_serialization.go (produksi, agregasi, shipment, verifikasi, klaim)
	registerInventory(api, d)     // routes_inventory.go (stok, order, pembayaran)
	registerPOS(api, d)           // routes_pos.go
	registerCommandCenter(api, d) // routes_commandcenter.go

	return r, nil
}
