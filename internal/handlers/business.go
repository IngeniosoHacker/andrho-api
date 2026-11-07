package handlers

// The account's type of business (internal/business) and its AndRho
// website: what the dashboard reads to order its sections, what the owner
// changes in Configuración, and the public data sites-router and each
// client's site service (andrho-tracker-dashboard/sites, on Railway's
// Clients project) read.

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/business"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/insights"
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
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

// AccountSite handles GET /account/site: the account's AndRho website. An
// offered site (todo.md Parte B) says how to turn it on.
func (h *Handler) AccountSite(c *gin.Context) {
	claims, _ := currentClaims(c)
	ctx := c.Request.Context()
	s, err := db.GetAccountSite(ctx, h.Accounts, claims.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"site": nil})
		return
	}
	seo, _ := db.GetSiteSEO(ctx, h.Accounts, claims.AccountID)
	caps, _ := h.accountCaps(ctx, claims.AccountID)
	out := gin.H{"host": s.Host, "url": "https://" + s.PublicHost(), "subdomain": s.Subdomain, "status": s.Status,
		"live_url": s.LiveURL, "custom_domain": s.CustomDomain, "seo": seo, "seo_level": caps.SEOLevel()}
	if s.Status == db.SiteOffered {
		out["offer"] = "Tu plan no incluye sitio web. Actívalo con el plan Despegue o con el complemento Sitio web básico; tu dirección " +
			s.Host + " queda reservada."
	}
	c.JSON(http.StatusOK, gin.H{"site": out})
}

// sitePayload is what sites-router and the client's own service read about
// a site. Only public data: never the owner's contact details.
func (h *Handler) sitePayload(ctx context.Context, s db.AccountSite) (gin.H, error) {
	acc, err := db.GetAccountByID(ctx, h.Accounts, s.AccountID)
	if err != nil {
		return nil, err
	}
	typ, _ := db.BusinessType(ctx, h.Accounts, acc.ID)
	p, _ := business.Get(typ)
	setup, _ := db.GetAccountSetup(ctx, h.Accounts, acc.ID)
	seo, _ := db.GetSiteSEO(ctx, h.Accounts, acc.ID)
	caps, _ := h.accountCaps(ctx, acc.ID)
	out := gin.H{
		"site_id": acc.SiteID, "company": acc.CompanyName, "business_type": typ, "type_name": p.Name,
		"status": s.Status, "host": s.Host, "public_host": s.PublicHost(), "legacy_host": s.LegacyHost,
		// published: the Creator delivered the real site (until then the
		// template shows "en construcción" and stays noindex).
		"published": s.LiveURL != "",
		"site": p.Site, "essence": setup.BrandEssence,
		"has_logo": len(setup.Logo) > 0, "logo_url": "/public/sites/" + acc.SiteID + "/logo",
		"seo": gin.H{"title": seo.Title, "description": seo.Description, "og_image": seo.OGImage, "lang": seo.Lang,
			"gsc_token": seo.GSCToken, "branches": seo.Branches},
		// The plan's SEO as flags (todo.md A4.6), never the plan's name.
		"features": gin.H{"website": caps.Website, "store": caps.Store, "full_site": caps.FullSite, "seo_level": caps.SEOLevel()},
	}
	if s.Status == db.SiteLive && s.RailwayServiceName != "" {
		out["upstream"] = gin.H{"service": s.RailwayServiceName, "port": h.Provision.Sites.Port}
	}
	return out, nil
}

// PublicSite handles GET /public/site?host=…, sites-router's lookup: the
// client's own domain first, then its subdomain, then an old host. An old
// host (or the subdomain of a site with its own domain) comes back with
// "redirect", the host to answer a 301 to.
func (h *Handler) PublicSite(c *gin.Context) {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.Query("host"))), ".")
	ctx := c.Request.Context()
	s, err := db.GetAccountSiteByHost(ctx, h.Accounts, host)
	if err != nil || s.Status == db.SiteOffered {
		respondError(c, http.StatusNotFound, "sitio no encontrado")
		return
	}
	out, err := h.sitePayload(ctx, s)
	if err != nil {
		respondError(c, http.StatusNotFound, "sitio no encontrado")
		return
	}
	if host != s.PublicHost() {
		out["redirect"] = s.PublicHost()
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.JSON(http.StatusOK, out)
}

// PublicSiteByID handles GET /public/sites/:siteId: the same data, for the
// client's own service (SITE_MODE=site, which knows its SITE_ID).
func (h *Handler) PublicSiteByID(c *gin.Context) {
	ctx := c.Request.Context()
	acc, err := db.GetAccountBySiteID(ctx, h.Accounts, c.Param("siteId"))
	if err != nil {
		respondError(c, http.StatusNotFound, "sitio no encontrado")
		return
	}
	s, err := db.GetAccountSite(ctx, h.Accounts, acc.ID)
	if err != nil || s.Status == db.SiteOffered {
		respondError(c, http.StatusNotFound, "sitio no encontrado")
		return
	}
	out, err := h.sitePayload(ctx, s)
	if err != nil {
		respondError(c, http.StatusNotFound, "sitio no encontrado")
		return
	}
	c.Header("Cache-Control", "public, max-age=60")
	c.JSON(http.StatusOK, out)
}

