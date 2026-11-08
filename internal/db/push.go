package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PushSubscription mirrors `push_subscriptions`: one browser of one person.
type PushSubscription struct {
	UserID    string
	AccountID string
	Endpoint  string
	P256dh    string
	Auth      string
}

// SavePushSubscription stores (or refreshes) a browser's subscription.
func SavePushSubscription(ctx context.Context, pool *pgxpool.Pool, s PushSubscription) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO push_subscriptions (user_id, account_id, endpoint, p256dh, auth) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (endpoint) DO UPDATE SET user_id = $1, account_id = $2, p256dh = $4, auth = $5, updated_at = now()`,
		s.UserID, s.AccountID, s.Endpoint, s.P256dh, s.Auth)
	if err != nil {
		return fmt.Errorf("db: save push subscription: %w", err)
	}
	return nil
}

// DeletePushSubscription removes one of a person's browsers.
func DeletePushSubscription(ctx context.Context, pool *pgxpool.Pool, userID, endpoint string) error {
	_, err := pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint = $2`, userID, endpoint)
	return err
}

// ListPushSubscriptions lists the browsers of an account's people.
func ListPushSubscriptions(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]PushSubscription, error) {
	rows, err := pool.Query(ctx, `SELECT user_id, account_id, endpoint, p256dh, auth FROM push_subscriptions WHERE account_id = $1`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var s PushSubscription
		if err := rows.Scan(&s.UserID, &s.AccountID, &s.Endpoint, &s.P256dh, &s.Auth); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
