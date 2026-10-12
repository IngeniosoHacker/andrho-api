package handlers

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/analyzer"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// POST /auth/register is the signup with setup: the account, the package
// the business picked (plan + modules), what it told us (survey answers or a
// description), its brand ("Cuéntanos de tu negocio") and the provisioning
// of every service that needs it (internal/provision). Both signup paths
// (quick and custom) end here. Nothing is charged yet: every plan is free
// during the beta.

const (
	maxBrandEssence = 1000
	maxLogoBytes    = 1 << 20
)

type registerRequest struct {
	Email       string             `json:"email"`
	Password    string             `json:"password"`
	CompanyName string             `json:"company_name"`
	DisplayName string             `json:"display_name"`
	Setup       string             `json:"setup"` // rapida | personalizada
	Plan        string             `json:"plan"`  // despegue | en_orbita | galactico
	Modules     []string           `json:"modules"`
	Answers     *recommend.Answers `json:"answers"`
	Description string             `json:"description"`
	Phone       string             `json:"phone"`
	Brand       struct {
		Essence string `json:"essence"`
		Help    bool   `json:"help"` // no brand yet, wants AndRho to propose one
		Logo    string `json:"logo"` // data:image/png|jpeg|webp;base64,...
	} `json:"brand"`
	Provision provision.Request `json:"provision"`
}

var logoRe = regexp.MustCompile(`^data:(image/(?:png|jpeg|webp));base64,([A-Za-z0-9+/=\s]+)$`)

