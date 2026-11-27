package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/live"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// Personal en obra (construction sites without computers or company
// phones; audit 2026-11-19). The owner or an admin registers the work
// sites (obras, apart from the branches) and gives each one supervisors:
// AndRho users with a linked WhatsApp and the "supervisor" role, which
// only reaches the staff of their own sites.
//
// From the site, by WhatsApp to AndRho's number:
//   - a worker's photo with their name or internal code (the company picks
//     which) registers or updates them in the client's ERP (hr.employee),
//     at that site; with more than one site AndRho asks which one;
//   - "Cuadrilla Losa Norte: Juan, Pedro, en el nivel 3" reports a crew:
//     Claude reads the free text into fields, never adding what the
//     message doesn't say, and members must exist at the site;
//   - "Baja de Juan Pérez" archives the worker and erases their photo.
//
// Nothing is saved before the supervisor confirms. Photos go from Meta
// straight to the ERP: AndRho keeps no copy and never sends them to Claude.

const (
	featureWorkforce = "workforce"
	workforceModule  = "andrho_workforce"
	crewModel        = "andrho.crew"
	employeeModel    = "hr.employee"
	locationModel    = "hr.work.location"
	wfIDModeParam    = "andrho.workforce.id_mode"
	waWorkforceKind  = "workforce"
)

// workforceFeature: available with the plan's Workforce (En órbita,
// Galaxy); enabled when turned on, or by default for type construccion.
func (h *Handler) workforceFeature(ctx context.Context, accountID string) (featureState, error) {
	caps, err := h.accountCaps(ctx, accountID)
	if err != nil {
		return featureState{}, err
	}
	st := featureState{Available: caps.Workforce}
	if !st.Available {
		return st, nil
	}
	on, set, err := db.FeatureSetting(ctx, h.Accounts, accountID, featureWorkforce)
	if err != nil {
		return st, err
	}
	if set {
		st.Enabled = on
	} else {
		bt, _ := db.BusinessType(ctx, h.Accounts, accountID)
		st.Enabled = bt == "construccion"
	}
	return st, nil
}

// workforceGate answers 403 unless the account has Personal en obra on.
func (h *Handler) workforceGate(c *gin.Context) (*auth.AccessClaims, bool) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return nil, false
	}
	st, err := h.workforceFeature(c.Request.Context(), claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	if !st.Available {
		c.JSON(http.StatusForbidden, gin.H{"error": "plan", "message": "Personal en obra viene en los planes En órbita y Galaxy."})
		return nil, false
	}
	if !st.Enabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "feature_off", "message": "Personal en obra está apagado. Actívalo en Configuración."})
		return nil, false
	}
	return claims, true
}

// sitesFor are the sites a user may manage: their own for a supervisor,
// all the active ones for admins and the owner.
func (h *Handler) sitesFor(ctx context.Context, accountID, userID, role string) ([]db.WorkSite, error) {
	if role == "supervisor" {
		return db.SupervisorSites(ctx, h.Accounts, accountID, userID)
	}
	if auth.HasAtLeast(role, "admin") {
		return db.ListWorkSites(ctx, h.Accounts, accountID, false)
	}
	return []db.WorkSite{}, nil
}

// syncSite makes sure the site exists in the ERP (hr.work.location).
func (h *Handler) syncSite(ctx context.Context, cl *odoo.Client, s *db.WorkSite) error {
	var id int
	if err := cl.Exec(ctx, locationModel, "andrho_sync_site", []any{s.ID, s.Name, s.Active}, nil, &id); err != nil {
		return err
	}
	if id != s.OdooLocationID {
		s.OdooLocationID = id
		return db.SaveWorkSite(ctx, h.Accounts, *s)
	}
	return nil
}

// workforceERP is the account's ERP with andrho_workforce installed.
func (h *Handler) workforceERP(ctx context.Context, accountID string) (*odoo.Client, error) {
	cl, err := h.odooFor(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !cl.HasModel(ctx, crewModel) {
		return nil, errWorkforceNotReady
	}
	return cl, nil
}

var errWorkforceNotReady = errors.New("workforce: erp module not installed")

func idMode(ctx context.Context, cl *odoo.Client) string {
	v, _ := erpGetParam(ctx, cl, wfIDModeParam)
	if v == "code" || v == "both" {
		return v
	}
	return "name"
}

// --- dashboard ---------------------------------------------------------------------

type siteView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Active      bool     `json:"active"`
	Supervisors []string `json:"supervisors"`
	InERP       bool     `json:"in_erp"`
}

func siteViews(sites []db.WorkSite) []siteView {
	out := make([]siteView, 0, len(sites))
	for _, s := range sites {
		out = append(out, siteView{ID: s.ID, Name: s.Name, Active: s.Active, Supervisors: s.Supervisors, InERP: s.OdooLocationID > 0})
	}
	return out
}

