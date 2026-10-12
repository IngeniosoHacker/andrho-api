package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenUsage is one row of `token_usage`.
type TokenUsage struct {
	Source       string    `json:"source"`
	AccountID    string    `json:"account_id,omitempty"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	Note         string    `json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// RecordTokenUsage inserts one usage row; empty account means none.
func RecordTokenUsage(ctx context.Context, pool *pgxpool.Pool, u TokenUsage) error {
	var account any
	if u.AccountID != "" {
		account = u.AccountID
	}
	created := u.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO token_usage (source, account_id, model, input_tokens, output_tokens, note, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		u.Source, account, u.Model, u.InputTokens, u.OutputTokens, u.Note, created)
	if err != nil {
		return fmt.Errorf("db: record token usage: %w", err)
	}
	return nil
}
