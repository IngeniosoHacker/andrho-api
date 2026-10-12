package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSlugTaken is returned when an account already has an article with that slug.
var ErrSlugTaken = errors.New("db: slug already exists")

// Article mirrors a row in the `articles` table.
type Article struct {
	ID          string     `json:"id"`
	AccountID   string     `json:"-"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Keywords    []string   `json:"keywords"`
	Tags        []string   `json:"tags"`
	Author      string     `json:"author"`
	Body        string     `json:"body,omitempty"`
	WordCount   int        `json:"word_count"`
	Path        string     `json:"path"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"published_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

const articleCols = `id, account_id, slug, title, description, keywords, tags, author, body, word_count, path, status, published_at, created_at, updated_at`

func scanArticle(row pgx.Row) (Article, error) {
	var a Article
	err := row.Scan(&a.ID, &a.AccountID, &a.Slug, &a.Title, &a.Description, &a.Keywords, &a.Tags, &a.Author,
		&a.Body, &a.WordCount, &a.Path, &a.Status, &a.PublishedAt, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("db: scan article: %w", err)
	}
	return a, nil
}

func isSlugConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreateArticle inserts a; published_at is set when it's created already published.
func CreateArticle(ctx context.Context, pool *pgxpool.Pool, a Article, createdBy string) (Article, error) {
	out, err := scanArticle(pool.QueryRow(ctx,
		`INSERT INTO articles (id, account_id, slug, title, description, keywords, tags, author, body, path, status, published_at, created_by, word_count)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, CASE WHEN $11 = 'publicado' THEN COALESCE($12, now()) END, $13, $14)
		 RETURNING `+articleCols,
		a.ID, a.AccountID, a.Slug, a.Title, a.Description, a.Keywords, a.Tags, a.Author, a.Body, a.Path, a.Status, a.PublishedAt, createdBy, len(strings.Fields(a.Body))))
	if err != nil && isSlugConflict(err) {
		return Article{}, ErrSlugTaken
	}
	return out, err
}

// UpdateArticle replaces an article's editable fields. Publishing for the
// first time stamps published_at; going back to borrador keeps it.
func UpdateArticle(ctx context.Context, pool *pgxpool.Pool, a Article) (Article, error) {
	out, err := scanArticle(pool.QueryRow(ctx,
		`UPDATE articles SET slug = $3, title = $4, description = $5, keywords = $6, tags = $7, author = $8, body = $9,
		   path = $10, status = $11, word_count = $13,
		   published_at = CASE WHEN $11 = 'publicado' THEN COALESCE($12, published_at, now()) ELSE published_at END,
		   updated_at = now()
		 WHERE id = $1 AND account_id = $2
		 RETURNING `+articleCols,
		a.ID, a.AccountID, a.Slug, a.Title, a.Description, a.Keywords, a.Tags, a.Author, a.Body, a.Path, a.Status, a.PublishedAt,
		len(strings.Fields(a.Body))))
	if err != nil && isSlugConflict(err) {
		return Article{}, ErrSlugTaken
	}
	return out, err
}

// GetArticle fetches one article, scoped to the account.
func GetArticle(ctx context.Context, pool *pgxpool.Pool, accountID, id string) (Article, error) {
	return scanArticle(pool.QueryRow(ctx, `SELECT `+articleCols+` FROM articles WHERE id = $1 AND account_id = $2`, id, accountID))
}

// DeleteArticle removes an article, scoped to the account.
func DeleteArticle(ctx context.Context, pool *pgxpool.Pool, accountID, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM articles WHERE id = $1 AND account_id = $2`, id, accountID)
	if err != nil {
		return fmt.Errorf("db: delete article: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListArticles returns an account's articles without their bodies, newest
// first. publishedOnly is what the public endpoints use.
func ListArticles(ctx context.Context, pool *pgxpool.Pool, accountID string, publishedOnly bool) ([]Article, error) {
	q := `SELECT id, account_id, slug, title, description, keywords, tags, author, '' AS body, word_count, path, status, published_at, created_at, updated_at
	      FROM articles WHERE account_id = $1`
	if publishedOnly {
		q += ` AND status = 'publicado'`
	}
	q += ` ORDER BY COALESCE(published_at, created_at) DESC`
	rows, err := pool.Query(ctx, q, accountID)
	if err != nil {
		return nil, fmt.Errorf("db: list articles: %w", err)
	}
	defer rows.Close()
	out := []Article{}
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetPublishedArticleBySiteSlug is the public read: the published article
// `slug` of the account that owns site_id.
func GetPublishedArticleBySiteSlug(ctx context.Context, pool *pgxpool.Pool, siteID, slug string) (Article, error) {
	return scanArticle(pool.QueryRow(ctx,
		`SELECT `+prefixed("a.", articleCols)+` FROM articles a JOIN accounts acc ON acc.id = a.account_id
		 WHERE acc.site_id = $1 AND a.slug = $2 AND a.status = 'publicado'`, siteID, slug))
}

// AccountIDBySiteID resolves a public site_id to its account.
func AccountIDBySiteID(ctx context.Context, pool *pgxpool.Pool, siteID string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id FROM accounts WHERE site_id = $1`, siteID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db: account by site id: %w", err)
	}
	return id, nil
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
