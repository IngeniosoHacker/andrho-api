package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/config"
)

// CORS builds a middleware that allows the origins listed in cfg.AllowedOrigins
// (or every origin, when unset / set to "*") for GET/POST/OPTIONS with
// Content-Type and Authorization headers.
func CORS(cfg *config.Config) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, o := range cfg.AllowedOrigins {
		allowed[o] = struct{}{}
	}
	allowAll := cfg.AllowAllOrigins()

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		if allowAll {
			if origin != "" {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			} else {
				c.Header("Access-Control-Allow-Origin", "*")
			}
		} else if _, ok := allowed[origin]; ok {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
