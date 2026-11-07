package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimitByPath sama dengan BodyLimit, tetapi path yang ada di overrides
// (cocok persis dengan URL.Path) memakai batasnya sendiri, misal POST
// /api/v1/pos/sync yang membawa sampai 500 transaksi (PLANNING §6.4).
// Batas tidak bisa diperbesar dengan membungkus ulang body di level route
// (MaxBytesReader dalam tetap berlaku), jadi pemilihan batas ada di sini.
func BodyLimitByPath(defaultMax int64, overrides map[string]int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			limit := defaultMax
			if v, ok := overrides[c.Request.URL.Path]; ok {
				limit = v
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		}
		c.Next()
	}
}
