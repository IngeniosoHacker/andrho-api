// Package provision sets up what a new account signed up for. The client id
// is the account's site_id (the id the tracker already uses): every service
// created here is named after it, which is how the account, its dashboard
// and all its AndRho branches stay connected.
//
// Every account gets an ERP: Odoo with inventory as its base, plus the Odoo
// apps its modules need (analyzer.OdooModules). The other services are one
// optional field each in a Request. Empty fields are filled by Fill from
// whatever in the signup matches them (the modules picked, the phone given);
// a field set explicitly wins. Then each service becomes a provisioning job:
//
//	erp      (always) -> Odoo + PostgreSQL on the VPS with OdooModules
//	                     (VPS/odoo/provision-client.sh, image from andrho_odoo)
//	whatsapp = number -> the client in AndRho's WhatsApp solution (VPS/whatsapp/register-client.sh)
//	web_page = true   -> a website task for Creators, with the brand brief
//	meta     = id     -> waits for AndRho's approval as a Meta Tech Provider
package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IngeniosoHacker/andrho-api/internal/analyzer"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/secret"
	"github.com/IngeniosoHacker/andrho-api/internal/vps"
)

// Request is what to set up. Every field may be left empty.
type Request struct {
	// OdooModules is what the ERP installs. Fill sets it from the modules
	// (analyzer.OdooModules); it always starts with inventory.
	OdooModules []string `json:"-"`
	Meta        string   `json:"meta"`     // Facebook/Instagram page id, when known
	WhatsApp    string   `json:"whatsapp"` // the business' WhatsApp number
	WebPage     *bool    `json:"web_page"` // a website built by Creators
}

var phoneRe = regexp.MustCompile(`^\+?[0-9 ()-]{7,20}$`)

// Fill completes the request from the modules picked: the ERP's Odoo
// modules, the WhatsApp module takes the phone, the website module sets
// WebPage, the social modules mark Meta as wanted.
func (r *Request) Fill(modules []string, phone string) {
	r.OdooModules = analyzer.OdooModules(modules)
	has := func(provision string) bool {
		for _, k := range modules {
			if m, ok := analyzer.Modules[k]; ok && m.Provision == provision {
				return true
			}
		}
		return false
	}
	if r.WhatsApp == "" && has("whatsapp") {
		r.WhatsApp = strings.TrimSpace(phone)
	}
	if r.WebPage == nil && has("website") {
		yes := true
		r.WebPage = &yes
	}
	if r.Meta == "" {
		for _, k := range modules {
			if k == "redes" || k == "redes_creacion" {
				r.Meta = "pendiente"
			}
		}
	}
}

// Account is who the jobs are for.
type Account struct {
	ID       string
	ClientID string // site_id
	Company  string
	Plan     string
}

// Brief is the brand information Creators get with a website task.
type Brief struct {
	Company   string   `json:"company"`
	ClientID  string   `json:"client_id"`
	Essence   string   `json:"essence"`
	BrandHelp bool     `json:"brand_help"` // no brand yet, wants AndRho's proposal
	HasLogo   bool     `json:"has_logo"`
	Modules   []string `json:"modules"`
	Sectors   []string `json:"sectors"`
}

// Provisioner runs the jobs.
type Provisioner struct {
	DB      *pgxpool.Pool
	VPS     *vps.Runner
	Secrets *secret.Box
	// DBMode is how Odoo's PostgreSQL is laid out on the VPS: "shared" (one
	// server, a database per client), "dedicated" (a server per client) or
	// "mixed" (dedicated for Galáctico, shared for the rest).
	DBMode string
}

// Statuses.
const (
	StatusPending     = "pendiente"
	StatusRunning     = "en_proceso"
	StatusDone        = "listo"
	StatusWaitingVPS  = "esperando_vps"
	StatusWaitingMeta = "esperando_meta"
	StatusError       = "error"
)

