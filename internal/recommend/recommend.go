// Package recommend matches a waiting-list survey to the AndRho plan that
// fits it best, and builds the set of modules we'd suggest for that business.
//
// Matching works on profiles: the answers are turned into a handful of
// signals (does the business want growth? control? does it run on paper? is
// it big?), and each plan has a weight per signal describing who it's for.
// A plan's score is the sum of signal × weight; the highest score wins. The
// signals that pushed the winner the most become the human-readable reasons
// shown next to the "Recomendado" badge.
//
// Plans (the survey's last question, andrho's lib/waitlist.js PLANS):
//
//	basico  — a dashboard with the business' data
//	crecer  — sales, marketing and social connected and analysed
//	todo    — every area in one account, Creators included
package recommend

import (
	"math"
	"sort"
)

// Answers is the part of the waiting-list survey the matching reads. JSON
// keys match the survey's POST body, so the handlers bind it directly.
type Answers struct {
	Sectors        []string          `json:"sectors"`
	CompanySize    string            `json:"companySize"`
	SalesMethods   []string          `json:"salesMethods"`
	SectorAnswers  map[string]string `json:"sectorAnswers"`
	RestaurantExp  string            `json:"restaurantExpiry"`
	UnsoldHandling string            `json:"unsoldHandling"`
	HasWebsite     string            `json:"hasWebsite"`
	SocialNetworks []string          `json:"socialNetworks"`
	MessagingApps  []string          `json:"messagingApps"`
	Management     string            `json:"management"`
	Satisfaction   int               `json:"satisfaction"`
	PainPoints     []string          `json:"painPoints"`
	Modules        []string          `json:"modules"`
	Expectations   []string          `json:"expectations"`
}

// Result is what the survey shows (plan + reasons) and what gets stored with
// the submission (plan + modules).
type Result struct {
	Plan    string             `json:"plan"`
	Scores  map[string]float64 `json:"scores"`
	Reasons []string           `json:"reasons"`
	Modules []string           `json:"suggestedModules"`
}

const (
	PlanBasico = "basico"
	PlanCrecer = "crecer"
	PlanTodo   = "todo"
)

// Plans in tie-break order: on equal scores the broader plan wins, since a
// business answering this survey is already looking past the basics.
var plans = []string{PlanTodo, PlanCrecer, PlanBasico}

// signal is one trait read from the answers. `value` is usually 0..1 (a few
// count-based ones go a bit above); `reason` is shown to the person when
// this signal is one of the main ones behind their recommendation.
type signal struct {
	name   string
	value  float64
	reason string
}

// weights[plan][signal] — who each plan is for. Signals a plan doesn't list
// weigh 0 for it; negative weights push away from it.
var weights = map[string]map[string]float64{
	PlanBasico: {
		"base":       0.6,
		"small":      1.0,
		"satisfied":  1.4,
		"narrow":     1.6,
		"breadth":    -1.5,
		"scattered":  -0.6,
		"noSystem":   -0.4,
		"bigCompany": -1.0,
	},
	PlanCrecer: {
		"base":         0.8,
		"growthGoals":  1.4,
		"growthAreas":  1.1,
		"noPresence":   1.2,
		"onlineSales":  0.7,
		"messaging":    0.4,
		"opsAreas":     -0.3,
		"satisfied":    0.3,
		"multiChannel": 0.5,
	},
	PlanTodo: {
		"base":        0.5,
		"opsAreas":    1.2,
		"orderGoals":  1.2,
		"noSystem":    1.1,
		"scattered":   1.2,
		"control":     1.0,
		"bigCompany":  1.0,
		"multiSector": 0.7,
		"breadth":     1.6,
		"unhappy":     0.6,
	},
}

func has(list []string, values ...string) bool {
	for _, l := range list {
		for _, v := range values {
			if l == v {
				return true
			}
		}
	}
	return false
}

