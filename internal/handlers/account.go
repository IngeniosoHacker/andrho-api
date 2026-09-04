package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// suggestionQuotaByPlan is how many suggestions an account may *generate*
// (not just have) per calendar month. -1 means unlimited (rendered as "∞" by
// the dashboard). These numbers aren't tied to any billing system yet --
// see accounts.plan's doc comment in schema.sql.
var suggestionQuotaByPlan = map[string]int{
	"base":      3,
	"despegue":  10,
	"en_orbita": 30,
	"galactico": -1,
}

// validPlans mirrors the landing page's Pricing tiers (andrho-tracker-dashboard/
// web/src/components/sections/Pricing.jsx).
var validPlans = map[string]bool{
	"base": true, "despegue": true, "en_orbita": true, "galactico": true,
}

type suggestionQuota struct {
	Limit         int `json:"limit"` // -1 = unlimited
	UsedThisMonth int `json:"used_this_month"`
	Remaining     int `json:"remaining"` // -1 = unlimited
}

type planResponse struct {
	Plan            string          `json:"plan"`
	SuggestionQuota suggestionQuota `json:"suggestions_quota"`
}

func (h *Handler) planResponseFor(c *gin.Context, accountID, plan string) (planResponse, error) {
	limit, ok := suggestionQuotaByPlan[plan]
	if !ok {
		limit = suggestionQuotaByPlan["base"]
	}
	used, err := db.CountSuggestionsThisMonth(c.Request.Context(), h.Accounts, accountID)
	if err != nil {
		return planResponse{}, err
	}
	remaining := -1
	if limit >= 0 {
		remaining = limit - used
		if remaining < 0 {
			remaining = 0
		}
	}
	return planResponse{
		Plan:            plan,
		SuggestionQuota: suggestionQuota{Limit: limit, UsedThisMonth: used, Remaining: remaining},
	}, nil
}

// GetPlan handles GET /account/plan.
func (h *Handler) GetPlan(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	acc, err := db.GetAccountByID(c.Request.Context(), h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	resp, err := h.planResponseFor(c, acc.ID, acc.Plan)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, resp)
}

type updatePlanRequest struct {
	Plan string `json:"plan"`
}

// UpdatePlan handles PATCH /account/plan (owner only, enforced in router.go).
// This does NOT charge anything -- there is no billing/payment processor
// wired up yet. It only flips which feature/quota tier the account is gated
// at, for internal testing and for once billing exists to hook into.
func (h *Handler) UpdatePlan(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	var req updatePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Plan = strings.TrimSpace(strings.ToLower(req.Plan))
	if !validPlans[req.Plan] {
		respondError(c, http.StatusBadRequest, "plan must be one of: base, despegue, en_orbita, galactico")
		return
	}

	ctx := c.Request.Context()
	if err := db.UpdateAccountPlan(ctx, h.Accounts, claims.AccountID, req.Plan); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "plan_changed", "Plan cambiado a "+req.Plan)

	resp, err := h.planResponseFor(c, claims.AccountID, req.Plan)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, resp)
}
