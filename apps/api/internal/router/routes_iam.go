package router

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/config"
	"mganik/api/internal/controller"
	"mganik/api/internal/middleware"
	"mganik/api/internal/pii"
	"mganik/api/internal/repository"
	"mganik/api/internal/service"
)

// newAuthResolver membangun resolver yang mengenali token Bearer device POS,
// cookie sesi staff, dan cookie sesi konsumen. Kunci PII divalidasi di sini
// (dipanggil sebelum registerIAM) supaya kesalahan konfigurasi menggagalkan
// router.New, bukan panic saat route didaftarkan.
func newAuthResolver(d Deps) (auth.Resolver, error) {
	if _, err := piiKeys(d.Config); err != nil {
		return nil, err
	}
	return service.NewAuthResolver(
		repository.NewAuthRepository(d.DB),
		repository.NewConsumerRepository(d.DB),
		d.Clock,
		service.NewCSRFGuard(d.Config.AllowedOrigins),
		d.Log,
	), nil
}

// registerIAM mendaftarkan login, logout, profil, OTP konsumen.
func registerIAM(api *gin.RouterGroup, d Deps) {
	keys, err := piiKeys(d.Config)
	if err != nil {
		// Tidak terjangkau: newAuthResolver sudah memvalidasi kunci yang sama.
		panic(err)
	}
	csrf := csrfMiddleware(service.NewCSRFGuard(d.Config.AllowedOrigins), d)

	authRepo := repository.NewAuthRepository(d.DB)
	authSvc := service.NewAuthService(d.DB, authRepo, d.Clock, keys, d.Log, service.DefaultLoginPolicy())
	authCtl := controller.NewAuthController(authSvc, d.Log)
	// Login dan logout dicek CSRF walau tanpa cookie (mencegah login CSRF).
	api.POST("/auth/login", csrf, authCtl.Login)
	api.POST("/auth/logout", csrf, authCtl.Logout)
	api.GET("/auth/me", middleware.RequireKind(d.Log, auth.KindStaff), authCtl.Me)

	consumerSvc := service.NewConsumerService(d.DB, repository.NewConsumerRepository(d.DB), authRepo, d.Clock, keys,
		d.Log, d.Config.OTPMockCode, service.DefaultOTPPolicy())
	consumerCtl := controller.NewConsumerController(consumerSvc, d.Log)
	api.POST("/public/otp/request", csrf, consumerCtl.RequestOTP)
	api.POST("/public/otp/verify", csrf, consumerCtl.VerifyOTP)
	api.GET("/public/consumer/me", middleware.RequireKind(d.Log, auth.KindConsumer), consumerCtl.Me)
	api.POST("/public/consumer/logout", csrf, consumerCtl.Logout)
}

// piiKeys menurunkan kunci PII dari PII_ENC_KEY. Config hasil Load selalu
// berisi kunci (default demo di luar production); Config buatan test yang
// kosong memakai kunci demo, kecuali production.
func piiKeys(cfg config.Config) (*pii.Keys, error) {
	key := cfg.PIIEncKey
	if len(key) == 0 {
		if cfg.IsProduction() {
			return nil, errors.New("PII_ENC_KEY kosong di production")
		}
		key = []byte(config.DemoPIIEncKey)
	}
	k, err := pii.New(key)
	if err != nil {
		return nil, fmt.Errorf("PII_ENC_KEY tidak valid: %w", err)
	}
	return k, nil
}

func csrfMiddleware(g *service.CSRFGuard, d Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := g.Check(c.Request); err != nil {
			apperror.Write(c, d.Log, err)
			return
		}
		c.Next()
	}
}
