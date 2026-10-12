package handlers

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// Articles: the account's site content, written or uploaded from the
// dashboard's Web > Artículos tab and read back by the site through the
// public endpoints below. Bodies are Markdown.

const (
	maxArticleTitle = 200
	maxArticleDesc  = 320
	maxArticleBody  = 120_000
	maxArticlePath  = 300
	maxArticleSlug  = 90
)

type articleRequest struct {
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Keywords    []string   `json:"keywords"`
	Tags        []string   `json:"tags"`
	Author      string     `json:"author"`
	Body        string     `json:"body"`
	Path        string     `json:"path"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"published_at"`
}

var slugAccents = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n",
	"à", "a", "è", "e", "ì", "i", "ò", "o", "ù", "u", "ç", "c",
)
var slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify turns a title into a URL slug: "¿Qué software necesita mi
// emprendimiento?" -> "que-software-necesita-mi-emprendimiento".
func Slugify(s string) string {
	s = slugAccents.Replace(strings.ToLower(strings.TrimSpace(s)))
	s = strings.Trim(slugInvalid.ReplaceAllString(s, "-"), "-")
	for len(s) > maxArticleSlug {
		cut := strings.LastIndex(s[:maxArticleSlug], "-")
		if cut <= 0 {
			s = s[:maxArticleSlug]
			break
		}
		s = s[:cut]
	}
	return s
}

var pathRe = regexp.MustCompile(`^/[A-Za-z0-9._~/%-]*$`)

// clean validates req in place and returns a Spanish error message, or "".
func (req *articleRequest) clean() string {
	req.Title = strings.TrimSpace(req.Title)
	req.Description = strings.TrimSpace(req.Description)
	req.Author = strings.TrimSpace(req.Author)
	req.Path = strings.TrimSpace(req.Path)
	req.Status = strings.TrimSpace(strings.ToLower(req.Status))
	if req.Slug = Slugify(req.Slug); req.Slug == "" {
		req.Slug = Slugify(req.Title)
	}
	if req.Status == "" {
		req.Status = "borrador"
	}

	switch {
	case req.Title == "":
		return "el título es obligatorio"
	case utf8.RuneCountInString(req.Title) > maxArticleTitle:
		return "el título es demasiado largo"
	case req.Slug == "":
		return "el slug no es válido"
	case utf8.RuneCountInString(req.Description) > maxArticleDesc:
		return "la descripción no puede pasar de 320 caracteres"
	case len(req.Body) > maxArticleBody:
		return "el artículo es demasiado largo"
	case req.Status != "borrador" && req.Status != "publicado":
		return "status debe ser borrador o publicado"
	case req.Path != "" && (len(req.Path) > maxArticlePath || !pathRe.MatchString(req.Path)):
		return "la ruta debe empezar con / y no llevar espacios"
	case req.Status == "publicado" && strings.TrimSpace(req.Body) == "":
		return "no se puede publicar un artículo vacío"
	}
	var ok bool
	if req.Keywords, ok = cleanList(lower(req.Keywords)); !ok {
		return "demasiadas palabras clave"
	}
	if req.Tags, ok = cleanList(req.Tags); !ok {
		return "demasiadas etiquetas"
	}
	return ""
}

func lower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func (req articleRequest) article(id, accountID string) db.Article {
	return db.Article{
		ID: id, AccountID: accountID, Slug: req.Slug, Title: req.Title, Description: req.Description,
		Keywords: req.Keywords, Tags: req.Tags, Author: req.Author, Body: req.Body, Path: req.Path,
		Status: req.Status, PublishedAt: req.PublishedAt,
	}
}

// ListArticles handles GET /articles: every article of the account, drafts
// included, without bodies.
func (h *Handler) ListArticles(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	list, err := db.ListArticles(c.Request.Context(), h.Accounts, claims.AccountID, false)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"articles": list})
}

// GetArticle handles GET /articles/:id (with body, for the editor).
func (h *Handler) GetArticle(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	a, err := db.GetArticle(c.Request.Context(), h.Accounts, claims.AccountID, c.Param("id"))
	if err != nil {
		h.articleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"article": a})
}

// CreateArticle handles POST /articles (editor+): a new article, written in
// the dashboard or uploaded as a Markdown file.
func (h *Handler) CreateArticle(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	var req articleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := req.clean(); msg != "" {
		respondError(c, http.StatusBadRequest, msg)
		return
	}
	ctx := c.Request.Context()
	a, err := db.CreateArticle(ctx, h.Accounts, req.article(uuid.NewString(), claims.AccountID), claims.Sub)
	if err != nil {
		h.articleError(c, err)
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "article_created",
		"Artículo \""+a.Title+"\" creado ("+a.Status+")")
	c.JSON(http.StatusCreated, gin.H{"article": a})
}

// UpdateArticle handles PUT /articles/:id (editor+).
func (h *Handler) UpdateArticle(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	var req articleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := req.clean(); msg != "" {
		respondError(c, http.StatusBadRequest, msg)
		return
	}
	ctx := c.Request.Context()
	before, err := db.GetArticle(ctx, h.Accounts, claims.AccountID, c.Param("id"))
	if err != nil {
		h.articleError(c, err)
		return
	}
	a, err := db.UpdateArticle(ctx, h.Accounts, req.article(before.ID, claims.AccountID))
	if err != nil {
		h.articleError(c, err)
		return
	}
	desc := "Artículo \"" + a.Title + "\" editado"
	if before.Status != a.Status {
		desc = "Artículo \"" + a.Title + "\" pasó a " + a.Status
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "article_updated", desc)
	c.JSON(http.StatusOK, gin.H{"article": a})
}

// DeleteArticle handles DELETE /articles/:id (editor+).
func (h *Handler) DeleteArticle(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	ctx := c.Request.Context()
	a, err := db.GetArticle(ctx, h.Accounts, claims.AccountID, c.Param("id"))
	if err != nil {
		h.articleError(c, err)
		return
	}
	if err := db.DeleteArticle(ctx, h.Accounts, claims.AccountID, a.ID); err != nil {
		h.articleError(c, err)
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "article_deleted",
		"Artículo \""+a.Title+"\" eliminado")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// PublicArticles handles GET /public/sites/:siteId/articles: the published
// articles of that site, without bodies, for its index pages.
func (h *Handler) PublicArticles(c *gin.Context) {
	ctx := c.Request.Context()
	accountID, err := db.AccountIDBySiteID(ctx, h.Accounts, c.Param("siteId"))
	if err != nil {
		h.articleError(c, err)
		return
	}
	list, err := db.ListArticles(ctx, h.Accounts, accountID, true)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.JSON(http.StatusOK, gin.H{"articles": list})
}

// PublicArticle handles GET /public/sites/:siteId/articles/:slug.
func (h *Handler) PublicArticle(c *gin.Context) {
	a, err := db.GetPublishedArticleBySiteSlug(c.Request.Context(), h.Accounts, c.Param("siteId"), c.Param("slug"))
	if err != nil {
		h.articleError(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.JSON(http.StatusOK, gin.H{"article": a})
}

func (h *Handler) articleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		respondError(c, http.StatusNotFound, "artículo no encontrado")
	case errors.Is(err, db.ErrSlugTaken):
		respondError(c, http.StatusConflict, "ya tienes un artículo con ese slug")
	default:
		respondError(c, http.StatusInternalServerError, "internal error")
	}
}
