package analyzer

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// Suggest is the signup's suggestion, made once every question is answered
// (survey or description, plus the brand): the rules rank the modules
// (ruleNeeds) and pick the plan that fits (internal/recommend); then Claude,
// with the project context, the catalog and everything the business said,
// ranks them again and explains each one. The two rankings are blended
// (blend), so neither decides alone: the rules keep it predictable, Claude
// reads what the rules can't (the business' own words). Without Claude, or
// if its answer fails, the rules' result goes out as is.

// SuggestInput is everything the signup knows before the suggestion.
type SuggestInput struct {
	Answers     recommend.Answers
	Details     Details
	Description string
	Brand       struct {
		Choice  string `json:"choice"` // tengo | ayuda | omitir
		Essence string `json:"essence"`
	}
}

const suggestTask = `Eres el analista de AndRho en el registro. Un negocio ya contestó todo lo que preguntamos y te pasamos:
- "negocio": sus respuestas, su descripción con sus palabras (si la escribió), su marca y detalles.
- "catalogo": los módulos de AndRho (key, nombre, descripción, área) y los planes.
- "reglas": el orden de módulos que propone nuestro algoritmo de reglas y el plan que mejor le queda según ese algoritmo.

Decide qué módulos necesita este negocio, del más al menos importante para su operación, usando SOLO keys del catálogo (no incluyas "creators", ya viene en el plan Galáctico).
Puedes quitar módulos de las reglas que no le sirvan y agregar los que las reglas no vieron, pero cada uno debe estar justificado por algo que el negocio dijo.
Mantén las áreas que el negocio pidió explícitamente.
Para cada módulo escribe "reason": una frase corta, de tú, que diga por qué le sirve a ESTE negocio, mencionando lo que contó (sin inventar datos).
Elige también el plan que mejor le queda (despegue, en_orbita o galactico) y por qué en una frase.

Devuelve {"summary": "una o dos frases, de tú, con lo que entendiste del negocio y lo que más necesita", "modules": [{"key": "...", "reason": "..."}], "plan": "...", "plan_reason": "..."}.`

type suggestOut struct {
	Summary string `json:"summary"`
	Modules []struct {
		Key    string `json:"key"`
		Reason string `json:"reason"`
	} `json:"modules"`
	Plan       string `json:"plan"`
	PlanReason string `json:"plan_reason"`
}

// Suggest builds the suggestion for in. client may be nil or disabled.
func Suggest(ctx context.Context, client *ai.Client, in SuggestInput) Result {
	a := Sanitize(in.Answers)
	rec := recommend.Recommend(a)
	rules := ruleNeeds(a)
	bestFit := planByRecommendation[rec.Plan]
	base := build(rules, bestFit, rec.Reasons)
	base.Source = "reglas"
	if !client.Enabled() {
		return base
	}

	user, _ := json.Marshal(map[string]any{
		"negocio": map[string]any{
			"respuestas":  a,
			"detalles":    cleanDetails(in.Details),
			"descripcion": clip(in.Description, MaxDescription),
			"marca":       map[string]string{"opcion": in.Brand.Choice, "esencia": clip(in.Brand.Essence, maxEssence)},
		},
		"catalogo": map[string]any{"modulos": catalogForPrompt(), "planes": Plans},
		"reglas":   map[string]any{"modulos": keysOf(rules), "plan": bestFit},
	})
	var out suggestOut
	if err := client.JSON(ctx, "analyzer", "", ai.WithContext(suggestTask), string(user), 1500, &out); err != nil {
		return base
	}

	var claude []string
	reasons := map[string]string{}
	for _, m := range out.Modules {
		if _, ok := Modules[m.Key]; !ok || m.Key == "creators" || reasons[m.Key] != "" {
			continue
		}
		claude = append(claude, m.Key)
		reasons[m.Key] = clip(m.Reason, 200)
	}
	if len(claude) == 0 {
		return base
	}

	// Always kept: the analysis (AndRho itself) and what the business
	// explicitly asked for.
	pinned := map[string]bool{"analitica": true}
	for _, m := range a.Modules {
		if m == "sitio_web" && a.HasWebsite == "si" {
			m = "tracking" // same swap as recommend.SuggestModules
		}
		pinned[m] = true
	}

	var needs []Module
	for _, k := range blend(keysOf(rules), claude, pinned) {
		needs = append(needs, Modules[k])
	}
	res := build(needs, blendPlan(bestFit, out.Plan), rec.Reasons)
	if r := clip(out.PlanReason, 300); r != "" {
		res.Reasons = []string{r}
	}
	res.Summary = clip(out.Summary, 400)
	res.ModuleReasons = map[string]string{}
	for _, m := range needs {
		if r := reasons[m.Key]; r != "" {
			res.ModuleReasons[m.Key] = r
		}
	}
	res.Source = "ia"
	return res
}

// blend mixes two rankings, Borda style: in a list of n, the i-th module
// gets 1 - i/n points, and a module's score is the average of both lists (0
// where it's missing); the order goes by score, ties keep the rules' order,
// then Claude's. Every module Claude lists stays (it had to justify each
// one with what the business said); one only the rules want stays if it's
// in the rules' top half, or pinned.
func blend(rules, claude []string, pinned map[string]bool) []string {
	points := func(list []string) map[string]float64 {
		p := map[string]float64{}
		for i, k := range list {
			p[k] = 1 - float64(i)/float64(len(list))
		}
		return p
	}
	pr, pc := points(rules), points(claude)
	order := append(append([]string{}, rules...), claude...)
	pos := map[string]int{}
	var keys []string
	for i, k := range order {
		if _, seen := pos[k]; !seen {
			pos[k] = i
			keys = append(keys, k)
		}
	}
	score := func(k string) float64 { return (pr[k] + pc[k]) / 2 }
	var out []string
	for _, k := range keys {
		if _, byClaude := pc[k]; byClaude || pr[k] > 0.5 || pinned[k] {
			out = append(out, k)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if si, sj := score(out[i]), score(out[j]); si != sj {
			return si > sj
		}
		return pos[out[i]] < pos[out[j]]
	})
	return out
}

// blendPlan meets the rules' plan and Claude's halfway, rounding up to the
// broader one. An unknown plan from Claude leaves the rules' plan.
func blendPlan(rules, claude string) string {
	rank := map[string]int{}
	for i, p := range Plans {
		rank[p.Key] = i
	}
	r, okR := rank[rules]
	c, okC := rank[claude]
	switch {
	case !okC:
		return rules
	case !okR:
		return claude
	}
	return Plans[(r+c+1)/2].Key
}

func keysOf(ms []Module) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Key
	}
	return out
}

// catalogForPrompt is the module catalog without Creators, in a stable
// order (the map has none).
func catalogForPrompt() []Module {
	var out []Module
	for k, m := range Modules {
		if k != "creators" {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].Key, out[j].Key) < 0 })
	return out
}
