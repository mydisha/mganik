package router

import (
	"github.com/gin-gonic/gin"

	"mganik/api/internal/controller"
	"mganik/api/internal/middleware"
	"mganik/api/internal/repository"
	"mganik/api/internal/service"
)

// registerCommandCenter mendaftarkan read model Command Center: visibilitas
// stok, runout/DOI, antrean anomali dan konflik alokasi.
func registerCommandCenter(api *gin.RouterGroup, d Deps) {
	MountCommandCenter(api, d)
}

// MountCommandCenter memasang route /cc/* ke group api. Diekspor supaya test
// integrasi bisa memasangnya di belakang middleware principal buatan tanpa
// bergantung pada mekanisme login.
func MountCommandCenter(api *gin.RouterGroup, d Deps) {
	repo := repository.NewCommandCenterRepository(d.DB)
	svc := service.NewCommandCenterService(repo, d.Clock)
	ctl := controller.NewCommandCenterController(svc, d.Log)

	read := middleware.RequirePermission(d.Log, service.PermCCRead)
	cc := api.Group("/cc")
	cc.GET("/summary", read, ctl.Summary)
	cc.GET("/stock", read, ctl.Stock)
	cc.GET("/runout", read, ctl.Runout)
	cc.GET("/anomalies", read, ctl.Anomalies)
	cc.PATCH("/anomalies/:id", middleware.RequirePermission(d.Log, service.PermAnomalyResolve), ctl.UpdateAnomaly)
	cc.GET("/conflicts", read, ctl.Conflicts)
	cc.GET("/distribution-leakage", read, ctl.Leakage)
}