type siteBranch struct {
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Phone   string   `json:"phone"`
	Hours   string   `json:"hours"` // "Mo-Fr 08:00-18:00" (schema.org openingHours)
	Lat     *float64 `json:"lat"`
	Lng     *float64 `json:"lng"`
}

type siteSEORequest struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	OGImage     string       `json:"og_image"`
	GSCToken    string       `json:"gsc_token"`
	Branches    []siteBranch `json:"branches"`
}

var (
	gscTokenRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{10,100}$`)
	gscContentRe = regexp.MustCompile(`content\s*=\s*["']([^"']+)["']`)
)

// SetSiteSEO handles PUT /account/site/seo (admin): the site's title,
// description, image, Search Console token (the token, or the whole
// <meta name="google-site-verification" …> tag pasted) and branches.
func (h *Handler) SetSiteSEO(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req siteSEORequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	trim := func(s string, n int) (string, bool) {
		s = strings.TrimSpace(s)
		return s, utf8.RuneCountInString(s) <= n
	}
	var ok bool
	if req.Title, ok = trim(req.Title, 70); !ok {
		respondError(c, http.StatusBadRequest, "el título no puede pasar de 70 caracteres")
		return
	}
	if req.Description, ok = trim(req.Description, 160); !ok {
		respondError(c, http.StatusBadRequest, "la descripción no puede pasar de 160 caracteres")
		return
	}
	req.OGImage = strings.TrimSpace(req.OGImage)
	if req.OGImage != "" && (!strings.HasPrefix(req.OGImage, "https://") || len(req.OGImage) > 500) {
		respondError(c, http.StatusBadRequest, "la imagen debe ser un enlace https")
		return
	}
	req.GSCToken = strings.TrimSpace(req.GSCToken)
	if m := gscContentRe.FindStringSubmatch(req.GSCToken); m != nil {
		req.GSCToken = m[1]
	}
	if req.GSCToken != "" && !gscTokenRe.MatchString(req.GSCToken) {
		respondError(c, http.StatusBadRequest, "el código de Google Search Console no es válido")
		return
	}
	if len(req.Branches) > 50 {
		respondError(c, http.StatusBadRequest, "hasta 50 sucursales")
		return
	}
	for i, b := range req.Branches {
		for _, f := range []*string{&b.Name, &b.Address, &b.Phone, &b.Hours} {
			if *f, ok = trim(*f, 200); !ok {
				respondError(c, http.StatusBadRequest, "revisa los datos de las sucursales")
				return
			}
		}
		if b.Name == "" || (b.Lat != nil && (*b.Lat < -90 || *b.Lat > 90)) || (b.Lng != nil && (*b.Lng < -180 || *b.Lng > 180)) {
			respondError(c, http.StatusBadRequest, "revisa los datos de las sucursales")
			return
		}
		req.Branches[i] = b
	}
	if req.Branches == nil {
		req.Branches = []siteBranch{}
	}
	branches, _ := json.Marshal(req.Branches)
	seo := db.SiteSEO{AccountID: claims.AccountID, Title: req.Title, Description: req.Description, OGImage: req.OGImage,
		Lang: "es-GT", GSCToken: req.GSCToken, Branches: branches}
	if err := db.SaveSiteSEO(c.Request.Context(), h.Accounts, seo); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "seo": seo})
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

// activateOfferedSite starts the site of an account whose site was only
// offered, once its plan (or an add-on) includes one: the same website job
// as at signup, without repeating the onboarding.
func (h *Handler) activateOfferedSite(ctx context.Context, accountID string) {
	s, err := db.GetAccountSite(ctx, h.Accounts, accountID)
	if err != nil || s.Status != db.SiteOffered {
		return
	}
	if caps, err := h.accountCaps(ctx, accountID); err != nil || !caps.Website {
		return
	}
	pacc, err := h.ProvisionAccount(ctx, accountID)
	if err != nil {
		return
	}
	job, err := h.Provision.PlanWebsite(ctx, pacc, h.websiteBrief(ctx, pacc))
	if err != nil {
		log.Printf("site: activate %s: %v", pacc.ClientID, err)
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), accountID, nil, "site_activated", "Tu plan ya incluye sitio web: empezamos a construirlo.")
	h.Provision.RunAll(pacc, []db.ProvisioningJob{job})
}

// websiteBrief rebuilds the Creator's brief from what the signup recorded.
func (h *Handler) websiteBrief(ctx context.Context, acc provision.Account) provision.Brief {
	b := provision.Brief{Company: acc.Company, ClientID: acc.ClientID}
	if setup, err := db.GetAccountSetup(ctx, h.Accounts, acc.ID); err == nil {
		var answers struct {
			Sectors []string `json:"sectors"`
		}
		_ = json.Unmarshal(setup.Answers, &answers)
		b.Essence, b.BrandHelp, b.HasLogo, b.Modules, b.Sectors = setup.BrandEssence, setup.BrandHelp, len(setup.Logo) > 0, setup.Modules, answers.Sectors
	}
	if typ, err := db.BusinessType(ctx, h.Accounts, acc.ID); err == nil {
		b.BusinessType, b.Site = typ, siteOf(typ)
	}
	return b
}
