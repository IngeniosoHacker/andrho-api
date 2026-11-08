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

// AccountPlan mirrors `account_plan`: what the account bought at checkout.
type AccountPlan struct {
	AccountID       string          `json:"-"`
	Plan            string          `json:"plan"`
	Addons          json.RawMessage `json:"addons"` // [{feature_id, qty}]
	BasePrice       int             `json:"base_price"`
	AddonsTotal     int             `json:"addons_total"`
	Total           int             `json:"total"`
	SetupTotal      int             `json:"setup_total"` // charged once (the website's construction)
	Currency        string          `json:"currency"`
	BillingStatus   string          `json:"billing_status"`
	PaymentSkipped  bool            `json:"payment_skipped"`
	PaymentProvider string          `json:"payment_provider"`
	PaymentRef      string          `json:"-"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

const accountPlanCols = `account_id, plan, addons, base_price, addons_total, total, currency, billing_status,
  payment_skipped, payment_provider, payment_ref, created_at, updated_at, setup_total`

func scanAccountPlan(row pgx.Row) (AccountPlan, error) {
	var p AccountPlan
	err := row.Scan(&p.AccountID, &p.Plan, &p.Addons, &p.BasePrice, &p.AddonsTotal, &p.Total, &p.Currency, &p.BillingStatus,
		&p.PaymentSkipped, &p.PaymentProvider, &p.PaymentRef, &p.CreatedAt, &p.UpdatedAt, &p.SetupTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// SaveAccountPlan creates or replaces the account's purchase.
func SaveAccountPlan(ctx context.Context, pool *pgxpool.Pool, p AccountPlan) error {
	if len(p.Addons) == 0 {
		p.Addons = json.RawMessage(`[]`)
	}
	if p.Currency == "" {
		p.Currency = "GTQ"
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO account_plan (account_id, plan, addons, base_price, addons_total, total, currency, billing_status,
		   payment_skipped, payment_provider, payment_ref, setup_total)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (account_id) DO UPDATE SET plan = EXCLUDED.plan, addons = EXCLUDED.addons, base_price = EXCLUDED.base_price,
		   addons_total = EXCLUDED.addons_total, total = EXCLUDED.total, currency = EXCLUDED.currency,
		   billing_status = EXCLUDED.billing_status, payment_skipped = EXCLUDED.payment_skipped,
		   payment_provider = EXCLUDED.payment_provider, payment_ref = EXCLUDED.payment_ref,
		   setup_total = EXCLUDED.setup_total, updated_at = now()`,
		p.AccountID, p.Plan, p.Addons, p.BasePrice, p.AddonsTotal, p.Total, p.Currency, p.BillingStatus,
		p.PaymentSkipped, p.PaymentProvider, p.PaymentRef, p.SetupTotal)
	if err != nil {
		return fmt.Errorf("db: save account plan: %w", err)
	}
	return nil
}

// GetAccountPlan returns the account's purchase (ErrNotFound for accounts
// created before checkout existed).
func GetAccountPlan(ctx context.Context, pool *pgxpool.Pool, accountID string) (AccountPlan, error) {
	return scanAccountPlan(pool.QueryRow(ctx, `SELECT `+accountPlanCols+` FROM account_plan WHERE account_id = $1`, accountID))
}

// SetAccountPlanPlan records a plan change (PATCH /account/plan) on the
// purchase, keeping the add-ons. Nothing is charged.
func SetAccountPlanPlan(ctx context.Context, pool *pgxpool.Pool, accountID, plan string, basePrice int) error {
	_, err := pool.Exec(ctx,
		`UPDATE account_plan SET plan = $2, base_price = $3, total = $3 + addons_total, updated_at = now() WHERE account_id = $1`,
		accountID, plan, basePrice)
	if err != nil {
		return fmt.Errorf("db: set account plan: %w", err)
	}
	return nil
}

// PaymentSkippedAccount is one account created without paying.
type PaymentSkippedAccount struct {
	AccountID   string          `json:"account_id"`
	SiteID      string          `json:"site_id"`
	CompanyName string          `json:"company_name"`
	Email       string          `json:"email"`
	Plan        string          `json:"plan"`
	Addons      json.RawMessage `json:"addons"`
	Total       int             `json:"total"`
	CreatedAt   time.Time       `json:"created_at"`
}

// PaymentSkippedQuery lists every account created while payments were off
// (todo.md C5.3), to charge or migrate them once a PaymentProvider exists.
// Run it as is in psql, or through ListPaymentSkipped / GET /admin/billing/skipped.
const PaymentSkippedQuery = `SELECT a.id, a.site_id, a.company_name, a.email, p.plan, p.addons, p.total, p.created_at
  FROM account_plan p JOIN accounts a ON a.id = p.account_id
 WHERE p.payment_skipped AND p.billing_status = 'pending_payment'
 ORDER BY p.created_at`

// ListPaymentSkipped runs PaymentSkippedQuery.
func ListPaymentSkipped(ctx context.Context, pool *pgxpool.Pool) ([]PaymentSkippedAccount, error) {
	rows, err := pool.Query(ctx, PaymentSkippedQuery)
	if err != nil {
		return nil, fmt.Errorf("db: list payment skipped: %w", err)
	}
	defer rows.Close()
	out := []PaymentSkippedAccount{}
	for rows.Next() {
		var a PaymentSkippedAccount
		if err := rows.Scan(&a.AccountID, &a.SiteID, &a.CompanyName, &a.Email, &a.Plan, &a.Addons, &a.Total, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountCreatorTasksThisMonth counts the account's Creator tasks created this
// calendar month against its monthly quota: not the website build (part of
// the site) and not cancelled ones.
func CountCreatorTasksThisMonth(ctx context.Context, pool *pgxpool.Pool, accountID string) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM creator_tasks WHERE account_id = $1 AND kind <> 'website' AND status <> 'cancelada'
		   AND created_at >= date_trunc('month', now())`, accountID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("db: count creator tasks: %w", err)
	}
	return n, nil
}
