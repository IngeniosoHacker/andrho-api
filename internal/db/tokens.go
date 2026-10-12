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

// TokenSummary groups usage for the staff Tokens Usage view.
type TokenSummary struct {
	Days     int          `json:"days"`
	BySource []TokenGroup `json:"by_source"`
	ByModel  []TokenGroup `json:"by_model"`
	Daily    []TokenDaily `json:"daily"`
	Accounts []TokenGroup `json:"accounts"` // top accounts by tokens
	Recent   []TokenUsage `json:"recent"`
}

// TokenGroup is usage summed under one key.
type TokenGroup struct {
	Key          string `json:"key"`
	Calls        int    `json:"calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// TokenDaily is one day of one source.
type TokenDaily struct {
	Day    string `json:"day"`
	Source string `json:"source"`
	Tokens int64  `json:"tokens"`
}

func tokenGroups(ctx context.Context, pool *pgxpool.Pool, q string, days int) ([]TokenGroup, error) {
	rows, err := pool.Query(ctx, q, days)
	if err != nil {
		return nil, fmt.Errorf("db: token groups: %w", err)
	}
	defer rows.Close()
	out := []TokenGroup{}
	for rows.Next() {
		var g TokenGroup
		if err := rows.Scan(&g.Key, &g.Calls, &g.InputTokens, &g.OutputTokens); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetTokenSummary sums the last `days` days of usage.
func GetTokenSummary(ctx context.Context, pool *pgxpool.Pool, days int) (TokenSummary, error) {
	s := TokenSummary{Days: days}
	const sums = `COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)`
	const since = ` WHERE t.created_at >= now() - make_interval(days => $1)`
	var err error
	if s.BySource, err = tokenGroups(ctx, pool, `SELECT source, `+sums+` FROM token_usage t`+since+` GROUP BY 1 ORDER BY SUM(input_tokens + output_tokens) DESC`, days); err != nil {
		return s, err
	}
	if s.ByModel, err = tokenGroups(ctx, pool, `SELECT COALESCE(NULLIF(model, ''), '—'), `+sums+` FROM token_usage t`+since+` GROUP BY 1 ORDER BY SUM(input_tokens + output_tokens) DESC`, days); err != nil {
		return s, err
	}
	if s.Accounts, err = tokenGroups(ctx, pool, `SELECT a.company_name, `+sums+` FROM token_usage t JOIN accounts a ON a.id = t.account_id`+since+` GROUP BY 1 ORDER BY SUM(input_tokens + output_tokens) DESC LIMIT 10`, days); err != nil {
		return s, err
	}
	rows, err := pool.Query(ctx, `SELECT to_char(created_at::date, 'YYYY-MM-DD'), source, SUM(input_tokens + output_tokens)
		FROM token_usage t`+since+` GROUP BY 1, 2 ORDER BY 1`, days)
	if err != nil {
		return s, fmt.Errorf("db: token daily: %w", err)
	}
	defer rows.Close()
	s.Daily = []TokenDaily{}
	for rows.Next() {
		var d TokenDaily
		if err := rows.Scan(&d.Day, &d.Source, &d.Tokens); err != nil {
			return s, err
		}
		s.Daily = append(s.Daily, d)
	}
	rrows, err := pool.Query(ctx, `SELECT source, COALESCE(account_id::text, ''), model, input_tokens, output_tokens, note, created_at
		FROM token_usage ORDER BY created_at DESC LIMIT 30`)
	if err != nil {
		return s, fmt.Errorf("db: token recent: %w", err)
	}
	defer rrows.Close()
	s.Recent = []TokenUsage{}
	for rrows.Next() {
		var u TokenUsage
		if err := rrows.Scan(&u.Source, &u.AccountID, &u.Model, &u.InputTokens, &u.OutputTokens, &u.Note, &u.CreatedAt); err != nil {
			return s, err
		}
		s.Recent = append(s.Recent, u)
	}
	return s, nil
}
