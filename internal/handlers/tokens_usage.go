package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// AdminTokens handles GET /admin/tokens?days=30: AI tokens by source
// (development, whatsapp, analyzer, insights), model, day and account.
func (h *Handler) AdminTokens(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", "30"))
	if days < 1 || days > 365 {
		days = 30
	}
	s, err := db.GetTokenSummary(c.Request.Context(), h.Accounts, days)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, s)
}

type tokenImport struct {
	Source  string `json:"source"`
	Entries []struct {
		Date         string `json:"date"` // YYYY-MM-DD
		Model        string `json:"model"`
		InputTokens  int    `json:"input_tokens"`
		OutputTokens int    `json:"output_tokens"`
		Note         string `json:"note"`
	} `json:"entries"`
}

// ImportTokens handles POST /admin/tokens: usage AndRho's code doesn't see
// by itself, e.g. the team's development usage (Claude Code, the console),
// entered by day. source defaults to "development".
func (h *Handler) ImportTokens(c *gin.Context) {
	var req tokenImport
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Entries) == 0 || len(req.Entries) > 1000 {
		respondError(c, http.StatusBadRequest, "entries: una lista de 1 a 1000 registros")
		return
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "development"
	}
	if source != "development" && source != "whatsapp" {
		respondError(c, http.StatusBadRequest, "source debe ser development o whatsapp")
		return
	}
	ctx := c.Request.Context()
	n := 0
	for _, e := range req.Entries {
		day, err := time.Parse("2006-01-02", strings.TrimSpace(e.Date))
		if err != nil || e.InputTokens < 0 || e.OutputTokens < 0 || len(e.Model) > 100 || len(e.Note) > 300 {
			respondError(c, http.StatusBadRequest, "registro "+strconv.Itoa(n+1)+": fecha YYYY-MM-DD y tokens no negativos")
			return
		}
		if err := db.RecordTokenUsage(ctx, h.Accounts, db.TokenUsage{
			Source: source, Model: strings.TrimSpace(e.Model), InputTokens: e.InputTokens, OutputTokens: e.OutputTokens,
			Note: strings.TrimSpace(e.Note), CreatedAt: day.Add(12 * time.Hour),
		}); err != nil {
			respondError(c, http.StatusInternalServerError, "internal error")
			return
		}
		n++
	}
	c.JSON(http.StatusCreated, gin.H{"imported": n})
}
