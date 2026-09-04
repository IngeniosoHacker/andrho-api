package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// ListSuggestions handles GET /suggestions?section=marketing. Without
// `section` it returns every suggestion for the account (the dashboard's
// global "Sugerencias" tab); with it, only that section's (the per-view
// "Sugerencias" button).
func (h *Handler) ListSuggestions(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	section := strings.TrimSpace(c.Query("section"))
	rows, err := db.ListSuggestions(c.Request.Context(), h.Accounts, claims.AccountID, section)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	out := make([]db.PublicSuggestion, 0, len(rows))
	for _, s := range rows {
		out = append(out, s.ToPublic())
	}
	c.JSON(http.StatusOK, gin.H{"suggestions": out})
}

type createSuggestionRequest struct {
	Section string `json:"section"`
}

// generateSuggestionStub is a placeholder for the real suggestion engine
// ("combina R con AI", per product direction) that doesn't exist yet. It
// returns fixed-shape, clearly-labeled placeholder content so the full
// generate -> list -> accept/reject -> report flow is real and testable
// end-to-end today; swap this function's body for the real call once that
// engine exists -- nothing else in this file should need to change.
func generateSuggestionStub(section string) (title, body, report string) {
	title = "Sugerencia para " + section
	body = "Contenido de ejemplo: esta sugerencia todavía no la genera el motor de IA real -- " +
		"esta llamada está reservada para cuando exista (ver generateSuggestionStub en suggestions.go)."
	report = "Reporte de ejemplo para la sección \"" + section + "\". Sin datos reales todavía."
	return
}

// CreateSuggestion handles POST /suggestions. Enforces the account's monthly
// suggestion quota (see suggestionQuotaByPlan in account.go) before calling
// the (stub) generator.
func (h *Handler) CreateSuggestion(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	var req createSuggestionRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Section) == "" {
		respondError(c, http.StatusBadRequest, "section is required")
		return
	}
	section := strings.TrimSpace(req.Section)

	ctx := c.Request.Context()

	acc, err := db.GetAccountByID(ctx, h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	limit, ok := suggestionQuotaByPlan[acc.Plan]
	if !ok {
		limit = suggestionQuotaByPlan["base"]
	}
	if limit >= 0 {
		used, err := db.CountSuggestionsThisMonth(ctx, h.Accounts, claims.AccountID)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal error")
			return
		}
		if used >= limit {
			respondError(c, http.StatusForbidden, "alcanzaste el límite de sugerencias de tu plan este mes")
			return
		}
	}

	title, body, report := generateSuggestionStub(section)
	creator := claims.Sub
	s := db.Suggestion{
		ID: uuid.NewString(), AccountID: claims.AccountID, Section: section,
		Status: "sugerida", Title: title, Body: body, Report: report, CreatedBy: &creator,
	}
	s, err = db.CreateSuggestion(ctx, h.Accounts, s)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "suggestion_created",
		"Nueva sugerencia generada para "+section)

	resp, err := h.planResponseFor(c, claims.AccountID, acc.Plan)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusCreated, gin.H{"suggestion": s.ToPublic(), "suggestions_quota": resp.SuggestionQuota})
}

type updateSuggestionStatusRequest struct {
	Status string `json:"status"`
}

// UpdateSuggestionStatus handles PATCH /suggestions/:id/status.
func (h *Handler) UpdateSuggestionStatus(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	var req updateSuggestionStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Status = strings.TrimSpace(strings.ToLower(req.Status))
	if !db.ValidSuggestionStatuses[req.Status] {
		respondError(c, http.StatusBadRequest, "status must be one of: sugerida, aceptada_en_proceso, terminada, rechazada")
		return
	}

	ctx := c.Request.Context()
	id := c.Param("id")

	s, err := db.GetSuggestion(ctx, h.Accounts, claims.AccountID, id)
	if err != nil {
		respondError(c, http.StatusNotFound, "suggestion not found")
		return
	}

	if err := db.UpdateSuggestionStatus(ctx, h.Accounts, id, req.Status); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "suggestion_status_changed",
		"\""+s.Title+"\" pasó a "+req.Status)

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
