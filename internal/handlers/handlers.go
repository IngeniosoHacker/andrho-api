// Package handlers implements the HTTP handlers for the /auth/* endpoints.
package handlers

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/config"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// Handler bundles the dependencies every /auth/* handler needs.
type Handler struct {
	Accounts *pgxpool.Pool
	Tracker  *pgxpool.Pool
	Redis    *redis.Client
	Cfg      *config.Config
	AI       *ai.Client // disabled without ANTHROPIC_API_KEY
}

// New builds a Handler. AI calls record their tokens in token_usage.
func New(accounts, tracker *pgxpool.Pool, rdb *redis.Client, cfg *config.Config) *Handler {
	h := &Handler{Accounts: accounts, Tracker: tracker, Redis: rdb, Cfg: cfg}
	h.AI = ai.New(cfg.AnthropicAPIKey, cfg.AnthropicModel, func(ctx context.Context, u ai.Usage) {
		// Detached: the request's context may be gone by the time we write.
		err := db.RecordTokenUsage(context.WithoutCancel(ctx), accounts, db.TokenUsage{
			Source: u.Source, AccountID: u.AccountID, Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		})
		if err != nil {
			log.Printf("ai: %v", err)
		}
	})
	return h
}

func (h *Handler) accessTTL() time.Duration {
	return time.Duration(h.Cfg.JWTAccessTTLMinutes) * time.Minute
}

func (h *Handler) refreshTTL() time.Duration {
	return time.Duration(h.Cfg.JWTRefreshTTLDays) * 24 * time.Hour
}
