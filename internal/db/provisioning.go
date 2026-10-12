package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------
// Staff (AndRho's own team)
// ---------------------------------------------------------------------

// SyncStaff makes the users with these emails staff with role (admin or
// creator). Emails without a user yet are skipped; they're picked up on a
// later boot. Returns how many were set.
func SyncStaff(ctx context.Context, pool *pgxpool.Pool, role string, emails []string) (int, error) {
	n := 0
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		tag, err := pool.Exec(ctx,
			`INSERT INTO staff (user_id, role) SELECT id, $2 FROM users WHERE email = $1
			 ON CONFLICT (user_id) DO UPDATE SET role = EXCLUDED.role`, e, role)
		if err != nil {
			return n, fmt.Errorf("db: sync staff: %w", err)
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// StaffRole is the user's AndRho staff role, or "".
func StaffRole(ctx context.Context, pool *pgxpool.Pool, userID string) (string, error) {
	var role string
	err := pool.QueryRow(ctx, `SELECT role FROM staff WHERE user_id = $1`, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// FirstStaffUser returns a user id with that staff role (the oldest), or "".
func FirstStaffUser(ctx context.Context, pool *pgxpool.Pool, role string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT user_id FROM staff WHERE role = $1 ORDER BY created_at LIMIT 1`, role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ---------------------------------------------------------------------
// Account setup (what the signup learned)
// ---------------------------------------------------------------------

// AccountSetup mirrors `account_setup`.
type AccountSetup struct {
	AccountID    string          `json:"-"`
	Setup        string          `json:"setup"`
	Answers      json.RawMessage `json:"answers"`
	Description  string          `json:"description"`
	Modules      []string        `json:"modules"`
	Phone        string          `json:"phone"`
	BrandEssence string          `json:"brand_essence"`
	BrandHelp    bool            `json:"brand_help"`
	HasLogo      bool            `json:"has_logo"`
	LogoType     string          `json:"-"`
	Logo         []byte          `json:"-"`
}

// SaveAccountSetup inserts or replaces the account's setup.
func SaveAccountSetup(ctx context.Context, pool *pgxpool.Pool, s AccountSetup) error {
	if len(s.Answers) == 0 {
		s.Answers = json.RawMessage(`{}`)
	}
	var logo any
	if len(s.Logo) > 0 {
		logo = s.Logo
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO account_setup (account_id, setup, answers, description, modules, phone, brand_essence, brand_help, logo_type, logo)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 ON CONFLICT (account_id) DO UPDATE SET setup = $2, answers = $3, description = $4, modules = $5, phone = $6,
		   brand_essence = $7, brand_help = $8, logo_type = $9, logo = $10, updated_at = now()`,
		s.AccountID, s.Setup, s.Answers, s.Description, s.Modules, s.Phone, s.BrandEssence, s.BrandHelp, s.LogoType, logo)
	if err != nil {
		return fmt.Errorf("db: save account setup: %w", err)
	}
	return nil
}

// GetAccountSetup reads the setup, logo bytes included.
func GetAccountSetup(ctx context.Context, pool *pgxpool.Pool, accountID string) (AccountSetup, error) {
	s := AccountSetup{AccountID: accountID}
	err := pool.QueryRow(ctx,
		`SELECT setup, answers, description, modules, phone, brand_essence, brand_help, logo_type, COALESCE(logo, '')
		 FROM account_setup WHERE account_id = $1`, accountID,
	).Scan(&s.Setup, &s.Answers, &s.Description, &s.Modules, &s.Phone, &s.BrandEssence, &s.BrandHelp, &s.LogoType, &s.Logo)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	s.HasLogo = len(s.Logo) > 0
	return s, err
}

// ---------------------------------------------------------------------
// Provisioning jobs
// ---------------------------------------------------------------------

// ProvisioningJob mirrors `provisioning_jobs`.
type ProvisioningJob struct {
	ID        string          `json:"id"`
	AccountID string          `json:"-"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status"`
	Detail    string          `json:"detail"`
	Input     json.RawMessage `json:"input"`
	Result    json.RawMessage `json:"result"`
	Attempts  int             `json:"attempts"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

const jobCols = `id, account_id, kind, status, detail, input, result, attempts, created_at, updated_at`

func scanJob(row pgx.Row) (ProvisioningJob, error) {
	var j ProvisioningJob
	err := row.Scan(&j.ID, &j.AccountID, &j.Kind, &j.Status, &j.Detail, &j.Input, &j.Result, &j.Attempts, &j.CreatedAt, &j.UpdatedAt)
	return j, err
}

// CreateProvisioningJob inserts a job.
func CreateProvisioningJob(ctx context.Context, pool *pgxpool.Pool, j ProvisioningJob) (ProvisioningJob, error) {
	if len(j.Input) == 0 {
		j.Input = json.RawMessage(`{}`)
	}
	out, err := scanJob(pool.QueryRow(ctx,
		`INSERT INTO provisioning_jobs (id, account_id, kind, status, detail, input) VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+jobCols, j.ID, j.AccountID, j.Kind, j.Status, j.Detail, j.Input))
	if err != nil {
		return out, fmt.Errorf("db: create provisioning job: %w", err)
	}
	return out, nil
}

// UpdateProvisioningJob sets status, detail and (when not nil) result,
// counting an attempt when the job ran.
func UpdateProvisioningJob(ctx context.Context, pool *pgxpool.Pool, id, status, detail string, result json.RawMessage, attempted bool) error {
	if result == nil {
		result = json.RawMessage(`null`)
	}
	_, err := pool.Exec(ctx,
		`UPDATE provisioning_jobs SET status = $2, detail = $3,
		   result = CASE WHEN $4::jsonb = 'null'::jsonb THEN result ELSE $4::jsonb END,
		   attempts = attempts + CASE WHEN $5 THEN 1 ELSE 0 END, updated_at = now()
		 WHERE id = $1`, id, status, detail, string(result), attempted)
	if err != nil {
		return fmt.Errorf("db: update provisioning job: %w", err)
	}
	return nil
}

// ListProvisioningJobs returns an account's jobs, oldest first.
func ListProvisioningJobs(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]ProvisioningJob, error) {
	return queryJobs(ctx, pool, `SELECT `+jobCols+` FROM provisioning_jobs WHERE account_id = $1 ORDER BY created_at`, accountID)
}

