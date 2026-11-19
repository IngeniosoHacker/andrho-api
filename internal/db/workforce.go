package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkSite mirrors work_sites, with its supervisors' user ids.
type WorkSite struct {
	ID, AccountID, Name string
	OdooLocationID      int
	Active              bool
	Supervisors         []string
	CreatedAt           time.Time
}

const workSiteCols = `s.id::text, s.account_id::text, s.name, s.odoo_location_id, s.active, s.created_at,
	COALESCE((SELECT array_agg(user_id::text ORDER BY user_id) FROM work_site_supervisors w WHERE w.site_id = s.id), '{}')`

func scanWorkSite(row pgx.Row) (WorkSite, error) {
	var s WorkSite
	err := row.Scan(&s.ID, &s.AccountID, &s.Name, &s.OdooLocationID, &s.Active, &s.CreatedAt, &s.Supervisors)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

func querySites(ctx context.Context, pool *pgxpool.Pool, q string, args ...any) ([]WorkSite, error) {
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkSite{}
	for rows.Next() {
		s, err := scanWorkSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListWorkSites lists an account's sites (inactive ones too when all).
func ListWorkSites(ctx context.Context, pool *pgxpool.Pool, accountID string, all bool) ([]WorkSite, error) {
	return querySites(ctx, pool, `SELECT `+workSiteCols+` FROM work_sites s WHERE s.account_id = $1 AND (s.active OR $2) ORDER BY s.name`, accountID, all)
}

// SupervisorSites are the active sites a user supervises.
func SupervisorSites(ctx context.Context, pool *pgxpool.Pool, accountID, userID string) ([]WorkSite, error) {
	return querySites(ctx, pool, `SELECT `+workSiteCols+` FROM work_sites s JOIN work_site_supervisors w ON w.site_id = s.id
		WHERE s.account_id = $1 AND w.user_id = $2 AND s.active ORDER BY s.name`, accountID, userID)
}

// GetWorkSite reads one of an account's sites.
func GetWorkSite(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (WorkSite, error) {
	return scanWorkSite(pool.QueryRow(ctx, `SELECT `+workSiteCols+` FROM work_sites s WHERE s.id::text = $2 AND s.account_id = $1`, accountID, id))
}

// SaveWorkSite inserts or updates a site.
func SaveWorkSite(ctx context.Context, pool *pgxpool.Pool, s WorkSite) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO work_sites (id, account_id, name, odoo_location_id, active) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (id) DO UPDATE SET name = $3, odoo_location_id = $4, active = $5, updated_at = now()
		 WHERE work_sites.account_id = $2`, s.ID, s.AccountID, s.Name, s.OdooLocationID, s.Active)
	return err
}

// SetSiteSupervisors replaces a site's supervisors (users of its account).
func SetSiteSupervisors(ctx context.Context, pool *pgxpool.Pool, accountID, siteID string, userIDs []string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM work_site_supervisors WHERE site_id = $1`, siteID); err != nil {
		return err
	}
	if len(userIDs) > 0 {
		if _, err := tx.Exec(ctx,
			`INSERT INTO work_site_supervisors (site_id, user_id)
			 SELECT $1, u.id FROM users u WHERE u.account_id = $2 AND u.id::text = ANY($3)`, siteID, accountID, userIDs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
