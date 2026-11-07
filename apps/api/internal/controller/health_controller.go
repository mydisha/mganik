// Package controller berisi handler Gin. Controller hanya mengurus HTTP:
// bind dan validasi DTO, memanggil service, lalu menulis respons atau
// problem+json. Controller tidak menyentuh database.
package controller

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/dto"
	"mganik/api/internal/service"
)

type readinessChecker interface {
	Readiness(ctx context.Context) (service.Readiness, error)
}

type HealthController struct {
	svc readinessChecker
	log *slog.Logger
}

func NewHealthController(svc readinessChecker, log *slog.Logger) *HealthController {
	return &HealthController{svc: svc, log: log}
}

// Live menjawab selama proses hidup. Dipakai healthcheck container.
func (h *HealthController) Live(c *gin.Context) {
	c.JSON(http.StatusOK, dto.LiveResponse{Status: "ok"})
}

// Ready menjawab 200 hanya kalau database terhubung dan migrasi sudah jalan.
func (h *HealthController) Ready(c *gin.Context) {
	r, err := h.svc.Readiness(c.Request.Context())
	if err != nil {
		apperror.Write(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.ReadyResponse{
		Status:        "ready",
		SchemaVersion: r.SchemaVersion,
		CheckedAt:     r.CheckedAt,
	})
}
