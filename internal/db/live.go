package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Venta en vivo: the seller's Pages, live sessions, claims, posts and
// Messenger threads (schema.sql, founder/live-sale-spec.md).

// MetaPage mirrors meta_pages.
type MetaPage struct {
	PageID, AccountID, Name, Picture, IGUserID string
	Token                                      []byte // sealed
	Selected, Subscribed                       bool
}

// SaveMetaPage inserts or refreshes a Page the seller manages.
func SaveMetaPage(ctx context.Context, pool *pgxpool.Pool, p MetaPage, connectedBy string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO meta_pages (page_id, account_id, name, picture, ig_user_id, token, connected_by)
		 VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid)
		 ON CONFLICT (page_id) DO UPDATE SET account_id = $2, name = $3, picture = $4, ig_user_id = $5, token = $6,
		   connected_by = NULLIF($7, '')::uuid, updated_at = now(),
		   selected = CASE WHEN meta_pages.account_id = $2 THEN meta_pages.selected ELSE false END`,
		p.PageID, p.AccountID, p.Name, p.Picture, p.IGUserID, p.Token, connectedBy)
	if err != nil {
		return fmt.Errorf("db: save meta page: %w", err)
	}
	return nil
}

const pageCols = `page_id, account_id::text, name, picture, ig_user_id, token, selected, subscribed`

func scanPage(row pgx.Row) (MetaPage, error) {
	var p MetaPage
	err := row.Scan(&p.PageID, &p.AccountID, &p.Name, &p.Picture, &p.IGUserID, &p.Token, &p.Selected, &p.Subscribed)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// ListMetaPages lists an account's Pages, the selected one first.
func ListMetaPages(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]MetaPage, error) {
	rows, err := pool.Query(ctx, `SELECT `+pageCols+` FROM meta_pages WHERE account_id = $1 ORDER BY selected DESC, name`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MetaPage{}
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetMetaPage reads one Page (any account: webhooks start from the Page).
func GetMetaPage(ctx context.Context, pool *pgxpool.Pool, pageID string) (MetaPage, error) {
	return scanPage(pool.QueryRow(ctx, `SELECT `+pageCols+` FROM meta_pages WHERE page_id = $1`, pageID))
}

// SelectedMetaPage is the Page an account sells on.
func SelectedMetaPage(ctx context.Context, pool *pgxpool.Pool, accountID string) (MetaPage, error) {
	return scanPage(pool.QueryRow(ctx, `SELECT `+pageCols+` FROM meta_pages WHERE account_id = $1 AND selected`, accountID))
}

// SelectMetaPage makes pageID the account's selling Page.
func SelectMetaPage(ctx context.Context, pool *pgxpool.Pool, accountID, pageID string, subscribed bool) error {
	tag, err := pool.Exec(ctx,
		`UPDATE meta_pages SET selected = (page_id = $2), subscribed = CASE WHEN page_id = $2 THEN $3 ELSE subscribed END, updated_at = now()
		 WHERE account_id = $1`, accountID, pageID, subscribed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteMetaPages forgets an account's Pages (disconnect).
func DeleteMetaPages(ctx context.Context, pool *pgxpool.Pool, accountID string) error {
	_, err := pool.Exec(ctx, `DELETE FROM meta_pages WHERE account_id = $1`, accountID)
	return err
}

// LiveSession mirrors live_sessions.
type LiveSession struct {
	ID, AccountID                     string
	OdooLiveID                        int
	PageID, PlatformLiveID, LiveURL   string
	Status                            string
	LastCommentAt, StartedAt, EndedAt *time.Time
	CreatedAt                         time.Time
}

const sessionCols = `id::text, account_id::text, odoo_live_id, page_id, platform_live_id, live_url, status, last_comment_at, started_at, ended_at, created_at`

func scanSession(row pgx.Row) (LiveSession, error) {
	var s LiveSession
	err := row.Scan(&s.ID, &s.AccountID, &s.OdooLiveID, &s.PageID, &s.PlatformLiveID, &s.LiveURL, &s.Status,
		&s.LastCommentAt, &s.StartedAt, &s.EndedAt, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

// CreateLiveSession records an organized live.
func CreateLiveSession(ctx context.Context, pool *pgxpool.Pool, s LiveSession) error {
	_, err := pool.Exec(ctx, `INSERT INTO live_sessions (id, account_id, odoo_live_id, page_id) VALUES ($1, $2, $3, $4)`,
		s.ID, s.AccountID, s.OdooLiveID, s.PageID)
	return err
}

// GetLiveSession reads one of an account's sessions ("" account = any).
func GetLiveSession(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (LiveSession, error) {
	return scanSession(pool.QueryRow(ctx,
		`SELECT `+sessionCols+` FROM live_sessions WHERE id::text = $2 AND ($1 = '' OR account_id::text = $1)`, accountID, id))
}

// ListLiveSessions lists an account's sessions, newest first.
func ListLiveSessions(ctx context.Context, pool *pgxpool.Pool, accountID string, limit int) ([]LiveSession, error) {
	return querySessions(ctx, pool, `SELECT `+sessionCols+` FROM live_sessions WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2`, accountID, limit)
}

// ActiveLiveSessions are the sessions on air (resumed on boot).
func ActiveLiveSessions(ctx context.Context, pool *pgxpool.Pool) ([]LiveSession, error) {
	return querySessions(ctx, pool, `SELECT `+sessionCols+` FROM live_sessions WHERE status = 'live'`)
}

// LatestReadySession is the account's most recent organized live, for a
// "live started" webhook to pick up.
func LatestReadySession(ctx context.Context, pool *pgxpool.Pool, accountID string) (LiveSession, error) {
	return scanSession(pool.QueryRow(ctx,
		`SELECT `+sessionCols+` FROM live_sessions WHERE account_id = $1 AND status = 'ready' ORDER BY created_at DESC LIMIT 1`, accountID))
}

func querySessions(ctx context.Context, pool *pgxpool.Pool, q string, args ...any) ([]LiveSession, error) {
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveSession{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StartLiveSession puts a session on air.
func StartLiveSession(ctx context.Context, pool *pgxpool.Pool, id, pageID, platformLiveID, liveURL string) error {
	_, err := pool.Exec(ctx,
		`UPDATE live_sessions SET status = 'live', page_id = $2, platform_live_id = $3, live_url = $4, started_at = COALESCE(started_at, now())
		 WHERE id = $1`, id, pageID, platformLiveID, liveURL)
	return err
}

// EndLiveSession marks a session done; false when it already was.
func EndLiveSession(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	tag, err := pool.Exec(ctx, `UPDATE live_sessions SET status = 'done', ended_at = now() WHERE id = $1 AND status <> 'done'`, id)
	return tag.RowsAffected() > 0, err
}

// TouchLiveSession records the newest comment read.
func TouchLiveSession(ctx context.Context, pool *pgxpool.Pool, id string, at time.Time) error {
	_, err := pool.Exec(ctx, `UPDATE live_sessions SET last_comment_at = GREATEST(COALESCE(last_comment_at, $2), $2) WHERE id = $1`, id, at)
	return err
}

// LiveClaim mirrors live_claims.
type LiveClaim struct {
	ID                                       int64
	AccountID, SessionID, PageID             string
	OdooLineID                               int
	Source, PostID, CommentID                string
	AuthorID, AuthorName, CommentText, State string
	PSID                                     string
	CardID                                   *int
	CommentedAt                              time.Time
	AskedAt, AnsweredAt                      *time.Time
}

const claimCols = `id, account_id::text, COALESCE(session_id::text, ''), page_id, odoo_line_id, source, post_id, comment_id,
	author_id, author_name, comment_text, state, psid, card_id, commented_at, asked_at, answered_at`

func scanClaim(row pgx.Row) (LiveClaim, error) {
	var c LiveClaim
	err := row.Scan(&c.ID, &c.AccountID, &c.SessionID, &c.PageID, &c.OdooLineID, &c.Source, &c.PostID, &c.CommentID,
		&c.AuthorID, &c.AuthorName, &c.CommentText, &c.State, &c.PSID, &c.CardID, &c.CommentedAt, &c.AskedAt, &c.AnsweredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// InsertLiveClaim records a claim; ok=false when the comment was already
// seen (Meta's webhook and AndRho's polling may both bring it).
func InsertLiveClaim(ctx context.Context, pool *pgxpool.Pool, c LiveClaim) (LiveClaim, bool, error) {
	row := pool.QueryRow(ctx,
		`INSERT INTO live_claims (account_id, session_id, page_id, odoo_line_id, source, post_id, comment_id, author_id, author_name, comment_text, commented_at)
		 VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (comment_id) DO NOTHING
		 RETURNING `+claimCols,
		c.AccountID, c.SessionID, c.PageID, c.OdooLineID, c.Source, c.PostID, c.CommentID, c.AuthorID, c.AuthorName, c.CommentText, c.CommentedAt)
	out, err := scanClaim(row)
	if errors.Is(err, ErrNotFound) {
		return c, false, nil
	}
	return out, err == nil, err
}

// GetLiveClaim reads a claim.
func GetLiveClaim(ctx context.Context, pool *pgxpool.Pool, id int64) (LiveClaim, error) {
	return scanClaim(pool.QueryRow(ctx, `SELECT `+claimCols+` FROM live_claims WHERE id = $1`, id))
}

// GetLiveClaimByComment reads the claim a Meta comment made.
func GetLiveClaimByComment(ctx context.Context, pool *pgxpool.Pool, commentID string) (LiveClaim, error) {
	return scanClaim(pool.QueryRow(ctx, `SELECT `+claimCols+` FROM live_claims WHERE comment_id = $1`, commentID))
}

// SetLiveClaimState moves a claim from one of from to state; false when it
// was in none of them (someone else moved it first).
func SetLiveClaimState(ctx context.Context, pool *pgxpool.Pool, id int64, from []string, state string) (bool, error) {
	stamp := ""
	switch state {
	case "asked":
		stamp = ", asked_at = now()"
	case "confirmed", "declined":
		stamp = ", answered_at = now()"
	}
	tag, err := pool.Exec(ctx, `UPDATE live_claims SET state = $3`+stamp+` WHERE id = $1 AND state = ANY($2)`, id, from, state)
	return tag.RowsAffected() > 0, err
}

// SetLiveClaimBuyer stores the buyer's Messenger id and card.
func SetLiveClaimBuyer(ctx context.Context, pool *pgxpool.Pool, id int64, psid string, cardID int) error {
	_, err := pool.Exec(ctx, `UPDATE live_claims SET psid = $2, card_id = NULLIF($3, 0) WHERE id = $1`, id, psid, cardID)
	return err
}

// NextQueuedClaim is the earliest waiting comment for an item.
func NextQueuedClaim(ctx context.Context, pool *pgxpool.Pool, accountID string, lineID int) (LiveClaim, error) {
	return scanClaim(pool.QueryRow(ctx,
		`SELECT `+claimCols+` FROM live_claims WHERE account_id = $1 AND odoo_line_id = $2 AND state = 'queued'
		 ORDER BY commented_at, id LIMIT 1`, accountID, lineID))
}

// ExpiredClaims are claims asked before cutoff and still unanswered.
func ExpiredClaims(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) ([]LiveClaim, error) {
	rows, err := pool.Query(ctx, `SELECT `+claimCols+` FROM live_claims WHERE state = 'asked' AND asked_at < $1 LIMIT 200`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveClaim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListSessionClaims is a session's activity, newest first.
func ListSessionClaims(ctx context.Context, pool *pgxpool.Pool, sessionID string, limit int) ([]LiveClaim, error) {
	rows, err := pool.Query(ctx, `SELECT `+claimCols+` FROM live_claims WHERE session_id::text = $1 ORDER BY created_at DESC LIMIT $2`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveClaim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LivePost mirrors live_posts.
type LivePost struct {
	PostID, AccountID, SessionID, PageID, State string
	OdooLineID                                  int
}

// SaveLivePost records an unsold item's post.
func SaveLivePost(ctx context.Context, pool *pgxpool.Pool, p LivePost) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO live_posts (post_id, account_id, session_id, page_id, odoo_line_id) VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5)
		 ON CONFLICT (post_id) DO NOTHING`, p.PostID, p.AccountID, p.SessionID, p.PageID, p.OdooLineID)
	return err
}

