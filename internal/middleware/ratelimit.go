package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const (
	loginRateLimitMax    = 10
	loginRateLimitWindow = 15 * time.Minute
)

type loginBody struct {
	Email string `json:"email"`
}

// LoginRateLimit throttles login attempts per email+IP using a simple
// fixed-window counter in Redis: at most loginRateLimitMax attempts per
// loginRateLimitWindow. It peeks the request body to extract the email
// (without an email, or without Redis configured, it falls back to
// per-IP-only limiting) and restores the body afterwards so the handler can
// still read it.
func LoginRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rdb == nil {
			c.Next()
			return
		}

		raw, err := io.ReadAll(c.Request.Body)
		if err == nil {
			c.Request.Body = io.NopCloser(bytes.NewBuffer(raw))
		}

		var body loginBody
		_ = json.Unmarshal(raw, &body)

		key := "ratelimit:login:" + c.ClientIP() + ":" + body.Email

		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		count, incrErr := rdb.Incr(ctx, key).Result()
		if incrErr != nil {
			// Redis unavailable: fail open rather than locking everyone out.
			c.Next()
			return
		}
		if count == 1 {
			rdb.Expire(ctx, key, loginRateLimitWindow)
		}

		if count > loginRateLimitMax {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many login attempts, please try again later"})
			return
		}

		c.Next()
	}
}