// JobsWaiting returns jobs in one of these statuses (for the retry loop).
func JobsWaiting(ctx context.Context, pool *pgxpool.Pool, statuses []string) ([]ProvisioningJob, error) {
	return queryJobs(ctx, pool, `SELECT `+jobCols+` FROM provisioning_jobs WHERE status = ANY($1) ORDER BY created_at LIMIT 50`, statuses)
}

func queryJobs(ctx context.Context, pool *pgxpool.Pool, q string, arg any) ([]ProvisioningJob, error) {
	rows, err := pool.Query(ctx, q, arg)
	if err != nil {
		return nil, fmt.Errorf("db: list provisioning jobs: %w", err)
	}
	defer rows.Close()
	out := []ProvisioningJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------
// Odoo connections
// ---------------------------------------------------------------------

// OdooConnection mirrors `odoo_connections` (Secret is the sealed password).
type OdooConnection struct {
	AccountID string
	URL       string
	DBName    string
	Login     string
	Secret    []byte
}

// SaveOdooConnection inserts or replaces the account's Odoo access.
func SaveOdooConnection(ctx context.Context, pool *pgxpool.Pool, c OdooConnection) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO odoo_connections (account_id, url, db_name, login, secret) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (account_id) DO UPDATE SET url = $2, db_name = $3, login = $4, secret = $5, updated_at = now()`,
		c.AccountID, c.URL, c.DBName, c.Login, c.Secret)
	if err != nil {
		return fmt.Errorf("db: save odoo connection: %w", err)
	}
	return nil
}

// GetOdooConnection reads the account's Odoo access.
func GetOdooConnection(ctx context.Context, pool *pgxpool.Pool, accountID string) (OdooConnection, error) {
	c := OdooConnection{AccountID: accountID}
	err := pool.QueryRow(ctx, `SELECT url, db_name, login, secret FROM odoo_connections WHERE account_id = $1`, accountID).
		Scan(&c.URL, &c.DBName, &c.Login, &c.Secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ---------------------------------------------------------------------
// Creator tasks
// ---------------------------------------------------------------------

// CreatorTask mirrors `creator_tasks`.
type CreatorTask struct {
	ID          string          `json:"id"`
	AccountID   string          `json:"account_id"`
	CompanyName string          `json:"company_name,omitempty"`
	Kind        string          `json:"kind"`
	Title       string          `json:"title"`
	Brief       json.RawMessage `json:"brief"`
	Status      string          `json:"status"`
	CreatorID   *string         `json:"creator_id"`
	DeliveryURL string          `json:"delivery_url"`
	Notes       string          `json:"notes"`
	DueAt       *time.Time      `json:"due_at"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

const taskCols = `t.id, t.account_id, a.company_name, t.kind, t.title, t.brief, t.status, t.creator_id, t.delivery_url, t.notes, t.due_at, t.created_at, t.updated_at`

func scanTask(row pgx.Row) (CreatorTask, error) {
	var t CreatorTask
	err := row.Scan(&t.ID, &t.AccountID, &t.CompanyName, &t.Kind, &t.Title, &t.Brief, &t.Status, &t.CreatorID,
		&t.DeliveryURL, &t.Notes, &t.DueAt, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// CreateCreatorTask inserts a task; creatorID may be "" (open to any creator).
func CreateCreatorTask(ctx context.Context, pool *pgxpool.Pool, t CreatorTask) (string, error) {
	if len(t.Brief) == 0 {
		t.Brief = json.RawMessage(`{}`)
	}
	var creator any
	if t.CreatorID != nil && *t.CreatorID != "" {
		creator = *t.CreatorID
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO creator_tasks (id, account_id, kind, title, brief, status, creator_id, due_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		t.ID, t.AccountID, t.Kind, t.Title, t.Brief, t.Status, creator, t.DueAt)
	if err != nil {
		return "", fmt.Errorf("db: create creator task: %w", err)
	}
	return t.ID, nil
}

// ListCreatorTasks lists tasks for creators: open ones plus those assigned
// to creatorID (every task when creatorID is "", for AndRho admins).
func ListCreatorTasks(ctx context.Context, pool *pgxpool.Pool, creatorID string) ([]CreatorTask, error) {
	q := `SELECT ` + taskCols + ` FROM creator_tasks t JOIN accounts a ON a.id = t.account_id`
	args := []any{}
	if creatorID != "" {
		q += ` WHERE t.creator_id = $1 OR (t.creator_id IS NULL AND t.status = 'abierta')`
		args = append(args, creatorID)
	}
	q += ` ORDER BY CASE t.status WHEN 'aceptada' THEN 0 WHEN 'abierta' THEN 1 ELSE 2 END, t.created_at`
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: list creator tasks: %w", err)
	}
	defer rows.Close()
	out := []CreatorTask{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAccountCreatorTasks lists one account's tasks (the customer's view).
func ListAccountCreatorTasks(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]CreatorTask, error) {
	rows, err := pool.Query(ctx, `SELECT `+taskCols+` FROM creator_tasks t JOIN accounts a ON a.id = t.account_id
		WHERE t.account_id = $1 ORDER BY t.created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("db: list account creator tasks: %w", err)
	}
	defer rows.Close()
	out := []CreatorTask{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetCreatorTask fetches one task.
func GetCreatorTask(ctx context.Context, pool *pgxpool.Pool, id string) (CreatorTask, error) {
	return scanTask(pool.QueryRow(ctx, `SELECT `+taskCols+` FROM creator_tasks t JOIN accounts a ON a.id = t.account_id WHERE t.id = $1`, id))
}

// UpdateCreatorTask sets a task's status, creator, delivery and notes.
func UpdateCreatorTask(ctx context.Context, pool *pgxpool.Pool, id, status string, creatorID *string, deliveryURL, notes string) error {
	_, err := pool.Exec(ctx,
		`UPDATE creator_tasks SET status = $2, creator_id = $3, delivery_url = $4, notes = $5, updated_at = now() WHERE id = $1`,
		id, status, creatorID, deliveryURL, notes)
	if err != nil {
		return fmt.Errorf("db: update creator task: %w", err)
	}
	return nil
}
