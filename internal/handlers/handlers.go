// Package handlers implements the HTTP handlers for the /auth/* endpoints.
package handlers

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/IngeniosoHacker/andrho-api/internal/config"
)

// Handler bundles the dependencies every /auth/* handler needs.
type Handler struct {
	Accounts *pgxpool.Pool
	Tracker  *pgxpool.Pool
	Redis    *redis.Client
	Cfg      *config.Config
}

// New builds a Handler.
func New(accounts, tracker *pgxpool.Pool, rdb *redis.Client, cfg *config.Config) *Handler {
	return &Handler{Accounts: accounts, Tracker: tracker, Redis: rdb, Cfg: cfg}
}

func (h *Handler) accessTTL() time.Duration {
	return time.Duration(h.Cfg.JWTAccessTTLMinutes) * time.Minute
}

func (h *Handler) refreshTTL() time.Duration {
	return time.Duration(h.Cfg.JWTRefreshTTLDays) * 24 * time.Hour
}