// GetLivePost reads a post AndRho published.
func GetLivePost(ctx context.Context, pool *pgxpool.Pool, postID string) (LivePost, error) {
	var p LivePost
	err := pool.QueryRow(ctx, `SELECT post_id, account_id::text, COALESCE(session_id::text, ''), page_id, state, odoo_line_id FROM live_posts WHERE post_id = $1`, postID).
		Scan(&p.PostID, &p.AccountID, &p.SessionID, &p.PageID, &p.State, &p.OdooLineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// SetLivePostState marks a post sold or deleted.
func SetLivePostState(ctx context.Context, pool *pgxpool.Pool, postID, state string) error {
	_, err := pool.Exec(ctx, `UPDATE live_posts SET state = $2 WHERE post_id = $1`, postID, state)
	return err
}

// LiveThread mirrors live_threads.
type LiveThread struct {
	PageID, PSID, AccountID, Step string
	CardID                        int
}

// SaveLiveThread sets what AndRho waits for from a buyer.
func SaveLiveThread(ctx context.Context, pool *pgxpool.Pool, t LiveThread) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO live_threads (page_id, psid, account_id, step, card_id) VALUES ($1, $2, $3, $4, NULLIF($5, 0))
		 ON CONFLICT (page_id, psid) DO UPDATE SET account_id = $3, step = $4, card_id = NULLIF($5, 0), updated_at = now()`,
		t.PageID, t.PSID, t.AccountID, t.Step, t.CardID)
	return err
}

// GetLiveThread reads a buyer's conversation; threads older than a day are
// stale (Messenger's 24 h window closed).
func GetLiveThread(ctx context.Context, pool *pgxpool.Pool, pageID, psid string) (LiveThread, error) {
	var t LiveThread
	var card *int
	err := pool.QueryRow(ctx,
		`SELECT page_id, psid, account_id::text, step, card_id FROM live_threads
		 WHERE page_id = $1 AND psid = $2 AND updated_at > now() - interval '24 hours'`, pageID, psid).
		Scan(&t.PageID, &t.PSID, &t.AccountID, &t.Step, &card)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if card != nil {
		t.CardID = *card
	}
	return t, err
}