// Plan creates one job per service req asks for.
func (p *Provisioner) Plan(ctx context.Context, acc Account, req Request, brief Brief) ([]db.ProvisioningJob, error) {
	type spec struct {
		kind, status, detail string
		input                any
	}
	odooModules := req.OdooModules
	if len(odooModules) == 0 {
		odooModules = analyzer.OdooModules(nil)
	}
	specs := []spec{{"erp", StatusPending, "Tu ERP con inventario y los módulos de tu negocio",
		erpInput{DBMode: p.dbModeFor(acc.Plan), OdooModules: odooModules}}}
	if req.WhatsApp != "" {
		if !phoneRe.MatchString(req.WhatsApp) {
			return nil, fmt.Errorf("provision: invalid WhatsApp number")
		}
		specs = append(specs, spec{"whatsapp", StatusPending, "Tu número en la solución de WhatsApp de AndRho", map[string]string{"phone": req.WhatsApp}})
	}
	if req.WebPage != nil && *req.WebPage {
		specs = append(specs, spec{"website", StatusPending, "Tu sitio web, hecho por Creators", brief})
	}
	if req.Meta != "" {
		specs = append(specs, spec{"meta", StatusWaitingMeta,
			"La conexión con Facebook e Instagram se activa cuando Meta apruebe a AndRho como Tech Provider.", map[string]string{"meta": req.Meta}})
	}

	jobs := []db.ProvisioningJob{}
	for _, s := range specs {
		input, _ := json.Marshal(s.input)
		j, err := db.CreateProvisioningJob(ctx, p.DB, db.ProvisioningJob{
			ID: uuid.NewString(), AccountID: acc.ID, Kind: s.kind, Status: s.status, Detail: s.detail, Input: input,
		})
		if err != nil {
			return jobs, err
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// erpInput is the erp job's input: how its PostgreSQL is laid out and the
// Odoo modules to install.
type erpInput struct {
	DBMode      string   `json:"db_mode"`
	OdooModules []string `json:"odoo_modules"`
}

var odooModuleRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// modules is the list to install, never without the base: jobs created
// before the list existed (or with a bad name in it) get inventory.
func (e erpInput) modules() []string {
	out := analyzer.OdooModules(nil)
	for _, m := range e.OdooModules {
		if odooModuleRe.MatchString(m) && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}

func (p *Provisioner) dbModeFor(plan string) string {
	switch p.DBMode {
	case "dedicated":
		return "dedicated"
	case "mixed":
		if plan == "galactico" {
			return "dedicated"
		}
	}
	return "shared"
}

// RunAll runs every runnable job of the account, one after the other, in
// the background (signup doesn't wait for Odoo to boot).
func (p *Provisioner) RunAll(acc Account, jobs []db.ProvisioningJob) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		for _, j := range jobs {
			if j.Status == StatusPending || j.Status == StatusWaitingVPS || j.Status == StatusError {
				p.Run(ctx, acc, j)
			}
		}
	}()
}

// Run executes one job and records the outcome.
func (p *Provisioner) Run(ctx context.Context, acc Account, j db.ProvisioningJob) {
	set := func(status, detail string, result any, attempted bool) {
		var raw json.RawMessage
		if result != nil {
			raw, _ = json.Marshal(result)
		}
		if err := db.UpdateProvisioningJob(ctx, p.DB, j.ID, status, detail, raw, attempted); err != nil {
			log.Printf("provision: %s %s: %v", acc.ClientID, j.Kind, err)
		}
		_ = db.LogEvent(ctx, p.DB, uuid.NewString(), acc.ID, nil, "provisioning_"+j.Kind, jobEventText(j.Kind, status))
	}
	var input map[string]any
	_ = json.Unmarshal(j.Input, &input)
	var erp erpInput
	_ = json.Unmarshal(j.Input, &erp)

	switch j.Kind {
	case "erp":
		if !p.VPS.Enabled() {
			set(StatusWaitingVPS, "Tu ERP se crea en cuanto el servidor de AndRho esté conectado.", nil, false)
			return
		}
		set(StatusRunning, "Levantando Odoo y PostgreSQL…", nil, false)
		out, err := p.VPS.Run(ctx, "odoo/provision-client.sh", map[string]string{
			"DB_MODE": erp.DBMode, "ODOO_MODULES": strings.Join(erp.modules(), ","),
		}, acc.ClientID, acc.Company)
		if err != nil {
			set(StatusError, "No pudimos crear tu ERP todavía; lo reintentamos solos.", map[string]string{"error": err.Error()}, true)
			return
		}
		var res struct{ URL, DB, Login, Password string }
		if err := json.Unmarshal([]byte(vps.LastJSONLine(out)), &res); err != nil || res.URL == "" {
			set(StatusError, "El servidor no devolvió los datos del ERP.", map[string]string{"error": "bad script output"}, true)
			return
		}
		sealed, err := p.Secrets.Seal(res.Password)
		if err == nil {
			err = db.SaveOdooConnection(ctx, p.DB, db.OdooConnection{AccountID: acc.ID, URL: res.URL, DBName: res.DB, Login: res.Login, Secret: sealed})
		}
		if err != nil {
			set(StatusError, "No pudimos guardar el acceso a tu ERP.", map[string]string{"error": err.Error()}, true)
			return
		}
		set(StatusDone, "Tu ERP está listo.", map[string]string{"url": res.URL, "db": res.DB, "login": res.Login}, true)

	case "whatsapp":
		if !p.VPS.Enabled() {
			set(StatusWaitingVPS, "Registramos tu número en cuanto el servidor de AndRho esté conectado.", nil, false)
			return
		}
		out, err := p.VPS.Run(ctx, "whatsapp/register-client.sh", nil, acc.ClientID, str(input["phone"]), acc.Plan)
		if err != nil {
			set(StatusError, "No pudimos registrar tu número todavía; lo reintentamos solos.", map[string]string{"error": err.Error()}, true)
			return
		}
		var res map[string]any
		_ = json.Unmarshal([]byte(vps.LastJSONLine(out)), &res)
		set(StatusWaitingMeta, "Tu número quedó registrado. La conexión se activa cuando Meta apruebe a AndRho como Tech Provider.", res, true)

	case "website":
		creator, err := db.FirstStaffUser(ctx, p.DB, "creator")
		if err != nil {
			set(StatusError, "No pudimos asignar tu sitio a un Creator.", map[string]string{"error": err.Error()}, true)
			return
		}
		due := time.Now().Add(14 * 24 * time.Hour)
		task := db.CreatorTask{ID: uuid.NewString(), AccountID: acc.ID, Kind: "website", Title: "Sitio web de " + acc.Company,
			Brief: j.Input, Status: "abierta", DueAt: &due}
		detail := "Tu sitio está en la lista de los Creators."
		if creator != "" {
			task.CreatorID, task.Status = &creator, "aceptada"
			detail = "Un Creator ya está trabajando en tu sitio."
		}
		if _, err := db.CreateCreatorTask(ctx, p.DB, task); err != nil {
			set(StatusError, "No pudimos crear la tarea de tu sitio.", map[string]string{"error": err.Error()}, true)
			return
		}
		set(StatusRunning, detail, map[string]string{"task_id": task.ID}, true)
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func jobEventText(kind, status string) string {
	names := map[string]string{"erp": "ERP", "whatsapp": "WhatsApp", "website": "Sitio web", "meta": "Facebook e Instagram"}
	states := map[string]string{
		StatusRunning: "en proceso", StatusDone: "listo", StatusWaitingVPS: "esperando el servidor",
		StatusWaitingMeta: "esperando a Meta", StatusError: "con error, se reintentará",
	}
	return names[kind] + ": " + states[status]
}

// RetryLoop retries jobs waiting for the VPS or that failed (up to 5 tries)
// every interval, so configuring VPS_* later provisions everyone who signed
// up before.
func (p *Provisioner) RetryLoop(ctx context.Context, interval time.Duration, accountFor func(context.Context, string) (Account, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !p.VPS.Enabled() {
			continue
		}
		jobs, err := db.JobsWaiting(ctx, p.DB, []string{StatusWaitingVPS, StatusError})
		if err != nil {
			log.Printf("provision: retry: %v", err)
			continue
		}
		for _, j := range jobs {
			if j.Attempts >= 5 {
				continue
			}
			acc, err := accountFor(ctx, j.AccountID)
			if err != nil {
				continue
			}
			p.Run(ctx, acc, j)
		}
	}
}
