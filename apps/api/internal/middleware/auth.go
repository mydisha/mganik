package middleware

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
)

// Authenticate menjalankan resolver dan memasang Principal ke context request.
// Request tanpa kredensial tetap diteruskan sebagai anonim; RequirePermission
// yang menolaknya. Kredensial yang ada tetapi tidak valid langsung ditolak.
// resolver nil berarti belum ada mekanisme login: semua request anonim.
func Authenticate(resolver auth.Resolver, log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resolver == nil {
			c.Next()
			return
		}
		p, err := resolver.Resolve(c.Request.Context(), c.Request)
		if err != nil {
			apperror.Write(c, log, err)
			return
		}
		if p != nil {
			c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), p))
		}
		c.Next()
	}
}

// RequireKind menolak request yang principal-nya bukan salah satu kind.
func RequireKind(log *slog.Logger, kinds ...auth.Kind) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := auth.FromContext(c.Request.Context())
		if p == nil {
			apperror.Write(c, log, apperror.ErrUnauthenticated)
			return
		}
		for _, k := range kinds {
			if p.Kind == k {
				c.Next()
				return
			}
		}
		apperror.Write(c, log, apperror.ErrForbidden)
	}
}

// RequirePermission menolak request anonim (401) dan principal tanpa
// permission perm (403). Cek lokasi/objek (BOLA) tetap dilakukan di service.
func RequirePermission(log *slog.Logger, perm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := auth.FromContext(c.Request.Context())
		if p == nil {
			apperror.Write(c, log, apperror.ErrUnauthenticated)
			return
		}
		if !p.Can(perm) {
			apperror.Write(c, log, apperror.ErrForbidden)
			return
		}
		c.Next()
	}
}
