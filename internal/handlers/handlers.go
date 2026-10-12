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
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
	"github.com/IngeniosoHacker/andrho-api/internal/secret"
	"github.com/IngeniosoHacker/andrho-api/internal/vps"
)

// Handler bundles the dependencies every /auth/* handler needs.
type Handler struct {
	Accounts *pgxpool.Pool
	Tracker  *pgxpool.Pool
	Redis    *redis.Client
	Cfg      *config.Config
	AI       *ai.Client // disabled without ANTHROPIC_API_KEY
	Provision *provision.Provisioner
	Secrets   *secret.Box
}

// New builds a Handler. AI calls record their tokens in token_usage. A bad
// VPS configuration (key or host key that don't parse) is an error.
func New(accounts, tracker *pgxpool.Pool, rdb *redis.Client, cfg *config.Config) (*Handler, error) {
	h := &Handler{Accounts: accounts, Tracker: tracker, Redis: rdb, Cfg: cfg}
	box, err := secret.New(cfg.SecretsKey)
	if err != nil {
		return nil, err
	}
	runner, err := vps.New(vps.Config{Host: cfg.VPSHost, Port: cfg.VPSPort, User: cfg.VPSUser,
		PrivateKey: cfg.VPSSSHKey, HostKey: cfg.VPSHostKey, ScriptsDir: cfg.VPSScriptsDir})
	if err != nil {
		return nil, err
	}
	h.Secrets = box
	h.Provision = &provision.Provisioner{DB: accounts, VPS: runner, Secrets: box, DBMode: cfg.OdooDBMode}
	h.AI = ai.New(cfg.AnthropicAPIKey, cfg.AnthropicModel, func(ctx context.Context, u ai.Usage) {
		// Detached: the request's context may be gone by the time we write.
		err := db.RecordTokenUsage(context.WithoutCancel(ctx), accounts, db.TokenUsage{
			Source: u.Source, AccountID: u.AccountID, Model: u.Model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		})
		if err != nil {
			log.Printf("ai: %v", err)
		}
	})
	return h, nil
}

// ProvisionAccount loads what provision.Run needs about an account.
func (h *Handler) ProvisionAccount(ctx context.Context, accountID string) (provision.Account, error) {
	acc, err := db.GetAccountByID(ctx, h.Accounts, accountID)
	if err != nil {
		return provision.Account{}, err
	}
	return provision.Account{ID: acc.ID, ClientID: acc.SiteID, Company: acc.CompanyName, Plan: acc.Plan}, nil
}

func (h *Handler) accessTTL() time.Duration {
	return time.Duration(h.Cfg.JWTAccessTTLMinutes) * time.Minute
}

func (h *Handler) refreshTTL() time.Duration {
	return time.Duration(h.Cfg.JWTRefreshTTLDays) * 24 * time.Hour
}
