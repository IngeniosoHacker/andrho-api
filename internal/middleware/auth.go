package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
)

// AccountContextKey is the gin.Context key under which RequireAuth stores the
// verified access-token claims.
const AccountContextKey = "account"

// RequireAuth verifies the Authorization: Bearer <token> header against
// jwtSecret and stores the parsed claims in the Gin context under
// AccountContextKey for downstream handlers (e.g. /auth/me).
func RequireAuth(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			respondUnauthorized(c, "missing authorization header")
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			respondUnauthorized(c, "invalid authorization header")
			return
		}

		claims, err := auth.ParseAccessToken(jwtSecret, parts[1])
		if err != nil {
			respondUnauthorized(c, "invalid or expired token")
			return
		}

		c.Set(AccountContextKey, claims)
		c.Next()
	}
}

func respondUnauthorized(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": message})
}
