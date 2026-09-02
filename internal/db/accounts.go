package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IngeniosoHacker/andrho-api/internal/models"
)

// ErrNotFound is returned by lookups that find no matching row.
var ErrNotFound = errors.New("db: not found")

// ErrEmailTaken is returned when inserting an account whose email already exists.
var ErrEmailTaken = errors.New("db: email already exists")

// CreateAccount inserts a new account row. It returns ErrEmailTaken on a
// unique-constraint violation of the email column.
func CreateAccount(ctx context.Context, pool *pgxpool.Pool, a models.Account) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO accounts (id, email, password_hash, company_name, site_id, odoo_company_id)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		a.ID, a.Email, a.PasswordHash, a.CompanyName, a.SiteID, a.OdooCompanyID,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "accounts_email_key" {
			return ErrEmailTaken
		}
		return fmt.Errorf("db: create account: %w", err)
	}
	return nil
}

// GetAccountByEmail fetches an account by its email address.
func GetAccountByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (models.Account, error) {
	return scanAccount(pool.QueryRow(ctx,
		`SELECT id, email, password_hash, company_name, site_id, odoo_company_id, created_at, updated_at
		 FROM accounts WHERE email = $1`, email))
}

// GetAccountByID fetches an account by its UUID (as a string).
func GetAccountByID(ctx context.Context, pool *pgxpool.Pool, id string) (models.Account, error) {
	return scanAccount(pool.QueryRow(ctx,
		`SELECT id, email, password_hash, company_name, site_id, odoo_company_id, created_at, updated_at
		 FROM accounts WHERE id = $1`, id))
}

// SiteIDExists reports whether a given site_id is already taken.
func SiteIDExists(ctx context.Context, pool *pgxpool.Pool, siteID string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE site_id = $1)`, siteID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("db: site id exists: %w", err)
	}
	return exists, nil
}

func scanAccount(row pgx.Row) (models.Account, error) {
	var a models.Account
	err := row.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.CompanyName, &a.SiteID, &a.OdooCompanyID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.Account{}, ErrNotFound
		}
		return models.Account{}, fmt.Errorf("db: scan account: %w", err)
	}
	return a, nil
}
