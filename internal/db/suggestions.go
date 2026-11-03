package db

import (
	"context"
	"encoding/json"
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
	Evidence  string          // technical backing: staff and Creators only
	Explain   json.RawMessage // the "¿Por qué?" view (insights.Explain)
	Action    json.RawMessage // what accepting does (insights.Action)
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
	Body      string          `json:"body"`
	Report    string          `json:"report"`
	Explain   json.RawMessage `json:"explain,omitempty"`
	Action    json.RawMessage `json:"action,omitempty"`
	// Who is doing it, once accepted (filled by the handler).
	Assignment *SuggestionAssignment `json:"assignment,omitempty"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
}

// SuggestionAssignment is the work an accepted suggestion turned into.
type SuggestionAssignment struct {
	Kind        string `json:"kind"` // creator | andrho
	Status      string `json:"status"`
	CreatorName string `json:"creator_name,omitempty"`
	DeliveryURL string `json:"delivery_url,omitempty"`
}

// ToPublic converts a Suggestion to its API representation.
func (s Suggestion) ToPublic() PublicSuggestion {
	return PublicSuggestion{
		ID: s.ID, Section: s.Section, Status: s.Status, Title: s.Title,
		Body: s.Body, Report: s.Report, Explain: nullable(s.Explain), Action: nullable(s.Action),
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

func nullable(j json.RawMessage) json.RawMessage {
	if len(j) == 0 || string(j) == "null" {
		return nil
	}
	return j
}

func jsonArg(j json.RawMessage) any {
	if len(j) == 0 {
		return nil
	}
	return []byte(j)
}

const suggestionCols = `id, account_id, section, status, title, body, report, evidence, explain, action, created_by, created_at, updated_at`

func scanSuggestion(row pgx.Row) (Suggestion, error) {
	var s Suggestion
	var ex, act []byte
	err := row.Scan(&s.ID, &s.AccountID, &s.Section, &s.Status, &s.Title, &s.Body, &s.Report, &s.Evidence, &ex, &act,
		&s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	s.Explain, s.Action = ex, act
	return s, err
}

// CreateSuggestion inserts a new suggestion row and fills in s's DB-generated
// created_at/updated_at before returning it (see CreateUser's doc comment for
// why -- same reasoning).
func CreateSuggestion(ctx context.Context, pool *pgxpool.Pool, s Suggestion) (Suggestion, error) {
	err := pool.QueryRow(ctx,
		`INSERT INTO suggestions (id, account_id, section, status, title, body, report, evidence, explain, action, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING created_at, updated_at`,
		s.ID, s.AccountID, s.Section, s.Status, s.Title, s.Body, s.Report, s.Evidence, jsonArg(s.Explain), jsonArg(s.Action), s.CreatedBy,
	).Scan(&s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return Suggestion{}, fmt.Errorf("db: create suggestion: %w", err)
	}
	return s, nil
}

// ListSuggestions returns an account's suggestions, newest first, optionally
// filtered to one section (empty string = every section, for the global
// "Sugerencias" tab).
func ListSuggestions(ctx context.Context, pool *pgxpool.Pool, accountID, section string) ([]Suggestion, error) {
	var rows pgx.Rows
	var err error
	const cols = suggestionCols
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
		s, err := scanSuggestion(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan suggestion: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSuggestion fetches one suggestion, scoped to accountID so one account
// can never read or mutate another's suggestion by guessing an id.
func GetSuggestion(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (Suggestion, error) {
	s, err := scanSuggestion(pool.QueryRow(ctx,
		`SELECT `+suggestionCols+` FROM suggestions WHERE id = $1 AND account_id = $2`, id, accountID))
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

// FinishSuggestion closes a suggestion with the outcome told to the owner.
func FinishSuggestion(ctx context.Context, pool *pgxpool.Pool, id, report string) error {
	_, err := pool.Exec(ctx, `UPDATE suggestions SET status = 'terminada', report = $2, updated_at = now() WHERE id = $1`, id, report)
	if err != nil {
		return fmt.Errorf("db: finish suggestion: %w", err)
	}
	return nil
}

// SuggestionAssignments returns, per suggestion id, the Creator task it
// became (latest one), for the panel's "Asignada a ..." line.
func SuggestionAssignments(ctx context.Context, pool *pgxpool.Pool, accountID string) (map[string]SuggestionAssignment, error) {
	rows, err := pool.Query(ctx,
		`SELECT DISTINCT ON (t.suggestion_id) t.suggestion_id::text, t.status, COALESCE(u.display_name, ''), t.delivery_url
		 FROM creator_tasks t LEFT JOIN users u ON u.id = t.creator_id
		 WHERE t.account_id = $1 AND t.suggestion_id IS NOT NULL
		 ORDER BY t.suggestion_id, t.created_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("db: suggestion assignments: %w", err)
	}
	defer rows.Close()
	out := map[string]SuggestionAssignment{}
	for rows.Next() {
		var id string
		var a SuggestionAssignment
		if err := rows.Scan(&id, &a.Status, &a.CreatorName, &a.DeliveryURL); err != nil {
			return nil, err
		}
		a.Kind = "creator"
		out[id] = a
	}
	return out, rows.Err()
}

// CountSuggestionsThisMonth counts suggestions created for accountID since
// the start of the current UTC month, for the monthly quota check in
// handlers/suggestions.go.
func CountSuggestionsThisMonth(ctx context.Context, pool *pgxpool.Pool, accountID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM suggestions
		 WHERE account_id = $1 AND created_at >= date_trunc('month', now())
		   AND COALESCE(action->>'type', '') <> 'erp_install'`, accountID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: count suggestions this month: %w", err)
	}
	return n, nil
}
