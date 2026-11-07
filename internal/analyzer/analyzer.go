package analyzer

import (
	"strconv"

	"github.com/IngeniosoHacker/andrho-api/internal/plans"
	"strings"

	"github.com/IngeniosoHacker/andrho-api/internal/business"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// Package is one of the offers: a plan, the modules it includes for
// this business and the ones it leaves out.
type Package struct {
	Plan
	Modules []Module `json:"modules"`
	Missing []Module `json:"missing"`
	// OdooModules is what this package installs in the client's ERP:
	// inventory always, plus what its modules need (OdooModules).
	OdooModules []string `json:"odoo_modules"`
	Recommended bool     `json:"recommended"`
	// Pitch is the line under the package. Smaller packages speak of what
	// stays behind; the complete one, of what's gained.
	Pitch string `json:"pitch"`
}

// Result is the analysis of one business.
type Result struct {
	// Needs: every module the business needs, most important first.
	Needs    []Module  `json:"needs"`
	Packages []Package `json:"packages"`
	// Recommended is the package AndRho puts forward: always the complete
	// one, so the business sees everything it could have. BestFit is the
	// plan that strictly fits the answers (internal/recommend), and Reasons
	// say why; the signup shows it as "lo mínimo para ti".
	Recommended string   `json:"recommended"`
	BestFit     string   `json:"best_fit"`
	Reasons     []string `json:"reasons"`
	// Set by Suggest: what Claude understood of the business, why each
	// module is there (by key) and whether Claude took part ("ia") or the
	// rules worked alone ("reglas").
	Summary       string            `json:"summary,omitempty"`
	ModuleReasons map[string]string `json:"module_reasons,omitempty"`
	Source        string            `json:"source,omitempty"`
	// BusinessType is the type inferred from the answers (internal/business:
	// servicios, restaurantes, ventas, or "" for none). TypeModules are the
	// modules its profile added after the analysis' own picks.
	BusinessType string   `json:"business_type,omitempty"`
	TypeModules  []string `json:"type_modules,omitempty"`
}

// withType appends the modules businesses of this type use that the
// analysis didn't pick: in addition to the analysis, after its own ranking,
// so it never pushes out what the business itself asked for.
func withType(needs []Module, typ string) ([]Module, []string) {
	p, ok := business.Get(typ)
	if !ok {
		return needs, nil
	}
	have := map[string]bool{}
	for _, m := range needs {
		have[m.Key] = true
	}
	added := []string{}
	for _, k := range p.Modules {
		if m, ok := Modules[k]; ok && !have[k] {
			have[k] = true
			needs = append(needs, m)
			added = append(added, k)
		}
	}
	return needs, added
}

func typeReasons(res *Result, typ string, added []string) {
	p, ok := business.Get(typ)
	if !ok {
		return
	}
	res.BusinessType, res.TypeModules = typ, added
	if len(added) == 0 {
		return
	}
	if res.ModuleReasons == nil {
		res.ModuleReasons = map[string]string{}
	}
	for _, k := range added {
		if res.ModuleReasons[k] == "" {
			res.ModuleReasons[k] = "Lo usan casi todos los negocios de " + strings.ToLower(p.Name) + "."
		}
	}
}

// Analyze builds the needs and the packages for a's answers.
func Analyze(a recommend.Answers) Result {
	rec := recommend.Recommend(a)
	typ := business.Classify(a.Sectors, "")
	needs, added := withType(ruleNeeds(a), typ)
	res := build(needs, planByRecommendation[rec.Plan], rec.Reasons)
	typeReasons(&res, typ, added)
	return res
}

// ruleNeeds is the rules' ranking: recommend.SuggestModules's order (what
// the answers point to first, then the areas the business asked for).
// Creators is part of Galáctico's core, not a module to count.
func ruleNeeds(a recommend.Answers) []Module {
	var needs []Module
	for _, key := range recommend.SuggestModules(a, recommend.PlanTodo) {
		if m, ok := Modules[key]; ok && key != "creators" {
			needs = append(needs, m)
		}
	}
	return needs
}

// build fills the packages with needs, in order.
func build(needs []Module, bestFit string, reasons []string) Result {
	res := Result{Needs: needs, Recommended: Complete, BestFit: bestFit, Reasons: reasons}
	for _, p := range Plans {
		pkg := Package{Plan: p, Modules: []Module{}, Missing: []Module{}}
		for i, m := range needs {
			if p.ModuleLimit < 0 || i < p.ModuleLimit {
				pkg.Modules = append(pkg.Modules, m)
			} else {
				pkg.Missing = append(pkg.Missing, m)
			}
		}
		keys := make([]string, len(pkg.Modules))
		for i, m := range pkg.Modules {
			keys[i] = m.Key
		}
		pkg.OdooModules = OdooModules(keys)
		pkg.Recommended = p.Key == res.Recommended
		pkg.Pitch = pitch(pkg, len(needs))
		res.Packages = append(res.Packages, pkg)
	}
	return res
}

func pitch(p Package, total int) string {
	switch {
	case len(p.Missing) == 0 && p.Key == Complete:
		return "Todo lo que tu negocio necesita, desde el primer día. Nada se queda en tierra."
	case len(p.Missing) == 0:
		return "Cubre lo que nos contaste. Cuando crezcas, vas a querer más espacio."
	case len(p.Missing) == 1:
		return "Te quedarías sin " + p.Missing[0].Name + "."
	default:
		return "Cubre " + strconv.Itoa(len(p.Modules)) + " de las " + strconv.Itoa(total) + " cosas que tu negocio necesita. El resto se queda en tierra."
	}
}

// PlanModuleLimit is how many modules plan holds (-1 = all; an unknown
// plan, the smallest).
func PlanModuleLimit(plan string) int {
	if p, ok := plans.Get(plan); ok {
		return p.Caps.ModuleLimit
	}
	return Plans[0].ModuleLimit
}
