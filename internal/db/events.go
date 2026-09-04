package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountEvent mirrors a row in the `account_events` table -- the audit/
// activity feed backing the dashboard's "Actualizaciones" tab.
type AccountEvent struct {
	ID          string
	AccountID   string
	ActorUserID *string
	Type        string
	Description string
	CreatedAt   time.Time
}

// PublicAccountEvent is the JSON shape returned over the API.
type PublicAccountEvent struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func (e AccountEvent) ToPublic() PublicAccountEvent {
	return PublicAccountEvent{ID: e.ID, Type: e.Type, Description: e.Description, CreatedAt: e.CreatedAt}
}

// LogEvent appends one row to an account's activity feed. Handlers call this
// after a meaningful state change (suggestion accepted, user invited, plan
// changed, ...); a logging failure is swallowed by callers on purpose --
// losing an audit-log entry should never fail the action it's describing.
func LogEvent(ctx context.Context, pool *pgxpool.Pool, id, accountID string, actorUserID *string, eventType, description string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO account_events (id, account_id, actor_user_id, type, description) VALUES ($1, $2, $3, $4, $5)`,
		id, accountID, actorUserID, eventType, description,
	)
	if err != nil {
		return fmt.Errorf("db: log event: %w", err)
	}
	return nil
}

// ListEvents returns an account's activity feed, newest first, paginated.
func ListEvents(ctx context.Context, pool *pgxpool.Pool, accountID string, limit, offset int) ([]AccountEvent, int, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, account_id, actor_user_id, type, description, created_at
		 FROM account_events WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		accountID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("db: list events: %w", err)
	}
	defer rows.Close()

	var out []AccountEvent
	for rows.Next() {
		var e AccountEvent
		if err := rows.Scan(&e.ID, &e.AccountID, &e.ActorUserID, &e.Type, &e.Description, &e.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("db: scan event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	var total int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_events WHERE account_id = $1`, accountID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("db: count events: %w", err)
	}

	return out, total, nil
}
