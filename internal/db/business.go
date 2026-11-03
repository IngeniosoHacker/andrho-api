package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BusinessType returns the account's type of business ("" = none).
func BusinessType(ctx context.Context, pool *pgxpool.Pool, accountID string) (string, error) {
	var t string
	err := pool.QueryRow(ctx, `SELECT business_type FROM accounts WHERE id = $1`, accountID).Scan(&t)
	if err != nil {
		return "", fmt.Errorf("db: business type: %w", err)
	}
	return t, nil
}

// SetBusinessType sets the account's type of business.
func SetBusinessType(ctx context.Context, pool *pgxpool.Pool, accountID, typ string) error {
	_, err := pool.Exec(ctx, `UPDATE accounts SET business_type = $2, updated_at = now() WHERE id = $1`, accountID, typ)
	if err != nil {
		return fmt.Errorf("db: set business type: %w", err)
	}
	return nil
}

// AccountSite mirrors `account_sites`.
type AccountSite struct {
	AccountID   string    `json:"-"`
	Host        string    `json:"host"`
	Status      string    `json:"status"`
	LiveURL     string    `json:"live_url"`
	DomainState string    `json:"domain_state,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const siteCols = `account_id, host, status, live_url, domain_state, created_at, updated_at`

func scanSite(row pgx.Row) (AccountSite, error) {
	var s AccountSite
	err := row.Scan(&s.AccountID, &s.Host, &s.Status, &s.LiveURL, &s.DomainState, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// SaveAccountSite creates or updates the account's site.
func SaveAccountSite(ctx context.Context, pool *pgxpool.Pool, s AccountSite) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO account_sites (account_id, host, status, live_url, domain_state) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (account_id) DO UPDATE SET host = EXCLUDED.host, status = EXCLUDED.status,
		   live_url = EXCLUDED.live_url, domain_state = EXCLUDED.domain_state, updated_at = now()`,
		s.AccountID, s.Host, s.Status, s.LiveURL, s.DomainState)
	if err != nil {
		return fmt.Errorf("db: save account site: %w", err)
	}
	return nil
}

// GetAccountSite returns the account's site (ErrNotFound without one).
func GetAccountSite(ctx context.Context, pool *pgxpool.Pool, accountID string) (AccountSite, error) {
	return scanSite(pool.QueryRow(ctx, `SELECT `+siteCols+` FROM account_sites WHERE account_id = $1`, accountID))
}

// GetAccountSiteByHost resolves a site by its host (the sites service).
func GetAccountSiteByHost(ctx context.Context, pool *pgxpool.Pool, host string) (AccountSite, error) {
	return scanSite(pool.QueryRow(ctx, `SELECT `+siteCols+` FROM account_sites WHERE host = $1`, host))
}
