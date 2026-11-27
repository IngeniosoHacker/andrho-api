// Package handlers implements the HTTP handlers for the /auth/* endpoints.
package handlers

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/billing"
	"github.com/IngeniosoHacker/andrho-api/internal/config"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/meta"
	"github.com/IngeniosoHacker/andrho-api/internal/notify"
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
	"github.com/IngeniosoHacker/andrho-api/internal/rstats"
	"github.com/IngeniosoHacker/andrho-api/internal/secret"
	"github.com/IngeniosoHacker/andrho-api/internal/sites"
	"github.com/IngeniosoHacker/andrho-api/internal/vps"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// Handler bundles the dependencies every /auth/* handler needs.
type Handler struct {
	Accounts  *pgxpool.Pool
	Tracker   *pgxpool.Pool
	Redis     *redis.Client
	Cfg       *config.Config
	AI        *ai.Client // disabled without ANTHROPIC_API_KEY
	Provision *provision.Provisioner
	Secrets   *secret.Box
	Stats     *rstats.Client // R analyses for insights, run on the VPS
	// Payments charges the checkout (internal/billing); a no-op that
	// approves everything while PAYMENTS_ENABLED=false.
	Payments billing.PaymentProvider
	// Notify tells people when their ERP is ready (email + Web Push).
	Notify *notify.Notifier
	// Meta is AndRho's Meta app (venta en vivo); Enabled() is false until
	// META_APP_ID and META_APP_SECRET are set.
	Meta *meta.Client
	// WA is AndRho's own WhatsApp number (internal/whatsapp); Enabled() is
	// false until WHATSAPP_ENABLED=true with its token and phone number id.
	WA *whatsapp.Client
	// WAApp is the Meta app for the stores' own numbers (Embedded Signup:
	// the code exchange); see whatsapp_business.go.
	WAApp *whatsapp.App
}

// New builds a Handler. AI calls record their tokens in token_usage. A bad
// VPS configuration (key or host key that don't parse) is an error.
func New(accounts, tracker *pgxpool.Pool, rdb *redis.Client, cfg *config.Config) (*Handler, error) {
	h := &Handler{Accounts: accounts, Tracker: tracker, Redis: rdb, Cfg: cfg,
		Meta:  meta.New(cfg.MetaAppID, cfg.MetaAppSecret, cfg.MetaGraphVersion, cfg.MetaLoginConfigID),
		WA:    whatsapp.New(cfg.WhatsAppEnabled, cfg.WhatsAppToken, cfg.WhatsAppPhoneNumberID, cfg.WhatsAppWABAID, cfg.MetaGraphVersion),
		WAApp: whatsapp.NewApp(cfg.MetaAppID, cfg.MetaAppSecret, cfg.MetaGraphVersion)}
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
	h.Payments = billing.FromEnv()
	h.Stats = &rstats.Client{VPS: runner} // R for the Web insights, on the VPS
	h.Notify = notify.FromEnv(accounts, cfg.DashboardURL)
	h.Provision = &provision.Provisioner{DB: accounts, VPS: runner, Secrets: box, DBMode: cfg.OdooDBMode, DashboardURL: cfg.DashboardURL,
		Sites: sites.FromEnv(), Notify: h.Notify, WhatsAppSignup: cfg.WhatsAppEmbeddedSignup}
	if cfg.WhatsAppEmbeddedSignup && accounts != nil {
		// whatsapp jobs that waited for Meta now wait for the store.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if n, err := db.MoveWhatsAppJobsToClient(ctx, accounts, provision.StatusWaitingClient, provision.WhatsAppConnectDetail); err != nil {
			log.Printf("whatsapp: move jobs: %v", err)
		} else if n > 0 {
			log.Printf("whatsapp: %d jobs now wait for the store to connect", n)
		}
		cancel()
	}
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
	pacc := provision.Account{ID: acc.ID, ClientID: acc.SiteID, Company: acc.CompanyName, Plan: acc.Plan}
	users, err := db.ListUsersByAccount(ctx, h.Accounts, accountID)
	if err != nil {
		return provision.Account{}, err
	}
	for _, u := range users {
		if u.Role == "owner" {
			pacc.OwnerEmail, pacc.OwnerName = u.Email, u.DisplayName
			break
		}
	}
	return pacc, nil
}

func (h *Handler) accessTTL() time.Duration {
	return time.Duration(h.Cfg.JWTAccessTTLMinutes) * time.Minute
}

func (h *Handler) refreshTTL() time.Duration {
	return time.Duration(h.Cfg.JWTRefreshTTLDays) * 24 * time.Hour
}
