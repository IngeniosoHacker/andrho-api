package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// Site statuses (account_sites.status).
const (
	SiteOffered   = "offered"   // the plan doesn't include a site yet: no service
	SiteDeploying = "deploying" // the client's Railway service is being created or deployed
	SiteLive      = "live"      // sites-router serves it
)

// AccountSite mirrors `account_sites`.
type AccountSite struct {
	AccountID          string    `json:"-"`
	Host               string    `json:"host"`
	Subdomain          string    `json:"subdomain"`
	Status             string    `json:"status"`
	LiveURL            string    `json:"live_url"`
	DomainState        string    `json:"domain_state,omitempty"`
	RailwayServiceID   string    `json:"-"`
	RailwayServiceName string    `json:"-"`
	LegacyHost         string    `json:"legacy_host,omitempty"`
	CustomDomain       string    `json:"custom_domain,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// PublicHost is where the site is canonical: the client's own domain once
// it has one, else its subdomain host.
func (s AccountSite) PublicHost() string {
	if s.CustomDomain != "" {
		return s.CustomDomain
	}
	return s.Host
}

const siteCols = `account_id, host, COALESCE(subdomain, ''), status, live_url, domain_state, railway_service_id,
  railway_service_name, legacy_host, custom_domain, created_at, updated_at`

func scanSite(row pgx.Row) (AccountSite, error) {
	var s AccountSite
	err := row.Scan(&s.AccountID, &s.Host, &s.Subdomain, &s.Status, &s.LiveURL, &s.DomainState, &s.RailwayServiceID,
		&s.RailwayServiceName, &s.LegacyHost, &s.CustomDomain, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// SaveAccountSite creates or updates the account's site (every column but
// the subdomain, which ReserveSiteSubdomain owns).
func SaveAccountSite(ctx context.Context, pool *pgxpool.Pool, s AccountSite) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO account_sites (account_id, host, status, live_url, domain_state, railway_service_id, railway_service_name,
		   legacy_host, custom_domain)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (account_id) DO UPDATE SET host = EXCLUDED.host, status = EXCLUDED.status,
		   live_url = EXCLUDED.live_url, domain_state = EXCLUDED.domain_state,
		   railway_service_id = EXCLUDED.railway_service_id, railway_service_name = EXCLUDED.railway_service_name,
		   legacy_host = EXCLUDED.legacy_host, custom_domain = EXCLUDED.custom_domain, updated_at = now()`,
		s.AccountID, s.Host, s.Status, s.LiveURL, s.DomainState, s.RailwayServiceID, s.RailwayServiceName, s.LegacyHost, s.CustomDomain)
	if err != nil {
		return fmt.Errorf("db: save account site: %w", err)
	}
	return nil
}

// ReserveSiteSubdomain gives the account a subdomain: the one it already
// has, or the first free of candidates (base, base-2, base-3…, built by
// sites.Candidates). host(sub) is the host stored with it. A new row starts
// with status.
func ReserveSiteSubdomain(ctx context.Context, pool *pgxpool.Pool, accountID, status string, candidates []string, host func(string) string) (AccountSite, error) {
	if s, err := GetAccountSite(ctx, pool, accountID); err == nil && s.Subdomain != "" {
		return s, nil
	}
	for _, sub := range candidates {
		h := host(sub)
		tag, err := pool.Exec(ctx,
			`INSERT INTO account_sites (account_id, host, subdomain, status) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (account_id) DO UPDATE SET subdomain = EXCLUDED.subdomain, host = EXCLUDED.host, updated_at = now()
			   WHERE account_sites.subdomain IS NULL`,
			accountID, h, sub, status)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // taken: next candidate
				continue
			}
			return AccountSite{}, fmt.Errorf("db: reserve subdomain: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return GetAccountSite(ctx, pool, accountID)
		}
	}
	return AccountSite{}, fmt.Errorf("db: reserve subdomain: no free candidate")
}

// GetAccountSite returns the account's site (ErrNotFound without one).
func GetAccountSite(ctx context.Context, pool *pgxpool.Pool, accountID string) (AccountSite, error) {
	return scanSite(pool.QueryRow(ctx, `SELECT `+siteCols+` FROM account_sites WHERE account_id = $1`, accountID))
}

