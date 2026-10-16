package analyzer

import (
	"strconv"

	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// Package is one of the three offers: a plan, the modules it includes for
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
}

// Analyze builds the needs and the three packages for a's answers.
func Analyze(a recommend.Answers) Result {
	rec := recommend.Recommend(a)

	// Priority order comes from recommend.SuggestModules (what the answers
	// point to first, then the areas the business asked for). Creators is
	// part of Galáctico's core, not a module to count.
	var needs []Module
	for _, key := range recommend.SuggestModules(a, recommend.PlanTodo) {
		if m, ok := Modules[key]; ok && key != "creators" {
			needs = append(needs, m)
		}
	}

	res := Result{Needs: needs, Recommended: "galactico", BestFit: planByRecommendation[rec.Plan], Reasons: rec.Reasons}
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
	case len(p.Missing) == 0 && p.Key == "galactico":
		return "Todo lo que tu negocio necesita, desde el primer día. Nada se queda en tierra."
	case len(p.Missing) == 0:
		return "Cubre lo que nos contaste. Cuando crezcas, vas a querer más espacio."
	case len(p.Missing) == 1:
		return "Te quedarías sin " + p.Missing[0].Name + "."
	default:
		return "Cubre " + strconv.Itoa(len(p.Modules)) + " de las " + strconv.Itoa(total) + " cosas que tu negocio necesita. El resto se queda en tierra."
	}
}

// PlanModuleLimit is how many modules plan holds (-1 = all, unknown = 3).
func PlanModuleLimit(plan string) int {
	for _, p := range Plans {
		if p.Key == plan {
			return p.ModuleLimit
		}
	}
	return Plans[0].ModuleLimit
}
