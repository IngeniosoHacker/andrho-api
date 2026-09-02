// Package trackerdb holds a second Postgres pool pointing at the WebTracker
// database. andrho-api only ever touches the `sites` table there, replicating
// the upsert pattern already used by WebTracker's collect.js (siteIsAllowed):
//
//	INSERT INTO sites (id, name, allowed_origin) VALUES ($1, $2, $3)
//	ON CONFLICT (id) DO NOTHING
package trackerdb

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// New creates a pgxpool.Pool for the tracker's database at trackerDatabaseURL.
func New(ctx context.Context, trackerDatabaseURL string) (*pgxpool.Pool, error) {
	if trackerDatabaseURL == "" {
		return nil, fmt.Errorf("trackerdb: TRACKER_DATABASE_URL is empty")
	}

	cfg, err := pgxpool.ParseConfig(trackerDatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("trackerdb: parse config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("trackerdb: new pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("trackerdb: ping: %w", err)
	}

	return pool, nil
}

// RegisterSite upserts a row into the tracker's `sites` table so the new
// account's site_id is immediately known to WebTracker's ingestion endpoint.
// Mirrors WebTracker/src/routes/collect.js siteIsAllowed's insert exactly,
// except we pass the real company name instead of defaulting name to the id.
func RegisterSite(ctx context.Context, pool *pgxpool.Pool, siteID, name string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO sites (id, name, allowed_origin) VALUES ($1, $2, $3)
		 ON CONFLICT (id) DO NOTHING`,
		siteID, name, nil,
	)
	if err != nil {
		return fmt.Errorf("trackerdb: register site: %w", err)
	}
	return nil
}
