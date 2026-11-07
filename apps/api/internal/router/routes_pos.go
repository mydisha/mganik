package router

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/controller"
	"mganik/api/internal/middleware"
	"mganik/api/internal/pii"
	"mganik/api/internal/service"
)

// posSyncPath: route yang memakai batas body POS_SYNC_MAX_BODY_BYTES.
const posSyncPath = "/api/v1/pos/sync"

// registerPOS mendaftarkan provisioning device, bootstrap data offline,
// heartbeat, dan sync transaksi POS.
func registerPOS(api *gin.RouterGroup, d Deps) { MountPOS(api, d) }

// posBodyLimits: batas body khusus route POS (dipakai middleware global).
func posBodyLimits(cfg config.Config) map[string]int64 {
	return map[string]int64{posSyncPath: cfg.POS.WithDefaults().SyncMaxBodyBytes}
}

// posSigningCipher menurunkan cipher signing key device dari PII_ENC_KEY,
// dengan fallback yang sama dengan piiKeys (kunci demo di luar production).
func posSigningCipher(cfg config.Config) (*pii.SigningKeyCipher, error) {
	key := cfg.PIIEncKey
	if len(key) == 0 {
		if cfg.IsProduction() {
			return nil, errors.New("PII_ENC_KEY kosong di production")
		}
		key = []byte(config.DemoPIIEncKey)
	}
	c, err := pii.NewSigningKeyCipher(key)
	if err != nil {
		return nil, fmt.Errorf("PII_ENC_KEY tidak valid: %w", err)
	}
	return c, nil
}

// NewPosService merangkai PosService dari Deps (dipakai router dan test).
func NewPosService(d Deps) (*service.PosService, error) {
	clk := d.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	keys, err := posSigningCipher(d.Config)
	if err != nil {
		return nil, err
	}
	inv := service.NewInventoryService(d.DB, clk)
	serials := service.NewSerialService(d.DB, clk, d.Config.ClaimPepper)
	return service.NewPosService(d.DB, clk, d.Config.POS, keys, inv, serials, d.Log), nil
}

// MountPOS memasang route POS di grup /api/v1. Diekspor supaya test
// integrasi bisa memasangnya di belakang principal buatan.
//
//	POST /pos/devices                staff, pos.device.manage (scope lokasi via CanAt)
//	GET  /pos/devices                staff, pos.device.manage
//	POST /pos/devices/:id/revoke     staff, pos.device.manage
//	GET  /pos/bootstrap              device (Bearer), pos.sync di lokasinya
//	GET  /pos/bootstrap/serials      device
//	POST /pos/heartbeat              device
//	POST /pos/sync                   device (batas body POS_SYNC_MAX_BODY_BYTES)
//	POST /cc/conflicts/:id/resolve   staff, conflict.resolve (scope lokasi konflik)
//
// Route resolve konflik ada di sini (bukan routes_commandcenter.go) karena
// aksi tulisnya milik domain POS; Command Center tetap read model.
func MountPOS(api *gin.RouterGroup, d Deps) {
	svc, err := NewPosService(d)
	if err != nil {
		// Tidak terjangkau lewat router.New: newAuthResolver sudah memvalidasi
		// kunci yang sama sebelum route didaftarkan.
		panic(err)
	}
	ctl := controller.NewPosController(svc, d.Log)
	perm := func(p string) gin.HandlerFunc { return middleware.RequirePermission(d.Log, p) }
	staff := middleware.RequireKind(d.Log, auth.KindStaff)
	device := middleware.RequireKind(d.Log, auth.KindDevice)

	pos := api.Group("/pos")
	pos.POST("/devices", staff, perm(service.PermPosDeviceManage), ctl.ProvisionDevice)
	pos.GET("/devices", staff, perm(service.PermPosDeviceManage), ctl.ListDevices)
	pos.POST("/devices/:id/revoke", staff, perm(service.PermPosDeviceManage), ctl.RevokeDevice)

	pos.GET("/bootstrap", device, perm(service.PermPosSync), ctl.Bootstrap)
	pos.GET("/bootstrap/serials", device, perm(service.PermPosSync), ctl.BootstrapSerials)
	pos.POST("/heartbeat", device, perm(service.PermPosSync), ctl.Heartbeat)
	pos.POST("/sync", device, perm(service.PermPosSync), ctl.Sync)

	api.POST("/cc/conflicts/:id/resolve", staff, perm(service.PermConflictResolve), ctl.ResolveConflict)
}
