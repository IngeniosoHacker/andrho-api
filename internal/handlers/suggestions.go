package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/insights"
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

	assigned, err := db.SuggestionAssignments(c.Request.Context(), h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]db.PublicSuggestion, 0, len(rows))
	for _, s := range rows {
		p := s.ToPublic()
		if a, ok := assigned[s.ID]; ok {
			p.Assignment = &a
		} else if s.Status == "aceptada_en_proceso" && suggestionAction(s).Type == "erp_install" {
			p.Assignment = &db.SuggestionAssignment{Kind: "andrho", Status: "en_proceso"}
		}
		out = append(out, p)
	}
	c.JSON(http.StatusOK, gin.H{"suggestions": out})
}

type createSuggestionRequest struct {
	Section string `json:"section"`
}

// CreateSuggestion handles POST /suggestions: asks AndRho for a new
// suggestion for a section now. It runs the section's analysis
// (internal/insights) and files the first proposal not already on the
// account; 200 with suggestion null when there's nothing new. Enforces the
// monthly quota (suggestionQuotaByPlan in account.go).
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

	src := insights.Sources{Tracker: h.Tracker, SiteID: claims.SiteID, Stats: h.Stats}
	src.BusinessType, _ = db.BusinessType(ctx, h.Accounts, claims.AccountID)
	if cl, err := h.odooFor(ctx, claims.AccountID); err == nil {
		src.Odoo = cl
	}
	var s db.Suggestion
	found := false
	for _, p := range insights.Run(ctx, src, h.AI, claims.AccountID, section).Proposals {
		var exists bool
		if err := h.Accounts.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM suggestions WHERE account_id = $1 AND title = $2 AND status IN ('sugerida', 'aceptada_en_proceso'))`,
			claims.AccountID, p.Title).Scan(&exists); err != nil {
			respondError(c, http.StatusInternalServerError, "internal error")
			return
		}
		if exists {
			continue
		}
		creator := claims.Sub
		s, err = db.CreateSuggestion(ctx, h.Accounts, proposalRow(claims.AccountID, section, p, &creator))
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal error")
			return
		}
		found = true
		break
	}
	if !found {
		c.JSON(http.StatusOK, gin.H{"suggestion": nil, "message": "Por ahora AndRho no tiene nada nuevo que sugerir en esta sección."})
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

	if req.Status == s.Status {
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}
	// Accepting starts the work (accept.go); only from "sugerida", so a
	// double click can't create two Creator tasks.
	if req.Status == "aceptada_en_proceso" && s.Status != "sugerida" {
		respondError(c, http.StatusConflict, "esta sugerencia ya no está pendiente")
		return
	}
	if err := db.UpdateSuggestionStatus(ctx, h.Accounts, id, req.Status); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	var assignment *db.SuggestionAssignment
	if req.Status == "aceptada_en_proceso" {
		assignment, err = h.acceptSuggestion(ctx, claims.AccountID, s)
		if err != nil {
			_ = db.UpdateSuggestionStatus(ctx, h.Accounts, id, s.Status)
			respondError(c, http.StatusInternalServerError, "no pudimos asignar esta sugerencia; intenta de nuevo")
			return
		}
	} else {
		h.closeSuggestionTasks(ctx, id, req.Status)
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "suggestion_status_changed",
		"\""+s.Title+"\" pasó a "+req.Status)

	c.JSON(http.StatusOK, gin.H{"ok": true, "assignment": assignment})
}

// proposalRow is a computed proposal as a suggestion row: the owner-facing
// text and explanation, the technical evidence apart, the action to run.
func proposalRow(accountID, section string, p insights.Proposal, createdBy *string) db.Suggestion {
	row := db.Suggestion{ID: uuid.NewString(), AccountID: accountID, Section: section, Status: "sugerida",
		Title: p.Title, Body: p.Body, Evidence: p.Evidence, CreatedBy: createdBy}
	if p.Explain != nil {
		row.Explain, _ = json.Marshal(p.Explain)
	}
	if p.Action != nil {
		row.Action, _ = json.Marshal(p.Action)
	}
	return row
}
