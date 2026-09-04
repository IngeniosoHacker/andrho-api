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

// CreateUser inserts a new user row. It returns ErrEmailTaken on a
// unique-constraint violation of the email column (mirrors CreateAccount).
func CreateUser(ctx context.Context, pool *pgxpool.Pool, u models.User) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, account_id, email, password_hash, display_name, role, invited_by, invite_token_hash, invite_expires_at)
		 VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9)`,
		u.ID, u.AccountID, u.Email, u.PasswordHash, u.DisplayName, u.Role, u.InvitedBy, u.InviteTokenHash, u.InviteExpiresAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailTaken
		}
		return fmt.Errorf("db: create user: %w", err)
	}
	return nil
}

// GetUserByEmail fetches a user by its email address.
func GetUserByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (models.User, error) {
	return scanUser(pool.QueryRow(ctx,
		`SELECT id, account_id, email, COALESCE(password_hash, ''), display_name, role, invited_by, invite_token_hash, invite_expires_at, created_at, updated_at
		 FROM users WHERE email = $1`, email))
}

// GetUserByID fetches a user by its UUID (as a string).
func GetUserByID(ctx context.Context, pool *pgxpool.Pool, id string) (models.User, error) {
	return scanUser(pool.QueryRow(ctx,
		`SELECT id, account_id, email, COALESCE(password_hash, ''), display_name, role, invited_by, invite_token_hash, invite_expires_at, created_at, updated_at
		 FROM users WHERE id = $1`, id))
}

// GetUserByInviteTokenHash fetches a still-pending user (no password set
// yet) by the SHA-256 hash of its invite token, as long as it hasn't
// expired. Used by POST /auth/accept-invite.
func GetUserByInviteTokenHash(ctx context.Context, pool *pgxpool.Pool, tokenHash string) (models.User, error) {
	return scanUser(pool.QueryRow(ctx,
		`SELECT id, account_id, email, COALESCE(password_hash, ''), display_name, role, invited_by, invite_token_hash, invite_expires_at, created_at, updated_at
		 FROM users
		 WHERE invite_token_hash = $1 AND password_hash IS NULL AND invite_expires_at > now()`, tokenHash))
}

// ListUsersByAccount returns every user belonging to accountID, oldest first
// (the owner, created at signup, sorts first).
func ListUsersByAccount(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]models.User, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, account_id, email, COALESCE(password_hash, ''), display_name, role, invited_by, invite_token_hash, invite_expires_at, created_at, updated_at
		 FROM users WHERE account_id = $1 ORDER BY created_at ASC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("db: list users: %w", err)
	}
	defer rows.Close()

	var out []models.User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUserRole sets a user's role.
func UpdateUserRole(ctx context.Context, pool *pgxpool.Pool, id, role string) error {
	_, err := pool.Exec(ctx, `UPDATE users SET role = $2, updated_at = now() WHERE id = $1`, id, role)
	if err != nil {
		return fmt.Errorf("db: update user role: %w", err)
	}
	return nil
}

// SetUserPassword sets a user's password hash and clears any pending invite
// (used both by accept-invite and, later, a password-reset flow).
func SetUserPassword(ctx context.Context, pool *pgxpool.Pool, id, passwordHash string) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, invite_token_hash = NULL, invite_expires_at = NULL, updated_at = now() WHERE id = $1`,
		id, passwordHash,
	)
	if err != nil {
		return fmt.Errorf("db: set user password: %w", err)
	}
	return nil
}

// DeleteUser removes a user row.
func DeleteUser(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: delete user: %w", err)
	}
	return nil
}

// CountUsersByAccount returns how many users (including pending invites)
// belong to accountID.
func CountUsersByAccount(ctx context.Context, pool *pgxpool.Pool, accountID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE account_id = $1`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: count users: %w", err)
	}
	return n, nil
}

func scanUser(row pgx.Row) (models.User, error) {
	var u models.User
	err := row.Scan(&u.ID, &u.AccountID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role,
		&u.InvitedBy, &u.InviteTokenHash, &u.InviteExpiresAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.User{}, ErrNotFound
		}
		return models.User{}, fmt.Errorf("db: scan user: %w", err)
	}
	return u, nil
}

func scanUserRow(rows pgx.Rows) (models.User, error) {
	var u models.User
	err := rows.Scan(&u.ID, &u.AccountID, &u.Email, &u.PasswordHash, &u.DisplayName, &u.Role,
		&u.InvitedBy, &u.InviteTokenHash, &u.InviteExpiresAt, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

// BackfillOwnerUsers ensures every pre-existing account (from before the
// multi-user migration) has a corresponding 'owner' user, created from its
// legacy email/password_hash. Idempotent -- accounts that already have a
// user are skipped -- so it's safe to call on every boot (see main.go, right
// after db.Migrate).
func BackfillOwnerUsers(ctx context.Context, pool *pgxpool.Pool, newID func() string) (int, error) {
	rows, err := pool.Query(ctx,
		`SELECT a.id, a.email, a.password_hash, a.company_name FROM accounts a
		 WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.account_id = a.id)`)
	if err != nil {
		return 0, fmt.Errorf("db: backfill owner users: query: %w", err)
	}

	type legacyAccount struct{ id, email, passwordHash, companyName string }
	var pending []legacyAccount
	for rows.Next() {
		var a legacyAccount
		if err := rows.Scan(&a.id, &a.email, &a.passwordHash, &a.companyName); err != nil {
			rows.Close()
			return 0, fmt.Errorf("db: backfill owner users: scan: %w", err)
		}
		pending = append(pending, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("db: backfill owner users: rows: %w", err)
	}

	n := 0
	for _, a := range pending {
		u := models.User{
			ID: newID(), AccountID: a.id, Email: a.email, PasswordHash: a.passwordHash,
			DisplayName: a.companyName, Role: "owner",
		}
		if err := CreateUser(ctx, pool, u); err != nil {
			// A duplicate email (another account already has a user with this
			// email) shouldn't happen in practice -- accounts.email was unique
			// too -- but don't let one bad row abort the rest of the backfill.
			if errors.Is(err, ErrEmailTaken) {
				continue
			}
			return n, fmt.Errorf("db: backfill owner users: create user for account %s: %w", a.id, err)
		}
		n++
	}
	return n, nil
}
