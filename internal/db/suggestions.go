package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ValidSuggestionStatuses is the suggestion state machine surfaced by the
// dashboard's "Sugerencias" panel: sugerida -> aceptada_en_proceso ->
// terminada, or sugerida -> rechazada.
var ValidSuggestionStatuses = map[string]bool{
	"sugerida":            true,
	"aceptada_en_proceso": true,
	"terminada":           true,
	"rechazada":           true,
}

// Suggestion mirrors a row in the `suggestions` table.
type Suggestion struct {
	ID        string
	AccountID string
	Section   string
	Status    string
	Title     string
	Body      string
	Report    string
	CreatedBy *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PublicSuggestion is the JSON shape returned over the API.
type PublicSuggestion struct {
	ID        string    `json:"id"`
	Section   string    `json:"section"`
	Status    string    `json:"status"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Report    string    `json:"report"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ToPublic converts a Suggestion to its API representation.
func (s Suggestion) ToPublic() PublicSuggestion {
	return PublicSuggestion{
		ID: s.ID, Section: s.Section, Status: s.Status, Title: s.Title,
		Body: s.Body, Report: s.Report, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// CreateSuggestion inserts a new suggestion row.
func CreateSuggestion(ctx context.Context, pool *pgxpool.Pool, s Suggestion) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO suggestions (id, account_id, section, status, title, body, report, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		s.ID, s.AccountID, s.Section, s.Status, s.Title, s.Body, s.Report, s.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("db: create suggestion: %w", err)
	}
	return nil
}

// ListSuggestions returns an account's suggestions, newest first, optionally
// filtered to one section (empty string = every section, for the global
// "Sugerencias" tab).
func ListSuggestions(ctx context.Context, pool *pgxpool.Pool, accountID, section string) ([]Suggestion, error) {
	var rows pgx.Rows
	var err error
	const cols = `id, account_id, section, status, title, body, report, created_by, created_at, updated_at`
	if section == "" {
		rows, err = pool.Query(ctx,
			`SELECT `+cols+` FROM suggestions WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	} else {
		rows, err = pool.Query(ctx,
			`SELECT `+cols+` FROM suggestions WHERE account_id = $1 AND section = $2 ORDER BY created_at DESC`,
			accountID, section)
	}
	if err != nil {
		return nil, fmt.Errorf("db: list suggestions: %w", err)
	}
	defer rows.Close()

	var out []Suggestion
	for rows.Next() {
		var s Suggestion
		if err := rows.Scan(&s.ID, &s.AccountID, &s.Section, &s.Status, &s.Title, &s.Body, &s.Report,
			&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db: scan suggestion: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSuggestion fetches one suggestion, scoped to accountID so one account
// can never read or mutate another's suggestion by guessing an id.
func GetSuggestion(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (Suggestion, error) {
	var s Suggestion
	err := pool.QueryRow(ctx,
		`SELECT id, account_id, section, status, title, body, report, created_by, created_at, updated_at
		 FROM suggestions WHERE id = $1 AND account_id = $2`, id, accountID,
	).Scan(&s.ID, &s.AccountID, &s.Section, &s.Status, &s.Title, &s.Body, &s.Report,
		&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Suggestion{}, ErrNotFound
		}
		return Suggestion{}, fmt.Errorf("db: get suggestion: %w", err)
	}
	return s, nil
}

// UpdateSuggestionStatus sets a suggestion's status.
func UpdateSuggestionStatus(ctx context.Context, pool *pgxpool.Pool, id, status string) error {
	_, err := pool.Exec(ctx, `UPDATE suggestions SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("db: update suggestion status: %w", err)
	}
	return nil
}

// CountSuggestionsThisMonth counts suggestions created for accountID since
// the start of the current UTC month, for the monthly quota check in
// handlers/suggestions.go.
func CountSuggestionsThisMonth(ctx context.Context, pool *pgxpool.Pool, accountID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM suggestions
		 WHERE account_id = $1 AND created_at >= date_trunc('month', now())`, accountID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: count suggestions this month: %w", err)
	}
	return n, nil
}
