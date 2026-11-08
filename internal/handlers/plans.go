package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/billing"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/plans"
)

// The plan catalog for the UI (todo.md C1–C4). Prices and limits only
// come from internal/plans; the signup and the dashboard never write them.

type planView struct {
	plans.Plan
	Includes []string `json:"includes"`
}

func viewOf(p plans.Plan) planView { return planView{Plan: p, Includes: plans.Includes(p)} }

// ListPlans handles GET /plans: every plan, ascending, with what it includes.
func (h *Handler) ListPlans(c *gin.Context) {
	out := []planView{}
	for _, p := range plans.All() {
		out = append(out, viewOf(p))
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.JSON(http.StatusOK, gin.H{"plans": out, "currency": "GTQ", "payments_enabled": billing.Enabled(),
		"site_levels": plans.SiteLevels, "budgets": plans.Budgets()})
}

type quoteRequest struct {
	Plan   string            `json:"plan"`
	Addons []plans.Selection `json:"addons"`
	// Needs: the analysis' modules (most needed first), to order the
	// next plan's features.
	Needs []string `json:"needs"`
}

// Quote handles POST /plans/quote, the step after picking a plan: what the
// plan includes, what the next one adds ("Te puede interesar",
// getNextPlanFeatures) and the price of the selection (calculateAddonPrice).
func (h *Handler) Quote(c *gin.Context) {
	var req quoteRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Addons) > 30 || len(req.Needs) > 40 {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	p, ok := plans.Get(req.Plan)
	if !ok {
		respondError(c, http.StatusBadRequest, "plan desconocido")
		return
	}
	next, features, _ := plans.NextPlanFeatures(p.ID, req.Needs)
	q, err := plans.CalculateAddonPrice(p.ID, req.Addons)
	if err != nil {
		respondError(c, http.StatusBadRequest, quoteError(err))
		return
	}
	var nextView *planView
	if next != nil {
		v := viewOf(*next)
		nextView = &v
	}
	// The website: what the plan includes and what it can buy (once + monthly).
	c.JSON(http.StatusOK, gin.H{"plan": viewOf(p), "next_plan": nextView, "features": features, "quote": q, "site_options": plans.SiteOptions(p.ID)})
}

// quoteError says in Spanish what's wrong with a selection.
func quoteError(err error) string {
	switch {
	case errors.Is(err, plans.ErrUnknownPlan):
		return "plan desconocido"
	case errors.Is(err, plans.ErrNotOffered):
		return "ese complemento no está disponible para tu plan"
	case errors.Is(err, plans.ErrQty):
		return "revisa la cantidad del complemento"
	case errors.Is(err, plans.ErrMissingRequire):
		_, detail, _ := strings.Cut(err.Error(), "necesita ")
		return "ese complemento necesita " + detail
	}
	return "selección inválida"
}

// accountCaps is what the account may do: its plan plus the add-ons it
// bought (account_plan). Every gate asks this, never the plan's name.
func (h *Handler) accountCaps(ctx context.Context, accountID string) (plans.Capabilities, error) {
	acc, err := db.GetAccountByID(ctx, h.Accounts, accountID)
	if err != nil {
		return plans.Capabilities{}, err
	}
	var addons []plans.Selection
	if ap, err := db.GetAccountPlan(ctx, h.Accounts, accountID); err == nil {
		_ = json.Unmarshal(ap.Addons, &addons)
	}
	return plans.Effective(acc.Plan, addons), nil
}

// errCreatorQuota: the month's Creator tasks are used up.
var errCreatorQuota = errors.New("creator quota")

// creatorQuotaLeft is how many Creator tasks the account may still create
// this month (todo.md B2): the plan's (0/0/1/2/4) plus the creator_task
// add-ons, minus what it already used. The website build doesn't count.
func (h *Handler) creatorQuotaLeft(ctx context.Context, accountID string) (left, limit int, err error) {
	caps, err := h.accountCaps(ctx, accountID)
	if err != nil {
		return 0, 0, err
	}
	used, err := db.CountCreatorTasksThisMonth(ctx, h.Accounts, accountID)
	if err != nil {
		return 0, 0, err
	}
	left = caps.CreatorTasks - used
	if left < 0 {
		left = 0
	}
	return left, caps.CreatorTasks, nil
}

func creatorQuotaMessage(limit int) string {
	if limit == 0 {
		return "Tu plan no incluye tareas de Creators. Súmalas con el complemento \"Tarea de Creator\" o con un plan mayor."
	}
	if limit == 1 {
		return "Ya usaste la tarea de Creators de este mes. Súmale una con el complemento \"Tarea de Creator\" o espera al próximo mes."
	}
	return fmt.Sprintf("Ya usaste las %d tareas de Creators de este mes. Súmale una con el complemento \"Tarea de Creator\" o espera al próximo mes.", limit)
}

// AdminPaymentSkipped handles GET /admin/billing/skipped: the accounts
// created without paying (db.PaymentSkippedQuery), to charge or migrate
// them once payments are on.
func (h *Handler) AdminPaymentSkipped(c *gin.Context) {
	list, err := db.ListPaymentSkipped(c.Request.Context(), h.Accounts)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": list, "query": db.PaymentSkippedQuery})
}

type buySiteRequest struct {
	Level string `json:"level"` // basico | tienda | pro
}

// BuySite handles POST /account/site/buy (owner): the website bought after
// signup. Its construction is charged once and its upkeep joins the monthly
// total (account_plan); a site that was only offered starts being built.
func (h *Handler) BuySite(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req buySiteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	acc, err := db.GetAccountByID(ctx, h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	var offer *plans.SiteOption
	for _, o := range plans.SiteOptions(acc.Plan) {
		if o.ID == req.Level && !o.Included {
			o := o
			offer = &o
		}
	}
	if offer == nil {
		respondError(c, http.StatusBadRequest, "ese sitio no está disponible para tu plan")
		return
	}
	ap, err := db.GetAccountPlan(ctx, h.Accounts, claims.AccountID)
	if err != nil {
		p := plans.MustGet(acc.Plan)
		ap = db.AccountPlan{AccountID: acc.ID, Plan: p.ID, BasePrice: p.Price, Total: p.Price, BillingStatus: "pending_payment", PaymentSkipped: true, PaymentProvider: "noop"}
	}
	var addons []plans.Selection
	_ = json.Unmarshal(ap.Addons, &addons)
	// One site per account: a higher level replaces the one bought before.
	kept := []plans.Selection{}
	for _, a := range addons {
		if plans.IsSite(a.FeatureID) {
			for _, l := range plans.SiteOptions(acc.Plan) {
				if l.FeatureID == a.FeatureID || "sitio_"+l.ID == a.FeatureID {
					ap.AddonsTotal -= l.Monthly
				}
			}
			continue
		}
		kept = append(kept, a)
	}
	kept = append(kept, plans.Selection{FeatureID: offer.FeatureID, Qty: 1})
	ap.Addons, _ = json.Marshal(kept)
	ap.AddonsTotal += offer.Monthly
	ap.Total = ap.BasePrice + ap.AddonsTotal
	ap.SetupTotal += offer.Setup
	if err := db.SaveAccountPlan(ctx, h.Accounts, ap); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), acc.ID, &claims.Sub, "site_bought",
		fmt.Sprintf("%s: %s una vez y %s al mes", offer.Label, plans.FormatQ(offer.Setup), plans.FormatQ(offer.Monthly)))
	h.activateOfferedSite(ctx, acc.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true, "site": offer, "monthly_total": ap.Total, "setup_total": ap.SetupTotal})
}
