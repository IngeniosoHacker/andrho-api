package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each store's own WhatsApp number (schema.sql: whatsapp_accounts,
// whatsapp_templates). Token and PIN arrive and leave sealed.

// WhatsApp account statuses.
const (
	WAPending      = "pendiente"
	WAActive       = "activo"
	WANoPayment    = "sin_pago"
	WADisconnected = "desconectado"
)

// WhatsAppAccount mirrors whatsapp_accounts.
type WhatsAppAccount struct {
	ID, AccountID, WABAID, PhoneNumberID, BusinessID string
	DisplayPhoneNumber, VerifiedName, NameStatus     string
	QualityRating                                    string
	Coexistence                                      bool
	Status, StatusDetail                             string
	Token, PIN                                       []byte // sealed
	SyncedAt, CheckedAt, DisconnectedAt              *time.Time
	ConnectedAt                                      time.Time
}

// Usable reports whether messages may leave from this number (sin_pago
// still answers inside the window: only templates are paid).
func (w WhatsAppAccount) Usable() bool {
	return (w.Status == WAActive || w.Status == WANoPayment) && len(w.Token) > 0
}

const waAccountCols = `id::text, account_id::text, waba_id, phone_number_id, business_id, display_phone_number, verified_name,
	name_status, quality_rating, coexistence, status, status_detail, token, pin, synced_at, checked_at, disconnected_at, connected_at`