// Register handles POST /auth/register.
func (h *Handler) Register(c *gin.Context) {
	// The logo travels inside the JSON (base64): cap the body at 2 MB.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.CompanyName = strings.TrimSpace(req.CompanyName)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Description = strings.TrimSpace(req.Description)
	req.Brand.Essence = strings.TrimSpace(req.Brand.Essence)
	if req.Setup != "personalizada" {
		req.Setup = "rapida"
	}
	if req.Plan == "" {
		req.Plan = "despegue"
	}
	if req.DisplayName == "" {
		req.DisplayName = req.CompanyName
	}

	switch {
	case !emailRe.MatchString(req.Email):
		respondError(c, http.StatusBadRequest, "invalid email")
		return
	case len(req.Password) < 8:
		respondError(c, http.StatusBadRequest, "password must be at least 8 characters")
		return
	case req.CompanyName == "" || utf8.RuneCountInString(req.CompanyName) > 120:
		respondError(c, http.StatusBadRequest, "company_name is required")
		return
	case !validPlans[req.Plan] || req.Plan == "base":
		respondError(c, http.StatusBadRequest, "plan must be despegue, en_orbita or galactico")
		return
	case utf8.RuneCountInString(req.Description) > analyzer.MaxDescription:
		respondError(c, http.StatusBadRequest, "la descripción no puede pasar de 2000 caracteres")
		return
	case utf8.RuneCountInString(req.Brand.Essence) > maxBrandEssence:
		respondError(c, http.StatusBadRequest, "la esencia de marca no puede pasar de 1000 caracteres")
		return
	}

	modules := []string{}
	seen := map[string]bool{}
	for _, m := range req.Modules {
		if _, ok := analyzer.Modules[m]; ok && !seen[m] && m != "creators" {
			seen[m] = true
			modules = append(modules, m)
		}
	}
	if limit := analyzer.PlanModuleLimit(req.Plan); limit >= 0 && len(modules) > limit {
		respondError(c, http.StatusBadRequest, "ese paquete incluye hasta "+strconv.Itoa(limit)+" módulos")
		return
	}

	var logoType string
	var logo []byte
	if req.Brand.Logo != "" {
		m := logoRe.FindStringSubmatch(req.Brand.Logo)
		if m == nil {
			respondError(c, http.StatusBadRequest, "el logo debe ser PNG, JPG o WebP")
			return
		}
		data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(m[2]), ""))
		if err != nil || len(data) == 0 {
			respondError(c, http.StatusBadRequest, "no pudimos leer el logo")
			return
		}
		if len(data) > maxLogoBytes {
			respondError(c, http.StatusBadRequest, "el logo no puede pesar más de 1 MB")
			return
		}
		if ct := http.DetectContentType(data); ct != m[1] {
			respondError(c, http.StatusBadRequest, "el logo no es la imagen que dice ser")
			return
		}
		logoType, logo = m[1], data
	}

	var answers json.RawMessage = []byte(`{}`)
	var sectors []string
	if req.Answers != nil {
		clean := analyzer.Sanitize(*req.Answers)
		sectors = clean.Sectors
		answers, _ = json.Marshal(clean)
	}

	ctx := c.Request.Context()
	acc, user, status, msg := h.createAccountWithOwner(ctx, req.Email, req.Password, req.CompanyName, req.DisplayName, req.Plan)
	if status != 0 {
		respondError(c, status, msg)
		return
	}

	setup := db.AccountSetup{
		AccountID: acc.ID, Setup: req.Setup, Answers: answers, Description: req.Description, Modules: modules,
		Phone: strings.TrimSpace(req.Phone), BrandEssence: req.Brand.Essence, BrandHelp: req.Brand.Help,
		LogoType: logoType, Logo: logo,
	}
	if err := db.SaveAccountSetup(ctx, h.Accounts, setup); err != nil {
		_ = db.DeleteAccount(ctx, h.Accounts, acc.ID)
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	// The register(erp, meta, whatsapp, web_page) of the spec: whatever the
	// request left empty is filled from the modules and the phone.
	pr := req.Provision
	pr.Fill(modules, setup.Phone)
	pacc := provision.Account{ID: acc.ID, ClientID: acc.SiteID, Company: acc.CompanyName, Plan: acc.Plan}
	jobs, err := h.Provision.Plan(ctx, pacc, pr, provision.Brief{
		Company: acc.CompanyName, ClientID: acc.SiteID, Essence: req.Brand.Essence, BrandHelp: req.Brand.Help,
		HasLogo: len(logo) > 0, Modules: modules, Sectors: sectors,
	})
	if err != nil {
		_ = db.DeleteAccount(ctx, h.Accounts, acc.ID)
		respondError(c, http.StatusBadRequest, "revisa el número de WhatsApp")
		return
	}

	tokens, err := h.issueTokenPair(ctx, user, acc)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), acc.ID, &user.ID, "account_created",
		"Cuenta creada con el paquete "+req.Plan+" ("+req.Setup+")")
	h.Provision.RunAll(pacc, jobs)

	c.JSON(http.StatusCreated, gin.H{
		"access_token": tokens.AccessToken, "refresh_token": tokens.RefreshToken,
		"account": acc.ToPublic(), "plan": acc.Plan, "modules": modules, "provisioning": jobs,
	})
}

// AccountProvisioning handles GET /account/provisioning: the setup the
// signup recorded (without the logo bytes), every provisioning job and the
// Creators tasks for the account.
func (h *Handler) AccountProvisioning(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	ctx := c.Request.Context()
	jobs, err := db.ListProvisioningJobs(ctx, h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	tasks, err := db.ListAccountCreatorTasks(ctx, h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	resp := gin.H{"jobs": jobs, "tasks": tasks, "setup": nil}
	if setup, err := db.GetAccountSetup(ctx, h.Accounts, claims.AccountID); err == nil {
		resp["setup"] = setup
	}
	c.JSON(http.StatusOK, resp)
}

// AccountLogo handles GET /account/logo: the logo uploaded at signup.
func (h *Handler) AccountLogo(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	h.serveLogo(c, claims.AccountID)
}

func (h *Handler) serveLogo(c *gin.Context, accountID string) {
	setup, err := db.GetAccountSetup(c.Request.Context(), h.Accounts, accountID)
	if err != nil || len(setup.Logo) == 0 {
		respondError(c, http.StatusNotFound, "sin logo")
		return
	}
	c.Header("Cache-Control", "private, max-age=300")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, setup.LogoType, setup.Logo)
}
