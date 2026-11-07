package handlers

// What happens when a business accepts a suggestion: AndRho does it itself
// when it can (installing an app in the account's ERP), and otherwise it
// becomes a Creator task assigned by internal/matching. Either way the
// owner only accepts; the suggestion moves to "terminada" on its own when
// the work is done (the install finishes, the Creator delivers).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/insights"
	"github.com/IngeniosoHacker/andrho-api/internal/matching"
)

// How long a Creator has for each kind of work.
var taskDue = map[string]time.Duration{
	"web": 7 * 24 * time.Hour, "contenido": 5 * 24 * time.Hour, "social": 5 * 24 * time.Hour,
	"erp": 3 * 24 * time.Hour, "diseno": 7 * 24 * time.Hour, "general": 5 * 24 * time.Hour,
}

// Apps AndRho may install on its own; anything else goes to a person.
var installable = map[string]bool{"point_of_sale": true, "crm": true, "sale_management": true, "purchase": true,
	"account": true, "contacts": true, "product_expiry": true, "stock_delivery": true, "calendar": true, "mrp": true, "hr": true}

func suggestionAction(s db.Suggestion) insights.Action {
	var a insights.Action
	if len(s.Action) > 0 {
		_ = json.Unmarshal(s.Action, &a)
	}
	if a.Type == "" {
		a.Type = "creator"
	}
	if a.Type == "creator" && a.Skill == "" {
		a.Skill = sectionSkillOf(s.Section)
	}
	return a
}

func sectionSkillOf(section string) string {
	switch section {
	case "web":
		return "web"
	case "social":
		return "social"
	case "inventario", "clientes", "prospectos", "sucursales":
		return "erp"
	}
	return "general"
}

// acceptSuggestion starts the work for s and returns what the panel shows.
func (h *Handler) acceptSuggestion(ctx context.Context, accountID string, s db.Suggestion) (*db.SuggestionAssignment, error) {
	act := suggestionAction(s)
	if act.Type == "erp_install" {
		mods := []string{}
		for _, m := range act.Modules {
			if installable[m] {
				mods = append(mods, m)
			}
		}
		if len(mods) > 0 {
			go h.installForSuggestion(accountID, s, mods)
			return &db.SuggestionAssignment{Kind: "andrho", Status: "en_proceso"}, nil
		}
		act = insights.Action{Type: "creator", Skill: "erp"}
	}

	acc, err := db.GetAccountByID(ctx, h.Accounts, accountID)
	if err != nil {
		return nil, err
	}
	// Creator tasks are limited per month by the plan and its add-ons
	// (todo.md B2).
	left, limit, err := h.creatorQuotaLeft(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if left <= 0 {
		return nil, fmt.Errorf("%w: %s", errCreatorQuota, creatorQuotaMessage(limit))
	}
	brief, _ := json.Marshal(map[string]any{
		"suggestion": s.Title, "body": s.Body, "evidence": s.Evidence, "section": s.Section,
		"explain": json.RawMessage(nonEmptyJSON(s.Explain)),
	})
	due := time.Now().Add(taskDue[act.Skill])
	if taskDue[act.Skill] == 0 {
		due = time.Now().Add(taskDue["general"])
	}
	sid := s.ID
	task := db.CreatorTask{ID: uuid.NewString(), AccountID: accountID, Kind: act.Skill,
		Title: s.Title + " · " + acc.CompanyName, Brief: brief, Status: "abierta", DueAt: &due, SuggestionID: &sid}
	out := &db.SuggestionAssignment{Kind: "creator", Status: "abierta"}

	res, ok, err := matching.Assign(ctx, h.Accounts, accountID, act.Skill)
	if err != nil {
		log.Printf("accept: matching: %v", err) // the task stays open for any Creator
	}
	if ok {
		task.CreatorID, task.Status = &res.UserID, "aceptada"
		task.Match, _ = json.Marshal(res)
		out.Status, out.CreatorName = "aceptada", res.Name
	}
	if _, err := db.CreateCreatorTask(ctx, h.Accounts, task); err != nil {
		return nil, err
	}
	event := "Sugerencia en la lista de los Creators: " + s.Title
	if ok {
		event = fmt.Sprintf("%s se encarga de: %s (%s)", res.Name, s.Title, strings.Join(res.Reasons, ", "))
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), accountID, nil, "suggestion_assigned", event)
	return out, nil
}

func nonEmptyJSON(j json.RawMessage) []byte {
	if len(j) == 0 {
		return []byte("null")
	}
	return j
}

// installForSuggestion installs Odoo apps in the account's ERP and closes the
// suggestion with the outcome. Runs detached from the request.
func (h *Handler) installForSuggestion(accountID string, s db.Suggestion, mods []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	fail := func(msg string, err error) {
		log.Printf("accept: install %v for %s: %v", mods, accountID, err)
		_, _ = h.Accounts.Exec(ctx, `UPDATE suggestions SET status = 'sugerida', report = $2, updated_at = now() WHERE id = $1`, s.ID, msg)
		_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), accountID, nil, "suggestion_failed", msg)
	}
	cl, err := h.odooFor(ctx, accountID)
	if err != nil {
		fail("No pudimos entrar a tu ERP para activarlo; vuelve a aceptarla en un momento.", err)
		return
	}
	done, err := cl.InstallModules(ctx, mods)
	if err != nil {
		fail("Tu ERP no nos dejó activarlo todavía; vuelve a aceptarla en un momento.", err)
		return
	}
	// The app changed which models exist: forget the cached client state.
	odooClients.Delete(accountID)
	report := "Listo: AndRho lo activó en tu ERP. Desde ahora esta sección se llena sola."
	if len(done) == 0 {
		report = "Ya estaba activo en tu ERP."
	}
	if err := db.FinishSuggestion(ctx, h.Accounts, s.ID, report); err != nil {
		log.Printf("accept: finish %s: %v", s.ID, err)
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), accountID, nil, "erp_modules_installed",
		"AndRho activó en tu ERP: "+strings.Join(mods, ", "))
}

// closeSuggestionTasks follows the owner's decision on a suggestion that
// already became work: rejecting it cancels its open Creator task.
func (h *Handler) closeSuggestionTasks(ctx context.Context, suggestionID, status string) {
	if status != "rechazada" {
		return
	}
	_, _ = h.Accounts.Exec(ctx,
		`UPDATE creator_tasks SET status = 'cancelada', updated_at = now()
		 WHERE suggestion_id = $1 AND status IN ('abierta', 'aceptada')`, suggestionID)
}