func scanWAAccount(row pgx.Row) (WhatsAppAccount, error) {
	var w WhatsAppAccount
	err := row.Scan(&w.ID, &w.AccountID, &w.WABAID, &w.PhoneNumberID, &w.BusinessID, &w.DisplayPhoneNumber, &w.VerifiedName,
		&w.NameStatus, &w.QualityRating, &w.Coexistence, &w.Status, &w.StatusDetail, &w.Token, &w.PIN, &w.SyncedAt, &w.CheckedAt,
		&w.DisconnectedAt, &w.ConnectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// SaveWhatsAppAccount connects a number to an account: the account's
// earlier number (and this number's earlier owner) end disconnected, and
// their tokens are erased.
func SaveWhatsAppAccount(ctx context.Context, pool *pgxpool.Pool, w WhatsAppAccount, connectedBy string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE whatsapp_accounts SET status = 'desconectado', token = NULL, pin = NULL,
		disconnected_at = now(), updated_at = now() WHERE status <> 'desconectado' AND (account_id = $1 OR phone_number_id = $2)`,
		w.AccountID, w.PhoneNumberID); err != nil {
		return fmt.Errorf("db: whatsapp account: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO whatsapp_accounts (id, account_id, waba_id, phone_number_id, business_id, display_phone_number,
		verified_name, name_status, quality_rating, coexistence, status, status_detail, token, pin, synced_at, checked_at, connected_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now(), NULLIF($16, '')::uuid)`,
		w.ID, w.AccountID, w.WABAID, w.PhoneNumberID, w.BusinessID, w.DisplayPhoneNumber, w.VerifiedName, w.NameStatus,
		w.QualityRating, w.Coexistence, w.Status, w.StatusDetail, w.Token, w.PIN, w.SyncedAt, connectedBy); err != nil {
		return fmt.Errorf("db: whatsapp account: %w", err)
	}
	return tx.Commit(ctx)
}

// WhatsAppAccountFor is an account's number (not disconnected).
func WhatsAppAccountFor(ctx context.Context, pool *pgxpool.Pool, accountID string) (WhatsAppAccount, error) {
	return scanWAAccount(pool.QueryRow(ctx, `SELECT `+waAccountCols+` FROM whatsapp_accounts
		WHERE account_id = $1 AND status <> 'desconectado'`, accountID))
}

// WhatsAppAccountByPhone is the number a webhook names (not disconnected).
func WhatsAppAccountByPhone(ctx context.Context, pool *pgxpool.Pool, phoneNumberID string) (WhatsAppAccount, error) {
	return scanWAAccount(pool.QueryRow(ctx, `SELECT `+waAccountCols+` FROM whatsapp_accounts
		WHERE phone_number_id = $1 AND status <> 'desconectado'`, phoneNumberID))
}

// WhatsAppAccountsByWABA are the numbers of a WABA (not disconnected).
func WhatsAppAccountsByWABA(ctx context.Context, pool *pgxpool.Pool, wabaID string) ([]WhatsAppAccount, error) {
	return queryWAAccounts(ctx, pool, `SELECT `+waAccountCols+` FROM whatsapp_accounts WHERE waba_id = $1 AND status <> 'desconectado'`, wabaID)
}

// WhatsAppAccountsToCheck are the connected numbers not read from Meta in
// the last `every`.
func WhatsAppAccountsToCheck(ctx context.Context, pool *pgxpool.Pool, every time.Duration) ([]WhatsAppAccount, error) {
	return queryWAAccounts(ctx, pool, `SELECT `+waAccountCols+` FROM whatsapp_accounts WHERE status <> 'desconectado'
		AND (checked_at IS NULL OR checked_at < $1) ORDER BY checked_at NULLS FIRST LIMIT 100`, time.Now().Add(-every))
}

func queryWAAccounts(ctx context.Context, pool *pgxpool.Pool, q string, arg any) ([]WhatsAppAccount, error) {
	rows, err := pool.Query(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WhatsAppAccount{}
	for rows.Next() {
		w, err := scanWAAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// UpdateWhatsAppInfo stores what Meta says of the number now.
func UpdateWhatsAppInfo(ctx context.Context, pool *pgxpool.Pool, id, display, verifiedName, nameStatus, quality string) error {
	_, err := pool.Exec(ctx, `UPDATE whatsapp_accounts SET display_phone_number = COALESCE(NULLIF($2, ''), display_phone_number),
		verified_name = COALESCE(NULLIF($3, ''), verified_name), name_status = COALESCE(NULLIF($4, ''), name_status),
		quality_rating = COALESCE(NULLIF($5, ''), quality_rating), checked_at = now(), updated_at = now() WHERE id = $1`,
		id, display, verifiedName, nameStatus, quality)
	return err
}

// SetWhatsAppStatus moves a connected number between activo and sin_pago
// (or pendiente). It never revives a disconnected one.
func SetWhatsAppStatus(ctx context.Context, pool *pgxpool.Pool, id, status, detail string) error {
	_, err := pool.Exec(ctx, `UPDATE whatsapp_accounts SET status = $2, status_detail = $3, updated_at = now()
		WHERE id = $1 AND status <> 'desconectado'`, id, status, detail)
	return err
}

// DisconnectWhatsApp ends a number's connection and erases its secrets.
func DisconnectWhatsApp(ctx context.Context, pool *pgxpool.Pool, id, detail string) error {
	_, err := pool.Exec(ctx, `UPDATE whatsapp_accounts SET status = 'desconectado', status_detail = $2, token = NULL, pin = NULL,
		disconnected_at = now(), updated_at = now() WHERE id = $1`, id, detail)
	return err
}

// MarkWhatsAppSynced records Coexistence's syncs as asked.
func MarkWhatsAppSynced(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `UPDATE whatsapp_accounts SET synced_at = now(), updated_at = now() WHERE id = $1`, id)
	return err
}

// WhatsAppTemplate mirrors whatsapp_templates.
type WhatsAppTemplate struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	MetaID   string `json:"-"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
}

// SaveWhatsAppTemplate inserts or updates a template of a number.
func SaveWhatsAppTemplate(ctx context.Context, pool *pgxpool.Pool, waAccountID string, t WhatsAppTemplate) error {
	_, err := pool.Exec(ctx, `INSERT INTO whatsapp_templates (wa_account_id, name, language, meta_id, category, status, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (wa_account_id, name, language) DO UPDATE SET meta_id = COALESCE(NULLIF($4, ''), whatsapp_templates.meta_id),
		  category = COALESCE(NULLIF($5, ''), whatsapp_templates.category), status = $6, reason = $7, updated_at = now()`,
		waAccountID, t.Name, t.Language, t.MetaID, t.Category, t.Status, t.Reason)
	return err
}

// SetTemplateStatusByWABA applies a message_template_status_update to the
// connected numbers of that WABA.
func SetTemplateStatusByWABA(ctx context.Context, pool *pgxpool.Pool, wabaID, name, language, status, reason string) error {
	_, err := pool.Exec(ctx, `UPDATE whatsapp_templates t SET status = $4, reason = $5, updated_at = now()
		FROM whatsapp_accounts a WHERE t.wa_account_id = a.id AND a.waba_id = $1 AND a.status <> 'desconectado'
		AND t.name = $2 AND ($3 = '' OR t.language = $3)`, wabaID, name, language, status, reason)
	return err
}

// WhatsAppTemplates lists a number's templates.
func WhatsAppTemplates(ctx context.Context, pool *pgxpool.Pool, waAccountID string) ([]WhatsAppTemplate, error) {
	rows, err := pool.Query(ctx, `SELECT name, language, meta_id, category, status, reason FROM whatsapp_templates
		WHERE wa_account_id = $1 ORDER BY name`, waAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WhatsAppTemplate{}
	for rows.Next() {
		var t WhatsAppTemplate
		if err := rows.Scan(&t.Name, &t.Language, &t.MetaID, &t.Category, &t.Status, &t.Reason); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// WhatsAppTemplateApproved reports whether a number may send that template.
func WhatsAppTemplateApproved(ctx context.Context, pool *pgxpool.Pool, waAccountID, name, language string) bool {
	var ok bool
	_ = pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM whatsapp_templates WHERE wa_account_id = $1 AND name = $2
		AND language = $3 AND status = 'APPROVED')`, waAccountID, name, language).Scan(&ok)
	return ok
}

// WhatsAppJob is the account's "whatsapp" provisioning job; ErrNotFound
// when it has none.
func WhatsAppJob(ctx context.Context, pool *pgxpool.Pool, accountID string) (ProvisioningJob, error) {
	j, err := scanJob(pool.QueryRow(ctx, `SELECT `+jobCols+` FROM provisioning_jobs WHERE account_id = $1 AND kind = 'whatsapp'
		ORDER BY created_at DESC LIMIT 1`, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// MoveWhatsAppJobsToClient: with Embedded Signup on, whatsapp jobs that
// waited for Meta (or the VPS) now wait for the store to connect.
func MoveWhatsAppJobsToClient(ctx context.Context, pool *pgxpool.Pool, status, detail string) (int64, error) {
	tag, err := pool.Exec(ctx, `UPDATE provisioning_jobs SET status = $1, detail = $2, progress = 0, updated_at = now()
		WHERE kind = 'whatsapp' AND status IN ('esperando_meta', 'esperando_vps', 'pendiente', 'error')`, status, detail)
	return tag.RowsAffected(), err
}
