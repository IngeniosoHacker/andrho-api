package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/insights"
)

const insightsTTL = 10 * time.Minute

var sectionRe = regexp.MustCompile(`^[a-z_]{2,30}$`)

// RunInsights handles POST /insights/:section[?refresh=1]: the AndRho
// panel's analysis of a section (internal/insights). The result is cached
// 10 minutes per section; new suggestions are stored (once per title while
// open, within the plan's monthly quota) for the panel to accept or reject.
func (h *Handler) RunInsights(c *gin.Context) {
	claims, _ := currentClaims(c)
	section := c.Param("section")
	if !sectionRe.MatchString(section) {
		respondError(c, http.StatusBadRequest, "invalid section")
		return
	}
	ctx := c.Request.Context()

	if c.Query("refresh") != "1" {
		var payload json.RawMessage
		var at time.Time
		err := h.Accounts.QueryRow(ctx, `SELECT payload, created_at FROM insights_runs WHERE account_id = $1 AND section = $2`,
			claims.AccountID, section).Scan(&payload, &at)
		if err == nil && time.Since(at) < insightsTTL {
			c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
			return
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			respondError(c, http.StatusInternalServerError, "internal error")
			return
		}
	}

	src := insights.Sources{Tracker: h.Tracker, SiteID: claims.SiteID}
	if cl, err := h.odooFor(ctx, claims.AccountID); err == nil {
		src.Odoo = cl
	}
	actx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	res := insights.Run(actx, src, h.AI, claims.AccountID, section)

	created, quotaReached, err := h.storeProposals(ctx, claims.AccountID, section, res.Proposals)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	payload, _ := json.Marshal(gin.H{"insights": res, "new_suggestions": created, "quota_reached": quotaReached})
	_, _ = h.Accounts.Exec(ctx,
		`INSERT INTO insights_runs (account_id, section, payload) VALUES ($1, $2, $3)
		 ON CONFLICT (account_id, section) DO UPDATE SET payload = EXCLUDED.payload, created_at = now()`,
		claims.AccountID, section, payload)
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

func (h *Handler) storeProposals(ctx context.Context, accountID, section string, props []insights.Proposal) (int, bool, error) {
	if len(props) == 0 {
		return 0, false, nil
	}
	acc, err := db.GetAccountByID(ctx, h.Accounts, accountID)
	if err != nil {
		return 0, false, err
	}
	limit, ok := suggestionQuotaByPlan[acc.Plan]
	if !ok {
		limit = suggestionQuotaByPlan["base"]
	}
	used, err := db.CountSuggestionsThisMonth(ctx, h.Accounts, accountID)
	if err != nil {
		return 0, false, err
	}
	created := 0
	for _, p := range props {
		var exists bool
		if err := h.Accounts.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM suggestions WHERE account_id = $1 AND title = $2
			   AND (status IN ('sugerida', 'aceptada_en_proceso') OR created_at >= now() - interval '14 days'))`,
			accountID, p.Title).Scan(&exists); err != nil {
			return created, false, err
		}
		if exists {
			continue
		}
		if limit >= 0 && used >= limit {
			return created, true, nil
		}
		if _, err := db.CreateSuggestion(ctx, h.Accounts, db.Suggestion{
			ID: uuid.NewString(), AccountID: accountID, Section: section, Status: "sugerida",
			Title: p.Title, Body: p.Body, Report: p.Evidence,
		}); err != nil {
			return created, false, err
		}
		used++
		created++
	}
	return created, false, nil
}

