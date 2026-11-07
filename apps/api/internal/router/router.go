// Package router membuat engine Gin, memasang middleware, dan merangkai
// dependency controller → service → repository secara manual.
package router

import (
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
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
}

func New(d Deps) (*gin.Engine, error) {
	if d.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

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

	return r, nil
}
