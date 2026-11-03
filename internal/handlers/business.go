package handlers

// The account's type of business (internal/business) and its AndRho
// website: what the dashboard reads to order its sections, what the owner
// changes in Configuración, and the public data the sites service
// (andrho-tracker-dashboard/sites, on Railway) renders a site from.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/business"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/insights"
)

type businessOption struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Examples    []string `json:"examples"`
}

func businessOptions() []businessOption {
	out := []businessOption{}
	for _, k := range business.Keys {
		p := business.Profiles[k]
		out = append(out, businessOption{p.Key, p.Name, p.Description, p.Examples})
	}
	return out
}

// GetBusinessType handles GET /account/business-type.
func (h *Handler) GetBusinessType(c *gin.Context) {
	claims, _ := currentClaims(c)
	typ, err := db.BusinessType(c.Request.Context(), h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	var profile any
	if p, ok := business.Get(typ); ok {
		profile = p
	}
	c.JSON(http.StatusOK, gin.H{"business_type": typ, "profile": profile, "options": businessOptions()})
}

type setBusinessTypeRequest struct {
	BusinessType string `json:"business_type"`
}

// SetBusinessType handles PUT /account/business-type (admin+). Changing it
// also proposes preparing the ERP for the new type: the owner accepts and
// AndRho installs the apps (accept.go).
func (h *Handler) SetBusinessType(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req setBusinessTypeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	typ := strings.TrimSpace(req.BusinessType)
	p, ok := business.Get(typ)
	if typ != "" && !ok {
		respondError(c, http.StatusBadRequest, "tipo de negocio desconocido")
		return
	}
	ctx := c.Request.Context()
	if err := db.SetBusinessType(ctx, h.Accounts, claims.AccountID, typ); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "business_type_changed",
		"Tipo de negocio: "+map[bool]string{true: p.Name, false: "sin tipo"}[ok])

	if ok {
		if _, err := db.GetOdooConnection(ctx, h.Accounts, claims.AccountID); err == nil {
			apps := business.OdooApps(typ, nil)
			prop := insights.Proposal{
				Title:    "Prepara tu ERP para " + strings.ToLower(p.Name),
				Body:     "Activamos en tu ERP lo que usan los negocios de " + strings.ToLower(p.Name) + ". Al aceptar, AndRho lo instala por ti.",
				Evidence: "Apps de Odoo del perfil " + typ + ": " + strings.Join(apps, ", ") + ".",
				Explain: &insights.Explain{Kind: "pasos", Headline: "Cambiaste tu tipo de negocio a " + p.Name + ".",
					Takeaway:   "Con lo que usan los negocios como el tuyo activo, AndRho puede fijarse en: " + strings.ToLower(strings.Join(p.Focus, "; ")) + ".",
					Confidence: "alta", ConfidenceText: "Es la configuración que AndRho usa para todos los negocios de este tipo."},
				Action: &insights.Action{Type: "erp_install", Modules: apps, Label: "Preparar mi ERP"},
			}
			_, _, _ = h.storeProposals(ctx, claims.AccountID, "inicio", []insights.Proposal{prop})
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "business_type": typ})
}

// AccountSite handles GET /account/site: the account's AndRho website.
func (h *Handler) AccountSite(c *gin.Context) {
	claims, _ := currentClaims(c)
	s, err := db.GetAccountSite(c.Request.Context(), h.Accounts, claims.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"site": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"site": gin.H{"host": s.Host, "url": "https://" + s.Host, "status": s.Status, "live_url": s.LiveURL}})
}

// PublicSite handles GET /public/site?host=…: what the sites service needs
// to render a business' site while it's being built. Only public data:
// the name, the type's site structure and whether there's a logo; never
// the owner's contact details.
func (h *Handler) PublicSite(c *gin.Context) {
	host := strings.ToLower(strings.TrimSpace(c.Query("host")))
	ctx := c.Request.Context()
	s, err := db.GetAccountSiteByHost(ctx, h.Accounts, host)
	if err != nil {
		respondError(c, http.StatusNotFound, "sitio no encontrado")
		return
	}
	acc, err := db.GetAccountByID(ctx, h.Accounts, s.AccountID)
	if err != nil {
		respondError(c, http.StatusNotFound, "sitio no encontrado")
		return
	}
	typ, _ := db.BusinessType(ctx, h.Accounts, acc.ID)
	p, _ := business.Get(typ)
	setup, _ := db.GetAccountSetup(ctx, h.Accounts, acc.ID)
	c.Header("Cache-Control", "public, max-age=60")
	c.JSON(http.StatusOK, gin.H{
		"site_id": acc.SiteID, "company": acc.CompanyName, "business_type": typ, "type_name": p.Name,
		"status": s.Status, "live_url": s.LiveURL, "site": p.Site, "essence": setup.BrandEssence,
		"has_logo": len(setup.Logo) > 0, "logo_url": "/public/sites/" + acc.SiteID + "/logo",
	})
}

// PublicLogo handles GET /public/sites/:siteId/logo, for the site.
func (h *Handler) PublicLogo(c *gin.Context) {
	acc, err := db.GetAccountBySiteID(c.Request.Context(), h.Accounts, c.Param("siteId"))
	if err != nil {
		respondError(c, http.StatusNotFound, "sin logo")
		return
	}
	setup, err := db.GetAccountSetup(c.Request.Context(), h.Accounts, acc.ID)
	if err != nil || len(setup.Logo) == 0 {
		respondError(c, http.StatusNotFound, "sin logo")
		return
	}
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, setup.LogoType, setup.Logo)
}
