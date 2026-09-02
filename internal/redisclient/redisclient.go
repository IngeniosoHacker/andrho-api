// Package redisclient wires up the Redis client used for login rate-limiting.
package redisclient

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// New parses redisURL and returns a connected *redis.Client.
func New(ctx context.Context, redisURL string) (*redis.Client, error) {
	if redisURL == "" {
		return nil, fmt.Errorf("redisclient: REDIS_URL is empty")
	}

	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redisclient: parse url: %w", err)
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redisclient: ping: %w", err)
	}

	return client, nil
}
