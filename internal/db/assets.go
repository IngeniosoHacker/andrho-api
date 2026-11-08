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

// Asset mirrors `account_assets` (Data only when read with GetAsset).
type Asset struct {
	ID          string          `json:"id"`
	AccountID   string          `json:"-"`
	Kind        string          `json:"kind"` // logo | brand_image | data
	Filename    string          `json:"filename"`
	ContentType string          `json:"content_type"`
	Size        int             `json:"size"`
	Status      string          `json:"status"`
	Meta        json.RawMessage `json:"meta"`
	CreatedAt   time.Time       `json:"created_at"`
	Data        []byte          `json:"-"`
}

// MaxAssets is the most files one account keeps.
const MaxAssets = 40

const assetCols = `id, account_id, kind, filename, content_type, size, status, meta, created_at`

func scanAsset(row pgx.Row, withData bool) (Asset, error) {
	var a Asset
	dest := []any{&a.ID, &a.AccountID, &a.Kind, &a.Filename, &a.ContentType, &a.Size, &a.Status, &a.Meta, &a.CreatedAt}
	if withData {
		dest = append(dest, &a.Data)
	}
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// CreateAsset stores an upload.
func CreateAsset(ctx context.Context, pool *pgxpool.Pool, a Asset, uploadedBy *string) (Asset, error) {
	if len(a.Meta) == 0 {
		a.Meta = json.RawMessage(`{}`)
	}
	a.Size = len(a.Data)
	err := pool.QueryRow(ctx,
		`INSERT INTO account_assets (id, account_id, kind, filename, content_type, size, data, status, meta, uploaded_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING created_at`,
		a.ID, a.AccountID, a.Kind, a.Filename, a.ContentType, a.Size, a.Data, a.Status, a.Meta, uploadedBy).Scan(&a.CreatedAt)
	if err != nil {
		return a, fmt.Errorf("db: create asset: %w", err)
	}
	return a, nil
}

// CountAssets counts an account's files.
func CountAssets(ctx context.Context, pool *pgxpool.Pool, accountID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM account_assets WHERE account_id = $1`, accountID).Scan(&n)
	return n, err
}

// ListAssets lists an account's files without their bytes; kinds filters
// (none = all).
func ListAssets(ctx context.Context, pool *pgxpool.Pool, accountID string, kinds ...string) ([]Asset, error) {
	q := `SELECT ` + assetCols + ` FROM account_assets WHERE account_id = $1`
	args := []any{accountID}
	if len(kinds) > 0 {
		q += ` AND kind = ANY($2)`
		args = append(args, kinds)
	}
	rows, err := pool.Query(ctx, q+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("db: list assets: %w", err)
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		a, err := scanAsset(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAsset reads one of an account's files, bytes included.
func GetAsset(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (Asset, error) {
	return scanAsset(pool.QueryRow(ctx, `SELECT `+assetCols+`, data FROM account_assets WHERE account_id = $1 AND id = $2`, accountID, id), true)
}

// DeleteAsset removes one of an account's files.
func DeleteAsset(ctx context.Context, pool *pgxpool.Pool, accountID, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM account_assets WHERE account_id = $1 AND id = $2`, accountID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ClaimAsset moves a file from one status to another, only if it is still
// in `from` (so two runs never load the same data twice).
func ClaimAsset(ctx context.Context, pool *pgxpool.Pool, id, from, to string) (bool, error) {
	tag, err := pool.Exec(ctx, `UPDATE account_assets SET status = $3, updated_at = now() WHERE id = $1 AND status = $2`, id, from, to)
	return err == nil && tag.RowsAffected() == 1, err
}

// UpdateAsset sets a file's status and meta.
func UpdateAsset(ctx context.Context, pool *pgxpool.Pool, id, status string, meta json.RawMessage) error {
	_, err := pool.Exec(ctx, `UPDATE account_assets SET status = $2, meta = $3, updated_at = now() WHERE id = $1`, id, status, meta)
	return err
}

// SetSetupLogo replaces the logo the site and the panel show.
func SetSetupLogo(ctx context.Context, pool *pgxpool.Pool, accountID, contentType string, data []byte) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO account_setup (account_id, logo_type, logo) VALUES ($1, $2, $3)
		 ON CONFLICT (account_id) DO UPDATE SET logo_type = $2, logo = $3, updated_at = now()`, accountID, contentType, data)
	return err
}

// GetKit reads the account's kit (account_setup.kit; {} when none).
func GetKit(ctx context.Context, pool *pgxpool.Pool, accountID string) (json.RawMessage, error) {
	var kit json.RawMessage
	err := pool.QueryRow(ctx, `SELECT kit FROM account_setup WHERE account_id = $1`, accountID).Scan(&kit)
	if errors.Is(err, pgx.ErrNoRows) {
		return json.RawMessage(`{}`), nil
	}
	return kit, err
}

// SetKit stores the account's kit.
func SetKit(ctx context.Context, pool *pgxpool.Pool, accountID string, kit json.RawMessage) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO account_setup (account_id, kit) VALUES ($1, $2)
		 ON CONFLICT (account_id) DO UPDATE SET kit = $2, updated_at = now()`, accountID, kit)
	return err
}

// OpenTaskOfKind finds an account's task of kind that isn't delivered or
// cancelled ("" when none).
func OpenTaskOfKind(ctx context.Context, pool *pgxpool.Pool, accountID, kind string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id FROM creator_tasks WHERE account_id = $1 AND kind = $2 AND status IN ('abierta', 'aceptada')
		ORDER BY created_at DESC LIMIT 1`, accountID, kind).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}