// WorkforceStatus handles GET /workforce/status.
func (h *Handler) WorkforceStatus(c *gin.Context) {
	claims, _ := currentClaims(c)
	ctx := c.Request.Context()
	st, err := h.workforceFeature(ctx, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	out := gin.H{"available": st.Available, "enabled": st.Enabled, "erp": "not_connected", "whatsapp": h.WA.Enabled(),
		"can_manage": auth.HasAtLeast(claims.Role, "admin"), "id_mode": "name", "sites": []siteView{}}
	if !st.Enabled {
		c.JSON(http.StatusOK, out)
		return
	}
	var sites []db.WorkSite
	if auth.HasAtLeast(claims.Role, "admin") {
		sites, err = db.ListWorkSites(ctx, h.Accounts, claims.AccountID, true)
	} else {
		sites, err = h.sitesFor(ctx, claims.AccountID, claims.Sub, claims.Role)
	}
	if err == nil {
		out["sites"] = siteViews(sites)
	}
	if cl, err := h.odooFor(ctx, claims.AccountID); err == nil {
		out["erp"] = "connected"
		if cl.HasModel(ctx, crewModel) {
			out["erp"] = "ready"
			out["id_mode"] = idMode(ctx, cl)
		}
	}
	if phone, _, err := db.UserWhatsApp(ctx, h.Accounts, claims.Sub); err == nil {
		out["my_whatsapp"] = phone != ""
	}
	c.JSON(http.StatusOK, out)
}

// WorkforceSetup handles POST /workforce/setup (admin): installs the ERP's
// module and puts the sites in it.
func (h *Handler) WorkforceSetup(c *gin.Context) {
	claims, ok := h.workforceGate(c)
	if !ok {
		return
	}
	cl, err := h.odooFor(c.Request.Context(), claims.AccountID)
	if err != nil {
		respondError(c, http.StatusNotFound, "erp_not_connected")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if !cl.HasModel(ctx, crewModel) {
		if err := cl.UpdateModuleList(ctx); err != nil {
			log.Printf("workforce: update module list for %s: %v", claims.AccountID, err)
		}
		if _, err := cl.InstallModules(ctx, []string{workforceModule}); err != nil {
			log.Printf("workforce: install for %s: %v", claims.AccountID, err)
			c.JSON(http.StatusBadGateway, gin.H{"error": "erp_update", "message": "Estamos actualizando tu ERP. Vuelve a intentarlo en unos minutos."})
			return
		}
		installedModels.Delete(claims.AccountID)
	}
	sites, _ := db.ListWorkSites(ctx, h.Accounts, claims.AccountID, true)
	for i := range sites {
		if err := h.syncSite(ctx, cl, &sites[i]); err != nil {
			log.Printf("workforce: sync site %s: %v", sites[i].ID, err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"installed": true})
}

// WorkforceCreateSite handles POST /workforce/sites (admin): {name}.
func (h *Handler) WorkforceCreateSite(c *gin.Context) {
	claims, ok := h.workforceGate(c)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" || len([]rune(req.Name)) > 80 {
		respondError(c, http.StatusBadRequest, "el nombre de la obra va de 1 a 80 letras")
		return
	}
	ctx := c.Request.Context()
	s := db.WorkSite{ID: uuid.NewString(), AccountID: claims.AccountID, Name: strings.TrimSpace(req.Name), Active: true}
	if err := db.SaveWorkSite(ctx, h.Accounts, s); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if cl, err := h.workforceERP(ctx, claims.AccountID); err == nil {
		if err := h.syncSite(ctx, cl, &s); err != nil {
			log.Printf("workforce: sync site %s: %v", s.ID, err)
		}
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "work_site_created", "Obra creada: "+s.Name)
	c.JSON(http.StatusCreated, gin.H{"site": siteViews([]db.WorkSite{s})[0]})
}

// WorkforceUpdateSite handles PUT /workforce/sites/:id (admin): {name?, active?}.
func (h *Handler) WorkforceUpdateSite(c *gin.Context) {
	claims, ok := h.workforceGate(c)
	if !ok {
		return
	}
	var req struct {
		Name   *string `json:"name"`
		Active *bool   `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	s, err := db.GetWorkSite(ctx, h.Accounts, claims.AccountID, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len([]rune(n)) > 80 {
			respondError(c, http.StatusBadRequest, "el nombre de la obra va de 1 a 80 letras")
			return
		}
		s.Name = n
	}
	if req.Active != nil {
		s.Active = *req.Active
	}
	if err := db.SaveWorkSite(ctx, h.Accounts, s); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if cl, err := h.workforceERP(ctx, claims.AccountID); err == nil {
		_ = h.syncSite(ctx, cl, &s)
	}
	c.JSON(http.StatusOK, gin.H{"site": siteViews([]db.WorkSite{s})[0]})
}

// WorkforceSetSupervisors handles PUT /workforce/sites/:id/supervisors
// (admin): {user_ids}. Users of the account only.
func (h *Handler) WorkforceSetSupervisors(c *gin.Context) {
	claims, ok := h.workforceGate(c)
	if !ok {
		return
	}
	var req struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.UserIDs) > 50 {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	s, err := db.GetWorkSite(ctx, h.Accounts, claims.AccountID, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	if err := db.SetSiteSupervisors(ctx, h.Accounts, claims.AccountID, s.ID, req.UserIDs); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	s, _ = db.GetWorkSite(ctx, h.Accounts, claims.AccountID, s.ID)
	c.JSON(http.StatusOK, gin.H{"site": siteViews([]db.WorkSite{s})[0]})
}

// WorkforceSettings handles PUT /workforce/settings (admin): {id_mode}
// name | code | both: what a supervisor must give for each worker.
func (h *Handler) WorkforceSettings(c *gin.Context) {
	claims, ok := h.workforceGate(c)
	if !ok {
		return
	}
	var req struct {
		IDMode string `json:"id_mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.IDMode != "name" && req.IDMode != "code" && req.IDMode != "both") {
		respondError(c, http.StatusBadRequest, "id_mode es name, code o both")
		return
	}
	cl, err := h.workforceERP(c.Request.Context(), claims.AccountID)
	if err != nil {
		respondError(c, http.StatusConflict, "erp_not_ready")
		return
	}
	if err := erpSetParam(c.Request.Context(), cl, wfIDModeParam, req.IDMode); err != nil {
		respondError(c, http.StatusBadGateway, "no pudimos guardar eso en tu ERP")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id_mode": req.IDMode})
}

type wfEmployee struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Code       string   `json:"code"`
	SiteID     int      `json:"site_id"`
	Site       string   `json:"site"`
	HasPhoto   bool     `json:"has_photo"`
	Crews      []string `json:"crews"`
	ReportedBy string   `json:"reported_by"`
	ReportedAt string   `json:"reported_at"`
}

type wfCrew struct {
	ID         int          `json:"id"`
	Name       string       `json:"name"`
	SiteID     int          `json:"site_id"`
	Zone       string       `json:"zone"`
	Activity   string       `json:"activity"`
	Members    []wfEmployee `json:"members"`
	ReportedBy string       `json:"reported_by"`
	ReportedAt string       `json:"reported_at"`
}

type wfSite struct {
	ID        int          `json:"id"`
	Ref       string       `json:"ref"`
	Name      string       `json:"name"`
	Employees []wfEmployee `json:"employees"`
	Crews     []wfCrew     `json:"crews"`
}

// WorkforceStaff handles GET /workforce/staff: the staff by site, with the
// crews, where they work and who reported what and when. Supervisors see
// their sites only.
func (h *Handler) WorkforceStaff(c *gin.Context) {
	claims, ok := h.workforceGate(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	sites, err := h.sitesFor(ctx, claims.AccountID, claims.Sub, claims.Role)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	cl, err := h.workforceERP(ctx, claims.AccountID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "erp_not_ready", "message": "Activa Personal en obra en tu ERP."})
		return
	}
	locs := []int{}
	for i := range sites {
		if sites[i].OdooLocationID == 0 {
			_ = h.syncSite(ctx, cl, &sites[i])
		}
		if sites[i].OdooLocationID > 0 {
			locs = append(locs, sites[i].OdooLocationID)
		}
	}
	out := []wfSite{}
	if len(locs) > 0 {
		if err := cl.Exec(ctx, crewModel, "andrho_overview", []any{locs}, nil, &out); err != nil {
			log.Printf("workforce: overview %s: %v", claims.AccountID, err)
			respondError(c, http.StatusBadGateway, "no pudimos leer tu ERP ahora")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"sites": out})
}

// WorkforceTerminate handles POST /workforce/employees/:id/terminate: the
// worker leaves (archived, photo erased). Supervisors only for their sites.
func (h *Handler) WorkforceTerminate(c *gin.Context) {
	claims, ok := h.workforceGate(c)
	if !ok {
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		respondError(c, http.StatusBadRequest, "invalid id")
		return
	}
	ctx := c.Request.Context()
	cl, err := h.workforceERP(ctx, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusConflict, "erp_not_ready")
		return
	}
	sites, _ := h.sitesFor(ctx, claims.AccountID, claims.Sub, claims.Role)
	if !h.employeeInSites(ctx, cl, id, sites) {
		respondError(c, http.StatusForbidden, "insufficient permissions")
		return
	}
	if err := cl.Exec(ctx, employeeModel, "andrho_terminate", []any{[]int{id}}, nil, nil); err != nil {
		respondError(c, http.StatusBadGateway, "no pudimos dar de baja ahora")
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "worker_terminated", "Baja de un trabajador (foto borrada)")
	c.Status(http.StatusNoContent)
}

