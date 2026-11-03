package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/config"
)

// CORS builds a middleware that allows the origins listed in cfg.AllowedOrigins
// (or every origin, when unset / set to "*") for every method the router
// uses, with Content-Type and Authorization headers. PATCH/PUT/DELETE must be
// listed: without them the browser blocks the preflight and fetch() fails
// with "Failed to fetch" before the request reaches the handler.
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

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Max-Age", "600")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
