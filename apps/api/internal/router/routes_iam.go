package router

import (
	"github.com/gin-gonic/gin"

	"mganik/api/internal/auth"
)

// newAuthResolver membangun resolver yang mengenali sesi staff, token device
// POS, dan sesi konsumen. nil = belum ada login (semua request anonim).
func newAuthResolver(_ Deps) (auth.Resolver, error) {
	return nil, nil
}

// registerIAM mendaftarkan login, logout, profil, OTP konsumen.
func registerIAM(_ *gin.RouterGroup, _ Deps) {}
