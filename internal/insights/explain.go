package insights

import "strconv"

// What the "¿Por qué?" view shows for a suggestion, and what accepting it
// does. The statistics stay in Proposal.Evidence (for AndRho's team and the
// Creator who does the work); Explain is the same finding for the business
// owner: one sentence, one picture, one conclusion and how sure we are — in
// plain words, no p-values.

// Explain is rendered by the dashboard (insights.js, openWhy) as a chart.
type Explain struct {
	// comparacion: bars side by side | tendencia: a line over time |
	// proporcion: one share of a whole | cambio: before vs now.
	Kind     string         `json:"kind"`
	Headline string         `json:"headline"`
	Bars     []ExplainBar   `json:"bars,omitempty"`
	Series   []ExplainPoint `json:"series,omitempty"`
	// LowerIsBetter flips the chart's reading (Google positions: #1 is best).
	LowerIsBetter bool   `json:"lower_is_better,omitempty"`
	Unit          string `json:"unit,omitempty"`
	Takeaway      string `json:"takeaway"`
	// alta: proven with statistics on enough data | media: a clear pattern,
	// not formally tested | baja: a single reading worth watching.
	Confidence     string `json:"confidence"`
	ConfidenceText string `json:"confidence_text"`
}

// ExplainBar is one bar; Highlight marks the one the suggestion is about.
type ExplainBar struct {
	Label     string  `json:"label"`
	Value     float64 `json:"value"`
	Highlight bool    `json:"highlight,omitempty"`
}

// ExplainPoint is one point of a trend.
type ExplainPoint struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// Action is what accepting the suggestion sets in motion.
//
//	creator      a task for AndRho's Creators, assigned by internal/matching
//	             (Skill says which kind of work: web, social, erp, contenido)
//	erp_install  AndRho installs Odoo modules in the account's ERP itself
type Action struct {
	Type    string   `json:"type"`
	Skill   string   `json:"skill,omitempty"`
	Modules []string `json:"modules,omitempty"`
	// Label is what the accept button says ("Activar CRM", "Asignar a un Creator").
	Label string `json:"label,omitempty"`
}

func creatorAction(skill string) *Action {
	return &Action{Type: "creator", Skill: skill, Label: "Aceptar y asignar a un Creator"}
}

// sectionSkill is the kind of Creator work a section's suggestions need.
func sectionSkill(section string) string {
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

// proposeX files a proposal with its explanation and action (nil action =
// a Creator task for the section's skill, decided when it's stored).
func (a *analysis) proposeX(title, body, evidence string, ex *Explain, act *Action) {
	a.proposals = append(a.proposals, Proposal{Title: title, Body: body, Evidence: evidence, Explain: ex, Action: act})
}

// Confidence wording, shared so every explanation says it the same way.
func proven(n int, what string) (string, string) {
	return "alta", "Lo comprobamos con " + strconv.Itoa(n) + " " + what + ": la diferencia es real, no casualidad."
}

func pattern(what string) (string, string) {
	return "media", "Es un patrón claro en " + what + ", pero conviene confirmarlo con más datos."
}

func reading(what string) (string, string) {
	return "baja", "Es lo que vemos " + what + "; vale la pena vigilarlo."
}

func withConfidence(ex *Explain, level, text string) *Explain {
	ex.Confidence, ex.ConfidenceText = level, text
	return ex
}
