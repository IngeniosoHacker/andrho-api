package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FeatureSetting is an account's own choice for a feature: set=false when
// it never chose (the default applies).
func FeatureSetting(ctx context.Context, pool *pgxpool.Pool, accountID, feature string) (enabled, set bool, err error) {
	err = pool.QueryRow(ctx, `SELECT enabled FROM account_features WHERE account_id = $1 AND feature = $2`, accountID, feature).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	return enabled, err == nil, err
}

// SetFeature records an account's choice for a feature.
func SetFeature(ctx context.Context, pool *pgxpool.Pool, accountID, feature string, enabled bool) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO account_features (account_id, feature, enabled) VALUES ($1, $2, $3)
		 ON CONFLICT (account_id, feature) DO UPDATE SET enabled = $3, updated_at = now()`, accountID, feature, enabled)
	return err
}