// GetAccountSiteByHost resolves a host the way sites-router does: the
// client's own domain first, then its subdomain host, then an old host
// (legacy, to redirect).
func GetAccountSiteByHost(ctx context.Context, pool *pgxpool.Pool, host string) (AccountSite, error) {
	return scanSite(pool.QueryRow(ctx, `SELECT `+siteCols+` FROM account_sites
		WHERE custom_domain = $1 OR host = $1 OR legacy_host = $1
		ORDER BY (custom_domain = $1) DESC, (host = $1) DESC LIMIT 1`, host))
}

// SitesToProvision lists sites that need work from the site provisioner:
// still deploying, or migrated from the old scheme without a service.
func SitesToProvision(ctx context.Context, pool *pgxpool.Pool) ([]AccountSite, error) {
	rows, err := pool.Query(ctx, `SELECT `+siteCols+` FROM account_sites WHERE status = $1 ORDER BY updated_at`, SiteDeploying)
	if err != nil {
		return nil, fmt.Errorf("db: sites to provision: %w", err)
	}
	defer rows.Close()
	out := []AccountSite{}
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// LegacySites lists sites from the old shared-service scheme (no subdomain).
func LegacySites(ctx context.Context, pool *pgxpool.Pool) ([]AccountSite, error) {
	rows, err := pool.Query(ctx, `SELECT `+siteCols+` FROM account_sites WHERE subdomain IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("db: legacy sites: %w", err)
	}
	defer rows.Close()
	out := []AccountSite{}
	for rows.Next() {
		s, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MoveLegacySite gives an old site its subdomain on the new scheme, keeping
// the old host to redirect from. Fails with a unique violation when the
// subdomain is taken (the caller tries the next candidate).
func MoveLegacySite(ctx context.Context, pool *pgxpool.Pool, accountID, sub, host, status string) error {
	_, err := pool.Exec(ctx,
		`UPDATE account_sites SET legacy_host = host, host = $3, subdomain = $2, status = $4, updated_at = now()
		  WHERE account_id = $1 AND subdomain IS NULL`, accountID, sub, host, status)
	return err
}

// SiteSEO mirrors `site_seo`.
type SiteSEO struct {
	AccountID   string          `json:"-"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	OGImage     string          `json:"og_image"`
	Lang        string          `json:"lang"`
	GSCToken    string          `json:"gsc_token"`
	Branches    json.RawMessage `json:"branches"`
}

// GetSiteSEO returns the site's SEO (defaults when none was saved).
func GetSiteSEO(ctx context.Context, pool *pgxpool.Pool, accountID string) (SiteSEO, error) {
	s := SiteSEO{AccountID: accountID, Lang: "es-GT", Branches: json.RawMessage(`[]`)}
	err := pool.QueryRow(ctx, `SELECT title, description, og_image, lang, gsc_token, branches FROM site_seo WHERE account_id = $1`, accountID).
		Scan(&s.Title, &s.Description, &s.OGImage, &s.Lang, &s.GSCToken, &s.Branches)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("db: site seo: %w", err)
	}
	return s, nil
}

// SaveSiteSEO creates or replaces the site's SEO.
func SaveSiteSEO(ctx context.Context, pool *pgxpool.Pool, s SiteSEO) error {
	if len(s.Branches) == 0 {
		s.Branches = json.RawMessage(`[]`)
	}
	if s.Lang == "" {
		s.Lang = "es-GT"
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO site_seo (account_id, title, description, og_image, lang, gsc_token, branches) VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (account_id) DO UPDATE SET title = EXCLUDED.title, description = EXCLUDED.description, og_image = EXCLUDED.og_image,
		   lang = EXCLUDED.lang, gsc_token = EXCLUDED.gsc_token, branches = EXCLUDED.branches, updated_at = now()`,
		s.AccountID, s.Title, s.Description, s.OGImage, s.Lang, s.GSCToken, s.Branches)
	if err != nil {
		return fmt.Errorf("db: save site seo: %w", err)
	}
	return nil
}