func countIn(list []string, set ...string) float64 {
	n := 0.0
	for _, l := range list {
		if has(set, l) {
			n++
		}
	}
	return n
}

// goodSectorAnswers are the sector-question options that mean "this is
// already under control" (the `tone: 'good'` ones in the survey).
var goodSectorAnswers = map[string]bool{"sistema": true, "crm": true, "gps": true}

// signals reads the traits the plan weights are written against.
func signals(a Answers) []signal {
	opsAreas := countIn(a.Modules, "finanzas", "inventario", "compras", "produccion", "logistica", "rrhh")
	growthAreas := countIn(a.Modules, "marketing", "ventas", "clientes", "sitio_web")
	growthGoals := countIn(a.Expectations, "mas_clientes", "vender_mas", "mas_vistas", "mas_seguidores")
	orderGoals := countIn(a.Expectations, "organizarse", "automatizar", "digitalizarse", "decidir_mejor", "reducir_perdidas")

	weakSector := 0.0
	answers := []string{a.RestaurantExp}
	for _, v := range a.SectorAnswers {
		answers = append(answers, v)
	}
	for _, v := range answers {
		if v != "" && !goodSectorAnswers[v] && !hasComma(v) {
			weakSector = 1
		}
	}
	control := math.Max(weakSector, b(has(a.PainPoints, "pierdo_control") || has([]string{a.UnsoldHandling}, "se_pierde", "bodega")))

	usesSoftware := has([]string{a.Management}, "software", "erp", "varias")
	noSystem := 0.0
	switch a.Management {
	case "papel", "nada":
		noSystem = 1
	case "excel":
		noSystem = 0.7
	}

	big := 0.0
	switch a.CompanySize {
	case "11-50":
		big = 0.6
	case "51-200", "200+":
		big = 1
	}

	return []signal{
		{"base", 1, ""},
		{"small", b(has([]string{a.CompanySize}, "solo", "1-10")), "Para un equipo de tu tamaño, lo esencial rinde de inmediato."},
		{"bigCompany", big, "A la escala de tu empresa, cada área necesita estar conectada."},
		{"satisfied", b(usesSoftware && a.Satisfaction >= 4), "Ya tienes un sistema que te funciona: lo complementamos sin reemplazarlo."},
		{"unhappy", b(a.Satisfaction > 0 && a.Satisfaction <= 2), "Lo que usas hoy no te convence: conviene resolverlo de raíz."},
		{"narrow", b(len(a.Modules) <= 2), "Te interesan pocas áreas por ahora."},
		{"breadth", float64(len(a.Modules)) / 6, "Quieres cubrir muchas áreas de tu negocio a la vez."},
		{"opsAreas", opsAreas / 3, "Buscas orden en finanzas, inventario y operación."},
		{"growthAreas", growthAreas / 2, "Tus prioridades son ventas, marketing y clientes."},
		{"growthGoals", growthGoals / 2, "Tu meta es atraer más clientes y vender más."},
		{"orderGoals", orderGoals / 2, "Tu meta es organizarte, automatizar y decidir con datos."},
		{"noSystem", noSystem, "Hoy administras sin un sistema: AndRho puede ser tu ERP completo."},
		{"scattered", b(a.Management == "varias" || has(a.PainPoints, "datos_dispersos")), "Tu información está regada: unificarla es el primer paso."},
		{"control", control, "Hay pérdidas de control (inventario, vencimientos o mermas) que conviene cerrar."},
		{"multiSector", b(len(a.Sectors) >= 2), "Tu negocio combina varios giros."},
		{"noPresence", b(a.HasWebsite == "no" || has(a.SocialNetworks, "ninguna")), "Todavía no tienes presencia en línea completa: la construimos."},
		{"onlineSales", b(has(a.SalesMethods, "en_linea", "redes")), "Ya vendes en línea y en redes."},
		{"multiChannel", b(len(a.SalesMethods) >= 3), "Vendes por varios canales."},
		{"messaging", b(has(a.MessagingApps, "whatsapp", "whatsapp_business", "instagram_dm", "messenger")), "Atiendes a tus clientes por mensajería."},
	}
}

