package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RefreshToken mirrors a row in the refresh_tokens table. AccountID is kept
// for rows issued before the multi-user migration; new tokens are issued
// with both AccountID and UserID (see CreateRefreshToken) so refresh.go can
// look the *user* up directly.
type RefreshToken struct {
	TokenHash string
	AccountID string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// CreateRefreshToken inserts a new refresh token hash for userID (whose
// account is accountID).
func CreateRefreshToken(ctx context.Context, pool *pgxpool.Pool, tokenHash, accountID, userID string, expiresAt time.Time) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO refresh_tokens (token_hash, account_id, user_id, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, accountID, userID, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("db: create refresh token: %w", err)
	}
	return nil
}

// GetRefreshToken looks up a refresh token row by its hash.
func GetRefreshToken(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (RefreshToken, error) {
	var rt RefreshToken
	var userID *string
	err := pool.QueryRow(ctx,
		`SELECT token_hash, account_id, user_id, expires_at, created_at FROM refresh_tokens WHERE token_hash = $1`,
		tokenHash,
	).Scan(&rt.TokenHash, &rt.AccountID, &userID, &rt.ExpiresAt, &rt.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RefreshToken{}, ErrNotFound
		}
		return RefreshToken{}, fmt.Errorf("db: get refresh token: %w", err)
	}
	if userID != nil {
		rt.UserID = *userID
	}
	return rt, nil
}

// DeleteRefreshToken removes a refresh token row by its hash. Deleting a
// non-existent hash is not an error (logout is idempotent).
func DeleteRefreshToken(ctx context.Context, pool *pgxpool.Pool, tokenHash string) error {
	_, err := pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return fmt.Errorf("db: delete refresh token: %w", err)
	}
	return nil
}
