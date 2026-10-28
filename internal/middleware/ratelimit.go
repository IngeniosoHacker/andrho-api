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

	waitlistRateLimitMax    = 5
	waitlistRateLimitWindow = time.Hour
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

// RateLimit throttles a route per IP with the same fixed-window counter as
// LoginRateLimit, under the key "ratelimit:<name>:<ip>". For public,
// unauthenticated endpoints open to the whole internet.
func RateLimit(rdb *redis.Client, name string, max int64, window time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if rdb == nil {
			c.Next()
			return
		}

		key := "ratelimit:" + name + ":" + c.ClientIP()

		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		count, err := rdb.Incr(ctx, key).Result()
		if err != nil {
			// Redis unavailable: fail open rather than locking everyone out.
			c.Next()
			return
		}
		if count == 1 {
			rdb.Expire(ctx, key, window)
		}

		if count > max {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests, please try again later"})
			return
		}

		c.Next()
	}
}

// WaitlistRateLimit caps POST /waitlist submissions per IP.
func WaitlistRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return RateLimit(rdb, "waitlist", waitlistRateLimitMax, waitlistRateLimitWindow)
}

// RecommendationRateLimit caps POST /waitlist/recommendation per IP. It's a
// pure computation, so the cap is generous: people go back and forth.
func RecommendationRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return RateLimit(rdb, "recommendation", 120, time.Hour)
}

// AnalyzerRateLimit caps POST /analyzer per IP: cheap, called as the
// signup's answers change.
func AnalyzerRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return RateLimit(rdb, "analyzer", 120, time.Hour)
}

// DescribeRateLimit caps POST /analyzer/describe per IP: it may call Claude.
func DescribeRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return RateLimit(rdb, "describe", 20, time.Hour)
}

// SuggestRateLimit caps POST /analyzer/suggest per IP: it may call Claude,
// and a signup can go back and forth between its last cards a few times.
func SuggestRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return RateLimit(rdb, "suggest", 30, time.Hour)
}

// RegisterRateLimit caps POST /auth/register per IP: each one creates an
// account and may start services on the VPS.
func RegisterRateLimit(rdb *redis.Client) gin.HandlerFunc {
	return RateLimit(rdb, "register", 10, time.Hour)
}