func hasComma(s string) bool {
	for _, r := range s {
		if r == ',' {
			return true
		}
	}
	return false
}

func b(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// Recommend scores every plan for these answers and returns the best one,
// the reasons behind it and the suggested modules.
func Recommend(a Answers) Result {
	sigs := signals(a)
	scores := map[string]float64{}
	for _, p := range plans {
		total := 0.0
		for _, s := range sigs {
			total += s.value * weights[p][s.name]
		}
		scores[p] = math.Round(total*100) / 100
	}

	best := plans[0]
	for _, p := range plans[1:] {
		if scores[p] > scores[best] {
			best = p
		}
	}

	// Reasons: the signals that added the most to the winner.
	type contrib struct {
		reason string
		amount float64
	}
	var cs []contrib
	for _, s := range sigs {
		amount := s.value * weights[best][s.name]
		if s.reason != "" && amount > 0.3 {
			cs = append(cs, contrib{s.reason, amount})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].amount > cs[j].amount })
	reasons := []string{}
	for i := 0; i < len(cs) && i < 2; i++ {
		reasons = append(reasons, cs[i].reason)
	}

	return Result{Plan: best, Scores: scores, Reasons: reasons, Modules: SuggestModules(a, best)}
}

// SuggestModules is the AndRho setup we'd build for these answers: the
// analytics core, what their answers point to, and the areas they asked for.
// Keys match SUGGESTED_MODULE_LABELS in andrho's lib/waitlist.js.
func SuggestModules(a Answers, plan string) []string {
	out := []string{"analitica"}
	add := func(m string) {
		for _, x := range out {
			if x == m {
				return
			}
		}
		out = append(out, m)
	}

	if has(a.PainPoints, "datos_dispersos") || a.Management == "varias" {
		add("unificacion")
	}
	if has([]string{a.Management}, "papel", "excel", "nada") {
		add("erp")
	}
	if a.HasWebsite == "si" {
		add("tracking")
	} else {
		add("sitio_web")
	}
	if has(a.SocialNetworks, "ninguna") {
		add("redes_creacion")
	} else if len(a.SocialNetworks) > 0 {
		add("redes")
	}
	if has(a.MessagingApps, "whatsapp", "whatsapp_business") {
		add("whatsapp")
	}
	if has(a.Sectors, "restaurantes") {
		add("vencimientos")
	}
	if has(a.Sectors, "comercio", "restaurantes", "manufactura") {
		add("inventario")
	}
	if has([]string{a.UnsoldHandling}, "se_pierde", "bodega") {
		add("mermas")
	}
	sectorModules := map[string]string{"servicios": "agenda", "manufactura": "produccion", "logistica": "rutas", "salud": "citas"}
	for _, s := range a.Sectors {
		if m, ok := sectorModules[s]; ok {
			add(m)
		}
	}
	for _, m := range a.Modules {
		if m == "sitio_web" && a.HasWebsite == "si" {
			m = "tracking"
		}
		add(m)
	}
	if plan == PlanTodo {
		add("creators")
	}

	known := out[:0]
	for _, m := range out {
		if knownModules[m] {
			known = append(known, m)
		}
	}
	return known
}

// knownModules are the keys the survey knows how to show; anything else a
// client sends in `modules` is dropped.
var knownModules = map[string]bool{
	"analitica": true, "unificacion": true, "erp": true, "sitio_web": true, "tracking": true,
	"redes_creacion": true, "redes": true, "whatsapp": true, "vencimientos": true, "inventario": true,
	"mermas": true, "agenda": true, "produccion": true, "rutas": true, "citas": true, "finanzas": true,
	"ventas": true, "marketing": true, "clientes": true, "compras": true, "logistica": true, "rrhh": true,
	"creators": true,
}
