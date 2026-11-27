package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AndRho's WhatsApp: linked numbers, link codes, the 24 h window and what
// AndRho waits for from each number (schema.sql, internal/whatsapp).

// SaveWALinkCode stores a link code's hash for a user (their older codes go).
func SaveWALinkCode(ctx context.Context, pool *pgxpool.Pool, codeHash, userID string, expires time.Time) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM wa_link_codes WHERE user_id = $1 OR expires_at < now()`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wa_link_codes (code_hash, user_id, expires_at) VALUES ($1, $2, $3)`, codeHash, userID, expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ConsumeWALinkCode returns the user a code belongs to and deletes it;
// ErrNotFound when it doesn't exist or expired.
func ConsumeWALinkCode(ctx context.Context, pool *pgxpool.Pool, codeHash string) (string, error) {
	var userID string
	err := pool.QueryRow(ctx, `DELETE FROM wa_link_codes WHERE code_hash = $1 AND expires_at > now() RETURNING user_id::text`, codeHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return userID, err
}

// SetUserWhatsApp links phone to a user. Whoever had it before loses it:
// sending the code from the number proves it's theirs now.
func SetUserWhatsApp(ctx context.Context, pool *pgxpool.Pool, userID, phone string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE users SET whatsapp_phone = '', whatsapp_verified_at = NULL WHERE whatsapp_phone = $1 AND id <> $2`, phone, userID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE users SET whatsapp_phone = $2, whatsapp_verified_at = now(), updated_at = now() WHERE id = $1`, userID, phone)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// ClearUserWhatsApp unlinks a user's number.
func ClearUserWhatsApp(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	_, err := pool.Exec(ctx, `UPDATE users SET whatsapp_phone = '', whatsapp_verified_at = NULL, updated_at = now() WHERE id = $1`, userID)
	return err
}

// UserWhatsApp is a user's linked number ("" = none).
func UserWhatsApp(ctx context.Context, pool *pgxpool.Pool, userID string) (string, *time.Time, error) {
	var phone string
	var at *time.Time
	err := pool.QueryRow(ctx, `SELECT whatsapp_phone, whatsapp_verified_at FROM users WHERE id = $1`, userID).Scan(&phone, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return phone, at, err
}

// WhatsAppLinkedUsers is the set of an account's users with a number.
func WhatsAppLinkedUsers(ctx context.Context, pool *pgxpool.Pool, accountID string) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT id::text FROM users WHERE account_id = $1 AND whatsapp_phone <> ''`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// WAUser is the AndRho user behind a WhatsApp number.
type WAUser struct {
	ID, AccountID, Email, DisplayName, Role string
}

// UserByWhatsApp finds the user who linked phone (only active users: an
// invite still pending has no password and can't use the channel).
func UserByWhatsApp(ctx context.Context, pool *pgxpool.Pool, phone string) (WAUser, error) {
	var u WAUser
	err := pool.QueryRow(ctx,
		`SELECT id::text, account_id::text, email, display_name, role FROM users
		 WHERE whatsapp_phone = $1 AND whatsapp_phone <> '' AND password_hash IS NOT NULL AND password_hash <> ''`, phone).
		Scan(&u.ID, &u.AccountID, &u.Email, &u.DisplayName, &u.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// TouchWAInbound records a message from phone to the business number biz
// (” = AndRho's): it opens the 24 h window between the two.
func TouchWAInbound(ctx context.Context, pool *pgxpool.Pool, biz, phone string, at time.Time) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO wa_inbound (business_phone_id, phone, last_at) VALUES ($1, $2, $3)
		 ON CONFLICT (business_phone_id, phone) DO UPDATE SET last_at = GREATEST(wa_inbound.last_at, $3)`, biz, phone, at)
	return err
}

// WALastInbound is phone's last message to biz (nil = never wrote).
func WALastInbound(ctx context.Context, pool *pgxpool.Pool, biz, phone string) (*time.Time, error) {
	var at time.Time
	err := pool.QueryRow(ctx, `SELECT last_at FROM wa_inbound WHERE business_phone_id = $1 AND phone = $2`, biz, phone).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &at, nil
}

// WASession is what AndRho waits for from a number, on a business number
// (Biz ” = AndRho's).
type WASession struct {
	Biz, Phone, AccountID, Kind string
	Data                        json.RawMessage
	ExpiresAt                   time.Time
}

// SaveWASession sets (replaces) phone's pending conversation with s.Biz.
func SaveWASession(ctx context.Context, pool *pgxpool.Pool, s WASession) error {
	if len(s.Data) == 0 {
		s.Data = json.RawMessage(`{}`)
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO wa_sessions (business_phone_id, phone, account_id, kind, data, expires_at) VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6)
		 ON CONFLICT (business_phone_id, phone) DO UPDATE SET account_id = NULLIF($3, '')::uuid, kind = $4, data = $5, expires_at = $6, updated_at = now()`,
		s.Biz, s.Phone, s.AccountID, s.Kind, s.Data, s.ExpiresAt)
	return err
}

// GetWASession reads phone's pending conversation with biz; ErrNotFound
// when there's none or it expired.
func GetWASession(ctx context.Context, pool *pgxpool.Pool, biz, phone string) (WASession, error) {
	s := WASession{Biz: biz, Phone: phone}
	var account *string
	err := pool.QueryRow(ctx, `SELECT account_id::text, kind, data, expires_at FROM wa_sessions
		WHERE business_phone_id = $1 AND phone = $2 AND expires_at > now()`, biz, phone).
		Scan(&account, &s.Kind, &s.Data, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	if account != nil {
		s.AccountID = *account
	}
	return s, err
}

// DeleteWASession forgets phone's pending conversation with biz.
func DeleteWASession(ctx context.Context, pool *pgxpool.Pool, biz, phone string) error {
	_, err := pool.Exec(ctx, `DELETE FROM wa_sessions WHERE business_phone_id = $1 AND phone = $2`, biz, phone)
	return err
}