func (h *Handler) employeeInSites(ctx context.Context, cl *odoo.Client, employeeID int, sites []db.WorkSite) bool {
	var rows []map[string]any
	if err := cl.SearchRead(ctx, employeeModel, []any{[]any{"id", "=", employeeID}}, []string{"work_location_id"}, "", 1, &rows); err != nil || len(rows) == 0 {
		return false
	}
	loc := m2oID(rows[0]["work_location_id"])
	for _, s := range sites {
		if s.OdooLocationID > 0 && s.OdooLocationID == loc {
			return true
		}
	}
	return false
}

func m2oID(v any) int {
	if a, ok := v.([]any); ok && len(a) > 0 {
		if f, ok := a[0].(float64); ok {
			return int(f)
		}
	}
	return 0
}

// --- WhatsApp: the supervisor writes from the site -----------------------------------

// wfPending is what AndRho waits for from a supervisor (wa_sessions).
type wfPending struct {
	Kind        string         `json:"kind"` // employee | crew | terminate
	Step        string         `json:"step"` // site | name | code | pick | confirm
	SiteID      string         `json:"site_id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Code        string         `json:"code,omitempty"`
	MediaID     string         `json:"media_id,omitempty"`
	EmployeeID  int            `json:"employee_id,omitempty"`
	Matched     bool           `json:"matched,omitempty"` // EmployeeID chosen (0 = new)
	Candidates  []wfEmployee   `json:"candidates,omitempty"`
	CrewName    string         `json:"crew_name,omitempty"`
	Zone        string         `json:"zone,omitempty"`
	Activity    string         `json:"activity,omitempty"`
	MemberNames []string       `json:"member_names,omitempty"`
	MemberIDs   []int          `json:"member_ids,omitempty"`
	Unknown     []string       `json:"unknown,omitempty"`
	SiteHint    string         `json:"site_hint,omitempty"`
	Options     []wfSiteOption `json:"options,omitempty"`
}

type wfSiteOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const (
	wfSave    = "WF_SAVE"
	wfCancel  = "WF_CANCEL"
	wfSiteBtn = "WF_SITE:"
	wfPick    = "WF_PICK:"
	wfNew     = "WF_NEW"

	wfHelp = "Así me usas desde la obra:\n" +
		"• Alta: manda la foto del trabajador con su nombre o código en el texto.\n" +
		"• Cuadrilla: «Cuadrilla Losa Norte: Juan Pérez, Pedro Gómez, en el nivel 3, armado de losa».\n" +
		"• Baja: «Baja de Juan Pérez».\n" +
		"Antes de guardar te pido que confirmes. Escribe «cancelar» para empezar de nuevo."
	wfNoSites   = "No tienes obras asignadas. Pídele al administrador de tu cuenta que te asigne una en AndRho."
	wfCancelled = "Listo, no guardé nada."
	wfOff       = "Personal en obra no está activo en tu cuenta de AndRho."
)

// waWorkforce takes a supervisor's (or admin's) messages about staff.
func (h *Handler) waWorkforce(ctx context.Context, m whatsapp.Message) bool {
	u, err := db.UserByWhatsApp(ctx, h.Accounts, m.From)
	if err != nil || (u.Role != "supervisor" && !auth.HasAtLeast(u.Role, "admin")) {
		return false
	}
	if st, err := h.workforceFeature(ctx, u.AccountID); err != nil || !st.Enabled {
		if u.Role == "supervisor" {
			h.waSay(ctx, m.From, wfOff)
			return true
		}
		return false
	}
	sites, err := h.sitesFor(ctx, u.AccountID, u.ID, u.Role)
	if err != nil {
		return false
	}
	if len(sites) == 0 {
		if u.Role == "supervisor" {
			h.waSay(ctx, m.From, wfNoSites)
			return true
		}
		return false
	}
	cl, err := h.workforceERP(ctx, u.AccountID)
	if err != nil {
		h.waSay(ctx, m.From, "Tu ERP todavía no tiene Personal en obra. Pídele al administrador que lo active en AndRho.")
		return true
	}
	w := wfConv{h: h, cl: cl, u: u, sites: sites, phone: m.From}
	n := live.Normalize(m.Text)
	if m.ButtonID == wfCancel || n == "cancelar" || n == "cancela" {
		_ = db.DeleteWASession(ctx, h.Accounts, waControl, m.From)
		h.waSay(ctx, m.From, wfCancelled)
		return true
	}
	if m.ImageID != "" {
		// A photo always starts a new registration.
		w.startEmployee(ctx, m.ImageID, m.Text)
		return true
	}
	if s, err := db.GetWASession(ctx, h.Accounts, waControl, m.From); err == nil && s.Kind == waWorkforceKind {
		var p wfPending
		if json.Unmarshal(s.Data, &p) == nil {
			w.continueWith(ctx, p, m)
			return true
		}
	}
	if n == "" || n == "ayuda" || n == "hola" || n == "menu" || n == "?" {
		h.waSay(ctx, m.From, wfHelp)
		return true
	}
	w.interpret(ctx, m.Text)
	return true
}

// wfConv is one supervisor's conversation.
type wfConv struct {
	h     *Handler
	cl    *odoo.Client
	u     db.WAUser
	sites []db.WorkSite
	phone string
}

func (w wfConv) save(ctx context.Context, p wfPending) {
	b, _ := json.Marshal(p)
	_ = db.SaveWASession(ctx, w.h.Accounts, db.WASession{Phone: w.phone, AccountID: w.u.AccountID, Kind: waWorkforceKind, Data: b,
		ExpiresAt: time.Now().Add(2 * time.Hour)})
}

func (w wfConv) site(id string) (db.WorkSite, bool) {
	for _, s := range w.sites {
		if s.ID == id {
			return s, true
		}
	}
	return db.WorkSite{}, false
}

func (w wfConv) reporter() string {
	name := strings.TrimSpace(w.u.DisplayName)
	if name == "" {
		name = w.u.Email
	}
	return name + " (WhatsApp)"
}

// location makes sure the site is in the ERP and returns its location.
func (w wfConv) location(ctx context.Context, s db.WorkSite) int {
	if s.OdooLocationID == 0 {
		_ = w.h.syncSite(ctx, w.cl, &s)
	}
	return s.OdooLocationID
}

var codeToken = regexp.MustCompile(`(?i)^(?:c[oó]d(?:igo)?\.?:?)?([a-z]{0,4}-?\d[\w-]*)$`)

// parseCaption reads "Juan Pérez 123" / "Código 45, María López": a token
// with digits is the code, the words are the name. Nothing is guessed.
func parseCaption(text string) (name, code string) {
	clean := strings.NewReplacer(",", " ", ";", " ", "\n", " ", "#", " ").Replace(text)
	words := []string{}
	for _, f := range strings.Fields(clean) {
		lf := strings.ToLower(strings.Trim(f, ".:"))
		if lf == "codigo" || lf == "código" || lf == "cod" || lf == "nombre" || lf == "alta" || lf == "de" && len(words) == 0 {
			continue
		}
		if m := codeToken.FindStringSubmatch(strings.Trim(f, ".:")); m != nil && code == "" {
			code = strings.ToUpper(m[1])
			continue
		}
		words = append(words, f)
	}
	return strings.Join(words, " "), code
}

func (w wfConv) startEmployee(ctx context.Context, mediaID, caption string) {
	name, code := parseCaption(caption)
	p := wfPending{Kind: "employee", MediaID: mediaID, Name: name, Code: code}
	if len(w.sites) == 1 {
		p.SiteID = w.sites[0].ID
	}
	w.next(ctx, p)
}

// next asks for what's missing, or for the confirmation.
func (w wfConv) next(ctx context.Context, p wfPending) {
	h := w.h
	if p.SiteID == "" {
		if p.SiteHint != "" {
			hint := live.Normalize(p.SiteHint)
			for _, s := range w.sites {
				if n := live.Normalize(s.Name); n == hint || strings.Contains(n, hint) {
					p.SiteID = s.ID
					break
				}
			}
		}
		if p.SiteID == "" {
			p.Step = "site"
			p.Options = nil
			for _, s := range w.sites {
				p.Options = append(p.Options, wfSiteOption{ID: s.ID, Name: s.Name})
			}
			w.save(ctx, p)
			q := "¿A qué obra va?"
			if len(p.Options) <= 3 {
				bs := []whatsapp.Button{}
				for _, o := range p.Options {
					bs = append(bs, whatsapp.Button{ID: wfSiteBtn + o.ID, Title: o.Name})
				}
				h.waAsk(ctx, w.phone, q, bs)
				return
			}
			lines := []string{q + " Responde con el número:"}
			for i, o := range p.Options {
				lines = append(lines, fmt.Sprintf("%d. %s", i+1, o.Name))
			}
			h.waSay(ctx, w.phone, strings.Join(lines, "\n"))
			return
		}
	}
	site, _ := w.site(p.SiteID)
	loc := w.location(ctx, site)
	switch p.Kind {
	case "employee":
		mode := idMode(ctx, w.cl)
		if (mode == "name" || mode == "both") && p.Name == "" {
			p.Step = "name"
			w.save(ctx, p)
			h.waSay(ctx, w.phone, "¿Cómo se llama el trabajador?")
			return
		}
		if (mode == "code" || mode == "both") && p.Code == "" {
			p.Step = "code"
			w.save(ctx, p)
			h.waSay(ctx, w.phone, "¿Cuál es su código interno?")
			return
		}
		if !p.Matched {
			var found []wfEmployee
			_ = w.cl.Exec(ctx, employeeModel, "andrho_find", []any{[]int{loc}, p.Name, p.Code}, nil, &found)
			switch {
			case len(found) == 1 && (p.Code != "" && strings.EqualFold(found[0].Code, p.Code) || live.Normalize(found[0].Name) == live.Normalize(p.Name)):
				p.EmployeeID, p.Matched = found[0].ID, true
			case len(found) >= 1:
				p.Step, p.Candidates = "pick", found
				w.save(ctx, p)
				lines := []string{"Encontré trabajadores parecidos. ¿Es alguno de ellos? Responde con el número, o 0 si es nuevo:"}
				for i, e := range found {
					lines = append(lines, fmt.Sprintf("%d. %s%s · %s", i+1, e.Name, codeSuffix(e.Code), e.Site))
				}
				h.waSay(ctx, w.phone, strings.Join(lines, "\n"))
				return
			default:
				p.Matched = true // new
			}
		}
		p.Step = "confirm"
		w.save(ctx, p)
		who := strings.TrimSpace(p.Name + codeSuffix(p.Code))
		verb := "Doy de alta a"
		if p.EmployeeID > 0 {
			verb = "Actualizo a"
		}
		photo := ""
		if p.MediaID != "" {
			photo = " con su foto"
		}
		h.waAsk(ctx, w.phone, fmt.Sprintf("%s %s%s en %s. ¿Lo guardo?", verb, who, photo, site.Name),
			[]whatsapp.Button{{ID: wfSave, Title: "Guardar"}, {ID: wfCancel, Title: "Cancelar"}})
	case "crew":
		if len(p.MemberIDs) == 0 && len(p.Unknown) == 0 && len(p.MemberNames) > 0 {
			for _, name := range p.MemberNames {
				var found []wfEmployee
				_ = w.cl.Exec(ctx, employeeModel, "andrho_find", []any{[]int{loc}, name, false}, nil, &found)
				if len(found) == 1 && found[0].SiteID == loc {
					p.MemberIDs = append(p.MemberIDs, found[0].ID)
				} else {
					p.Unknown = append(p.Unknown, name)
				}
			}
		}
		if len(p.MemberIDs) == 0 {
			_ = db.DeleteWASession(ctx, h.Accounts, waControl, w.phone)
			msg := "No encontré a nadie de esa cuadrilla en " + site.Name + "."
			if len(p.Unknown) > 0 {
				msg += " Revisa los nombres (" + strings.Join(p.Unknown, ", ") + ") o dalos de alta primero con su foto."
			}
			h.waSay(ctx, w.phone, msg)
			return
		}
		p.Step = "confirm"
		w.save(ctx, p)
		var names []map[string]any
		_ = w.cl.SearchRead(ctx, employeeModel, []any{[]any{"id", "in", p.MemberIDs}}, []string{"name"}, "name", 0, &names)
		list := []string{}
		for _, n := range names {
			list = append(list, odoo.Str(n["name"]))
		}
		msg := fmt.Sprintf("Cuadrilla %s en %s: %s.", p.CrewName, site.Name, strings.Join(list, ", "))
		if p.Zone != "" {
			msg += " Dónde: " + p.Zone + "."
		}
		if p.Activity != "" {
			msg += " Actividad: " + p.Activity + "."
		}
		if len(p.Unknown) > 0 {
			msg += " No encontré a: " + strings.Join(p.Unknown, ", ") + " (no los incluyo)."
		}
		h.waAsk(ctx, w.phone, msg+" ¿Lo guardo?", []whatsapp.Button{{ID: wfSave, Title: "Guardar"}, {ID: wfCancel, Title: "Cancelar"}})
	case "terminate":
		if !p.Matched {
			var found []wfEmployee
			_ = w.cl.Exec(ctx, employeeModel, "andrho_find", []any{[]int{loc}, p.Name, p.Code}, nil, &found)
			mine := []wfEmployee{}
			for _, e := range found {
				if w.ownsLocation(e.SiteID) {
					mine = append(mine, e)
				}
			}
			switch len(mine) {
			case 0:
				_ = db.DeleteWASession(ctx, h.Accounts, waControl, w.phone)
				h.waSay(ctx, w.phone, "No encontré a "+strings.TrimSpace(p.Name+codeSuffix(p.Code))+" en tus obras.")
				return
			case 1:
				p.EmployeeID, p.Matched, p.Name = mine[0].ID, true, mine[0].Name
			default:
				p.Step, p.Candidates = "pick", mine
				w.save(ctx, p)
				lines := []string{"¿A quién doy de baja? Responde con el número:"}
				for i, e := range mine {
					lines = append(lines, fmt.Sprintf("%d. %s%s · %s", i+1, e.Name, codeSuffix(e.Code), e.Site))
				}
				h.waSay(ctx, w.phone, strings.Join(lines, "\n"))
				return
			}
		}
		p.Step = "confirm"
		w.save(ctx, p)
		h.waAsk(ctx, w.phone, "Doy de baja a "+p.Name+" y borro su foto. ¿Seguro?",
			[]whatsapp.Button{{ID: wfSave, Title: "Dar de baja"}, {ID: wfCancel, Title: "Cancelar"}})
	}
}

func (w wfConv) ownsLocation(loc int) bool {
	for _, s := range w.sites {
		if s.OdooLocationID == loc && loc > 0 {
			return true
		}
	}
	return false
}

func codeSuffix(code string) string {
	if code == "" {
		return ""
	}
	return " (código " + code + ")"
}

// continueWith takes the answer to what AndRho asked.
func (w wfConv) continueWith(ctx context.Context, p wfPending, m whatsapp.Message) {
	h := w.h
	text := strings.TrimSpace(m.Text)
	switch p.Step {
	case "site":
		id := strings.TrimPrefix(m.ButtonID, wfSiteBtn)
		if m.ButtonID == "" {
			if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(p.Options) {
				id = p.Options[n-1].ID
			} else {
				p.SiteHint = text
				id = ""
			}
		}
		if _, ok := w.site(id); ok {
			p.SiteID = id
		}
		w.next(ctx, p)
	case "name":
		if text == "" {
			h.waSay(ctx, w.phone, "¿Cómo se llama el trabajador?")
			return
		}
		p.Name = text
		w.next(ctx, p)
	case "code":
		if text == "" {
			h.waSay(ctx, w.phone, "¿Cuál es su código interno?")
			return
		}
		p.Code = strings.ToUpper(text)
		w.next(ctx, p)
	case "pick":
		n, err := strconv.Atoi(text)
		if strings.HasPrefix(m.ButtonID, wfPick) {
			n, err = strconv.Atoi(strings.TrimPrefix(m.ButtonID, wfPick))
		}
		if err != nil || n < 0 || n > len(p.Candidates) || (n == 0 && p.Kind == "terminate") {
			h.waSay(ctx, w.phone, "Responde con uno de los números de la lista.")
			return
		}
		if n > 0 {
			p.EmployeeID = p.Candidates[n-1].ID
			if p.Kind == "terminate" {
				p.Name = p.Candidates[n-1].Name
			}
		}
		p.Matched, p.Candidates = true, nil
		w.next(ctx, p)
	case "confirm":
		yes := m.ButtonID == wfSave
		if m.ButtonID == "" {
			n := live.Normalize(text)
			yes = n == "si" || n == "guardar" || n == "guardalo" || n == "ok" || n == "dale"
			if !yes && n != "no" {
				h.waSay(ctx, w.phone, "¿Lo guardo? Responde «sí» o «cancelar».")
				return
			}
		}
		_ = db.DeleteWASession(ctx, h.Accounts, waControl, w.phone)
		if !yes {
			h.waSay(ctx, w.phone, wfCancelled)
			return
		}
		w.execute(ctx, p)
	default:
		_ = db.DeleteWASession(ctx, h.Accounts, waControl, w.phone)
		h.waSay(ctx, w.phone, wfHelp)
	}
}

// execute saves what the supervisor confirmed.
func (w wfConv) execute(ctx context.Context, p wfPending) {
	h := w.h
	site, ok := w.site(p.SiteID)
	if !ok {
		h.waSay(ctx, w.phone, wfNoSites)
		return
	}
	loc := w.location(ctx, site)
	switch p.Kind {
	case "employee":
		vals := map[string]any{"name": p.Name, "code": p.Code, "reported_by": w.reporter()}
		if p.EmployeeID > 0 {
			vals["employee_id"] = p.EmployeeID
		}
		if p.MediaID != "" {
			// Straight from Meta to the ERP, only in memory.
			img, _, err := h.WA.Media(ctx, p.MediaID)
			if err != nil {
				log.Printf("workforce: media for %s: %v", w.u.AccountID, err)
				h.waSay(ctx, w.phone, "No pude descargar la foto. Mándala otra vez, por favor.")
				return
			}
			vals["image"] = base64.StdEncoding.EncodeToString(img)
		}
		var res struct {
			ID      int  `json:"id"`
			Created bool `json:"created"`
		}
		if err := w.cl.Exec(ctx, employeeModel, "andrho_upsert", []any{loc, vals}, nil, &res); err != nil {
			log.Printf("workforce: upsert in %s: %v", w.u.AccountID, err)
			h.waSay(ctx, w.phone, "No pude guardarlo en el ERP. Intenta de nuevo en un momento.")
			return
		}
		verb := "quedó dado de alta"
		if !res.Created {
			verb = "quedó actualizado"
		}
		_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), w.u.AccountID, &w.u.ID, "worker_reported", "Trabajador reportado por WhatsApp en "+site.Name)
		h.waSay(ctx, w.phone, fmt.Sprintf("Listo: %s %s en %s.", strings.TrimSpace(p.Name+codeSuffix(p.Code)), verb, site.Name))
	case "crew":
		var id int
		if err := w.cl.Exec(ctx, crewModel, "andrho_report", []any{loc, map[string]any{"name": p.CrewName, "member_ids": p.MemberIDs,
			"zone": p.Zone, "activity": p.Activity, "reported_by": w.reporter()}}, nil, &id); err != nil {
			log.Printf("workforce: crew in %s: %v", w.u.AccountID, err)
			h.waSay(ctx, w.phone, "No pude guardarlo en el ERP. Intenta de nuevo en un momento.")
			return
		}
		_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), w.u.AccountID, &w.u.ID, "crew_reported", "Cuadrilla reportada por WhatsApp en "+site.Name)
		h.waSay(ctx, w.phone, "Listo: cuadrilla "+p.CrewName+" guardada en "+site.Name+".")
	case "terminate":
		if err := w.cl.Exec(ctx, employeeModel, "andrho_terminate", []any{[]int{p.EmployeeID}}, nil, nil); err != nil {
			h.waSay(ctx, w.phone, "No pude darlo de baja ahora. Intenta de nuevo en un momento.")
			return
		}
		_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), w.u.AccountID, &w.u.ID, "worker_terminated", "Baja de un trabajador por WhatsApp (foto borrada)")
		h.waSay(ctx, w.phone, "Listo: "+p.Name+" quedó de baja y su foto se borró.")
	}
}

// --- reading free text ----------------------------------------------------------------

// wfIntent is what Claude (or the simple parser) reads from a message.
type wfIntent struct {
	Intent     string   `json:"intent"` // crew | terminate | other
	CrewName   string   `json:"crew_name"`
	Members    []string `json:"members"`
	Zone       string   `json:"zone"`
	Activity   string   `json:"activity"`
	Site       string   `json:"site"`
	WorkerName string   `json:"worker_name"`
	WorkerCode string   `json:"worker_code"`
}

const wfSystem = `Eres AndRho y lees un mensaje de WhatsApp de un supervisor de obra de construcción en Guatemala.
Conviértelo en datos. Reglas estrictas:
- intent: "crew" si reporta una cuadrilla (quiénes la forman y/o dónde trabajan), "terminate" si pide dar de baja a un trabajador, "other" para todo lo demás.
- Copia los datos tal como aparecen en el mensaje. No inventes, no completes, no corrijas nombres. Si algo no aparece, déjalo vacío ("" o []).
- crew_name: el nombre de la cuadrilla si lo dice; si no lo dice, "".
- members: cada nombre o código de trabajador mencionado como integrante, uno por elemento.
- zone: dónde trabajan dentro de la obra (nivel, torre, área), tal como lo dice.
- activity: qué están haciendo, tal como lo dice.
- site: la obra, solo si la nombra.
- worker_name / worker_code: para "terminate", a quién dar de baja.
Campos: intent, crew_name, members, zone, activity, site, worker_name, worker_code.`

// grounded drops what isn't in the message: Claude may only copy.
func grounded(msg string, in wfIntent) wfIntent {
	n := live.Normalize(msg)
	keep := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" || !strings.Contains(n, live.Normalize(s)) {
			return ""
		}
		return s
	}
	out := wfIntent{Intent: in.Intent, CrewName: keep(in.CrewName), Zone: keep(in.Zone), Activity: keep(in.Activity), Site: keep(in.Site),
		WorkerName: keep(in.WorkerName), WorkerCode: keep(in.WorkerCode)}
	for _, m := range in.Members {
		if k := keep(m); k != "" {
			out.Members = append(out.Members, k)
		}
	}
	return out
}

var (
	crewPattern = regexp.MustCompile(`(?is)^\s*cuadrilla\s+([^:]+?)\s*:\s*(.+)$`)
	inPattern   = regexp.MustCompile(`(?i)\s*,?\s*\ben\s+(?:el|la|los|las)?\s*(.+)$`)
	termPattern = regexp.MustCompile(`(?i)^\s*(?:dar\s+de\s+)?baja\s+(?:a|de)?\s*(.+?)\s*$`)
)

// parseSimple reads the documented formats without Claude.
func parseSimple(msg string) wfIntent {
	if m := termPattern.FindStringSubmatch(msg); m != nil {
		name, code := parseCaption(m[1])
		return wfIntent{Intent: "terminate", WorkerName: name, WorkerCode: code}
	}
	m := crewPattern.FindStringSubmatch(msg)
	if m == nil {
		return wfIntent{Intent: "other"}
	}
	out := wfIntent{Intent: "crew", CrewName: strings.TrimSpace(m[1])}
	rest := m[2]
	if loc := inPattern.FindStringSubmatchIndex(rest); loc != nil {
		where := strings.TrimSpace(rest[loc[2]:loc[3]])
		rest = rest[:loc[0]]
		if i := strings.Index(where, ","); i >= 0 {
			out.Activity = strings.TrimSpace(where[i+1:])
			where = strings.TrimSpace(where[:i])
		}
		out.Zone = where
	}
	for _, part := range regexp.MustCompile(`\s*(?:,|\by\b)\s*`).Split(rest, -1) {
		if p := strings.TrimSpace(part); p != "" {
			out.Members = append(out.Members, p)
		}
	}
	return out
}

// interpret reads a free-text message and starts what it asks for.
func (w wfConv) interpret(ctx context.Context, text string) {
	h := w.h
	in := parseSimple(text)
	if in.Intent == "other" && h.AI.Enabled() {
		var got wfIntent
		if err := h.AI.JSON(ctx, "whatsapp", w.u.AccountID, ai.WithContext(wfSystem), text, 500, &got); err == nil {
			in = grounded(text, got)
		} else {
			log.Printf("workforce: interpret: %v", err)
		}
	}
	p := wfPending{SiteHint: in.Site}
	if len(w.sites) == 1 {
		p.SiteID = w.sites[0].ID
	}
	switch in.Intent {
	case "crew":
		if len(in.Members) == 0 {
			h.waSay(ctx, w.phone, "¿Quiénes forman la cuadrilla? Escríbelo así: «Cuadrilla Losa Norte: Juan Pérez, Pedro Gómez, en el nivel 3».")
			return
		}
		p.Kind, p.CrewName, p.MemberNames, p.Zone, p.Activity = "crew", in.CrewName, in.Members, in.Zone, in.Activity
		if p.CrewName == "" {
			p.CrewName = "Cuadrilla de " + in.Members[0]
		}
	case "terminate":
		if in.WorkerName == "" && in.WorkerCode == "" {
			h.waSay(ctx, w.phone, "¿A quién doy de baja? Escribe su nombre o código.")
			return
		}
		p.Kind, p.Name, p.Code = "terminate", in.WorkerName, in.WorkerCode
	default:
		h.waSay(ctx, w.phone, wfHelp)
		return
	}
	w.next(ctx, p)
}
