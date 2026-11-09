package plans

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Complementos (todo.md C3). Each step of the catalog (a plan to the next)
// lists what the next plan adds; each of those can be bought alone. Its
// price comes from the step's difference in price and the feature's weight:
//
//	Δ = precio(siguiente) − precio(seleccionado)
//	precio = redondear_arriba_a_Q5( Δ × peso / Σpesos × Markup )
//
// Markup (1.3) makes buying everything separately cost more than moving up
// a plan, so upgrading is always the best offer. The prices are computed
// once at start (priceStep) and kept in the catalog.

// Markup is the add-on surcharge over its share of the step, in tenths
// (13 = 1.3) so prices are exact integers.
const Markup = 13

// RoundTo is what add-on prices are rounded up to (Q5).
const RoundTo = 5

// Addon is one feature of the next plan, sold alone.
type Addon struct {
	FeatureID string `json:"feature_id"`
	Label     string `json:"label"`
	Detail    string `json:"detail"`
	// Weight in quarters (4 = peso 1, 1 = peso 0.25), so the math is exact.
	Weight int `json:"-"`
	// MaxQty is how many can be bought (1 for a yes/no feature): up to the
	// next plan's cap.
	MaxQty int `json:"max_qty"`
	// Requires are capabilities the account must already have ("website",
	// "whatsapp_avisos").
	Requires []string `json:"requires,omitempty"`
	// Price is per unit, in quetzales (set by priceStep).
	Price int `json:"price"`
	apply func(c *Capabilities, qty int)
}

// PerUnit says whether the add-on is bought by quantity.
func (a Addon) PerUnit() bool { return a.MaxQty > 1 }

// WeightValue is the weight as the catalog writes it (1, 0.5, 0.25…).
func (a Addon) WeightValue() float64 { return float64(a.Weight) / 4 }

type step struct {
	From, To string
	Addons   []Addon
	// Reserved is weight (quarters) that still counts in Σpesos but isn't
	// sold here: the website's, since it became its own product (site.go).
	// It keeps the other add-ons at the prices they had.
	Reserved int
}

func addUsers(n int) func(*Capabilities, int) {
	return func(c *Capabilities, q int) {
		if c.Users != Unlimited {
			c.Users += n * q
		}
	}
}

func addBranches(c *Capabilities, q int) {
	if c.Branches != Unlimited {
		c.Branches += q
	}
}

func addCreatorTasks(n int) func(*Capabilities, int) {
	return func(c *Capabilities, q int) { c.CreatorTasks += n * q }
}

func addLiveSupport(c *Capabilities, q int) {
	c.LiveSupportHours += q
	if c.Support == "autoservicio" {
		c.Support = "chat"
	}
}

func whatsAppControl(commands int, own bool) func(*Capabilities, int) {
	return func(c *Capabilities, _ int) {
		c.WhatsApp = "control"
		if commands > c.WhatsAppCommands {
			c.WhatsAppCommands = commands
		}
		c.OwnNumber = c.OwnNumber || own
	}
}

// steps: the table of todo.md C3, in catalog order.
var steps = []step{
	{From: "base", To: "plataforma", Addons: []Addon{
		{FeatureID: "usuario_extra", Label: "Usuario extra", Detail: "Una persona más de tu equipo en AndRho (+1).", Weight: 4, MaxQty: 1, apply: addUsers(1)},
		{FeatureID: "facebook_instagram", Label: "Facebook e Instagram", Detail: "Tus redes medidas junto con tus ventas.", Weight: 12, MaxQty: 1,
			apply: func(c *Capabilities, _ int) { c.Social = true }},
		{FeatureID: "soporte_chat", Label: "Soporte por chat", Detail: "Escríbenos y te ayudamos en el día.", Weight: 4, MaxQty: 1,
			apply: func(c *Capabilities, _ int) { c.Support = "chat" }},
	}},
	{From: "plataforma", To: "despegue", Reserved: 16, Addons: []Addon{
		{FeatureID: "sucursal_extra", Label: "Sucursal extra", Detail: "Cada sucursal con sus ventas e inventario (c/u, hasta +2).", Weight: 4, MaxQty: 2, apply: addBranches},
		{FeatureID: "usuario_extra", Label: "Usuario extra", Detail: "Más personas de tu equipo (c/u, hasta +4).", Weight: 2, MaxQty: 4, apply: addUsers(1)},
		{FeatureID: "whatsapp_avisos", Label: "WhatsApp: avisos y reportes", Detail: "AndRho te avisa y te manda tus reportes por WhatsApp.", Weight: 8, MaxQty: 1,
			apply: func(c *Capabilities, _ int) {
				if c.WhatsApp == "" {
					c.WhatsApp = "avisos"
				}
			}},
		{FeatureID: "creator_task", Label: "Tarea de Creator (+1/mes)", Detail: "Un diseñador o programador hace una tarea para ti cada mes.", Weight: 12, MaxQty: 1, apply: addCreatorTasks(1)},
	}},
	{From: "despegue", To: "en_orbita", Reserved: 16, Addons: []Addon{
		{FeatureID: "sucursal_extra", Label: "Sucursal extra", Detail: "Cada sucursal con sus ventas e inventario (c/u, hasta +3).", Weight: 2, MaxQty: 3, apply: addBranches},
		{FeatureID: "usuario_extra", Label: "Usuario extra", Detail: "Más personas de tu equipo (c/u, hasta +9).", Weight: 1, MaxQty: 9, apply: addUsers(1)},
		{FeatureID: "whatsapp_control", Label: "WhatsApp: control total (400)", Detail: "Maneja AndRho con mensajes: 400 comandos al mes.", Weight: 12, MaxQty: 1,
			Requires: []string{"whatsapp_avisos"}, apply: whatsAppControl(400, false)},
		{FeatureID: "creator_task", Label: "Tarea de Creator (+1/mes)", Detail: "Una tarea más de Creators cada mes.", Weight: 10, MaxQty: 1, apply: addCreatorTasks(1)},
		{FeatureID: "soporte_vivo", Label: "Soporte en vivo 1 h", Detail: "Una hora al mes con una persona de AndRho, en llamada.", Weight: 4, MaxQty: 1, apply: addLiveSupport},
	}},
	{From: "en_orbita", To: "galaxy", Reserved: 12, Addons: []Addon{
		{FeatureID: "sucursales_ilimitadas", Label: "Sucursales ilimitadas", Detail: "Todas las sucursales que abras.", Weight: 8, MaxQty: 1,
			apply: func(c *Capabilities, _ int) { c.Branches = Unlimited }},
		{FeatureID: "usuarios_ilimitados", Label: "Usuarios ilimitados", Detail: "Todo tu equipo en AndRho.", Weight: 8, MaxQty: 1,
			apply: func(c *Capabilities, _ int) { c.Users = Unlimited }},
		{FeatureID: "whatsapp_propio", Label: "WhatsApp 1,500 + número propio", Detail: "1,500 comandos al mes y AndRho en tu propio número.", Weight: 16, MaxQty: 1,
			Requires: []string{"whatsapp_avisos"}, apply: whatsAppControl(1500, true)},
		{FeatureID: "creator_task", Label: "Tareas de Creator (+2/mes)", Detail: "Dos tareas más de Creators cada mes.", Weight: 16, MaxQty: 1, apply: addCreatorTasks(2)},
		{FeatureID: "soporte_vivo", Label: "Soporte en vivo +1 h", Detail: "Una hora más al mes con una persona de AndRho.", Weight: 4, MaxQty: 1, apply: addLiveSupport},
	}},
}

// priceStep sets each add-on's price from the step's Δ and its weight.
func priceStep(s *step) {
	from, to := catalog[index[s.From]], catalog[index[s.To]]
	delta := to.Price - from.Price
	total := s.Reserved
	for _, a := range s.Addons {
		total += a.Weight
	}
	for i := range s.Addons {
		s.Addons[i].Price = AddonPrice(delta, s.Addons[i].Weight, total)
	}
}

// AddonPrice is the C3 rule with integer math: Δ × w / Σw × 1.3, rounded up
// to Q5 (weights in any common unit).
func AddonPrice(delta, weight, totalWeight int) int {
	num := delta * weight * Markup
	den := totalWeight * 10 * RoundTo
	return (num + den - 1) / den * RoundTo
}

func stepFrom(planID string) (step, bool) {
	id := Normalize(planID)
	for _, s := range steps {
		if s.From == id {
			return s, true
		}
	}
	return step{}, false
}

// Addons returns the add-ons offered on planID: only the next plan's
// features (none on Galaxy).
func Addons(planID string) []Addon {
	s, _ := stepFrom(planID)
	return append([]Addon(nil), s.Addons...)
}

// has reports whether caps already provide a requirement.
func has(c Capabilities, req string) bool {
	switch req {
	case "website":
		return c.Website
	case "whatsapp_avisos":
		return c.WhatsApp != ""
	case "social":
		return c.Social
	}
	return false
}

// requirementLabel says, for people, what a requirement is.
var requirementLabel = map[string]string{
	"website":         "Sitio web (incluido en En órbita y Galaxy, o compra el sitio)",
	"whatsapp_avisos": "WhatsApp: avisos y reportes (incluido desde Despegue)",
	"social":          "Facebook e Instagram",
}

// Requirement is one dependency of a feature and whether it's met.
type Requirement struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Met   bool   `json:"met"`
}

// AddonOffer is how a feature can be bought alone.
type AddonOffer struct {
	Price   int  `json:"price"` // per unit
	MaxQty  int  `json:"max_qty"`
	PerUnit bool `json:"per_unit"`
}

// Feature is one thing the next plan gains over the selected one.
type Feature struct {
	FeatureID string        `json:"featureId"`
	Label     string        `json:"label"`
	Detail    string        `json:"detail"`
	Addon     *AddonOffer   `json:"addon"`
	Requires  []Requirement `json:"requires"`
	// Selectable is false while a requirement is missing.
	Selectable bool `json:"selectable"`
	// Needed: the business said it needs this (the signup's analysis).
	Needed bool `json:"needed"`
}

// NextPlanFeatures is what the plan after planID adds over planID (only the
// differences), most needed first: needs are the analysis' module keys
// (internal/analyzer's "necesidades detectadas"), in their order. next is
// nil on Galaxy.
func NextPlanFeatures(planID string, needs []string) (*Plan, []Feature, error) {
	p, ok := Get(planID)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrUnknownPlan, planID)
	}
	next, ok := Next(p.ID)
	if !ok {
		return nil, []Feature{}, nil
	}
	s, _ := stepFrom(p.ID)
	rank := needRank(needs)
	out := make([]Feature, 0, len(s.Addons))
	for _, a := range s.Addons {
		f := Feature{FeatureID: a.FeatureID, Label: a.Label, Detail: a.Detail, Requires: []Requirement{}, Selectable: true,
			Addon: &AddonOffer{Price: a.Price, MaxQty: a.MaxQty, PerUnit: a.PerUnit()}}
		for _, r := range a.Requires {
			met := has(p.Caps, r)
			f.Requires = append(f.Requires, Requirement{ID: r, Label: requirementLabel[r], Met: met})
			f.Selectable = f.Selectable && met
		}
		_, f.Needed = rank[a.FeatureID]
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, oki := rank[out[i].FeatureID]
		rj, okj := rank[out[j].FeatureID]
		if oki != okj {
			return oki
		}
		return oki && ri < rj
	})
	return &next, out, nil
}

// featuresByNeed maps the analysis' modules (internal/analyzer.Modules) to
// the features that serve them.
var featuresByNeed = map[string][]string{
	"redes":          {"facebook_instagram"},
	"redes_creacion": {"facebook_instagram", "creator_task"},
	"marketing":      {"facebook_instagram"},
	"whatsapp":       {"whatsapp_avisos", "whatsapp_control", "whatsapp_propio"},
	"creators":       {"creator_task"},
	"rrhh":           {"usuario_extra", "usuarios_ilimitados"},
	"logistica":      {"sucursal_extra", "sucursales_ilimitadas"},
	"rutas":          {"sucursal_extra", "sucursales_ilimitadas"},
}

func needRank(needs []string) map[string]int {
	rank := map[string]int{}
	for i, n := range needs {
		for _, f := range featuresByNeed[n] {
			if _, ok := rank[f]; !ok {
				rank[f] = i
			}
		}
	}
	return rank
}

// Selection is one add-on picked (Qty defaults to 1).
type Selection struct {
	FeatureID string `json:"feature_id"`
	Qty       int    `json:"qty"`
}

// QuoteLine is one priced add-on (or the website's construction).
type QuoteLine struct {
	FeatureID string `json:"featureId"`
	Label     string `json:"label"`
	Qty       int    `json:"qty"`
	Price     int    `json:"price"` // qty × unit price
}

// Quote is the monthly price of a plan plus add-ons (todo.md C3.1).
type Quote struct {
	Plan        string      `json:"plan"`
	BasePrice   int         `json:"basePrice"`
	Addons      []QuoteLine `json:"addons"`
	AddonsTotal int         `json:"addonsTotal"`
	Total       int         `json:"total"` // al mes
	// Charged once: the website's construction.
	Setup         []QuoteLine `json:"setup"`
	SetupTotal    int         `json:"setupTotal"`
	NextPlan      string      `json:"nextPlan,omitempty"` // "" on Galaxy
	NextPlanPrice int         `json:"nextPlanPrice,omitempty"`
	// UpgradeIsCheaper: total >= the next plan's price; the UI then
	// recommends the next plan.
	UpgradeIsCheaper bool `json:"upgradeIsCheaper"`
}

// Errors from CalculateAddonPrice (wrapped with details).
var (
	ErrUnknownPlan    = errors.New("plans: unknown plan")
	ErrNotOffered     = errors.New("plans: add-on not offered on this plan")
	ErrQty            = errors.New("plans: quantity out of range")
	ErrMissingRequire = errors.New("plans: missing requirement")
)

// CalculateAddonPrice prices planID plus the selected add-ons. Only the
// next plan's add-ons are accepted; quantities up to the next plan's cap;
// a feature whose requirement the plan lacks can't be selected. Selecting
// the same feature twice adds the quantities.
func CalculateAddonPrice(planID string, selected []Selection) (Quote, error) {
	p, ok := Get(planID)
	if !ok {
		return Quote{}, fmt.Errorf("%w: %q", ErrUnknownPlan, planID)
	}
	q := Quote{Plan: p.ID, BasePrice: p.Price, Addons: []QuoteLine{}, Setup: []QuoteLine{}}
	if next, ok := Next(p.ID); ok {
		q.NextPlan, q.NextPlanPrice = next.ID, next.Price
	}
	s, _ := stepFrom(p.ID)
	byID := map[string]Addon{}
	for _, a := range s.Addons {
		byID[a.FeatureID] = a
	}
	qty := map[string]int{}
	order := []string{}
	var site *SiteOption
	for _, sel := range selected {
		id := strings.TrimSpace(sel.FeatureID)
		// The website: one level, on any plan that doesn't include it.
		if IsSite(id) {
			o, err := siteOffer(p.ID, id)
			if err != nil {
				return Quote{}, err
			}
			if site != nil && site.ID != o.ID {
				return Quote{}, fmt.Errorf("%w: un solo sitio por cuenta", ErrQty)
			}
			site = &o
			continue
		}
		a, ok := byID[id]
		if !ok {
			return Quote{}, fmt.Errorf("%w: %q en %s", ErrNotOffered, id, p.ID)
		}
		n := sel.Qty
		if n == 0 {
			n = 1
		}
		if n < 0 {
			return Quote{}, fmt.Errorf("%w: %s", ErrQty, id)
		}
		if _, seen := qty[id]; !seen {
			order = append(order, id)
		}
		qty[id] += n
		if qty[id] > a.MaxQty {
			return Quote{}, fmt.Errorf("%w: %s admite hasta %d", ErrQty, id, a.MaxQty)
		}
		for _, r := range a.Requires {
			if !has(p.Caps, r) {
				return Quote{}, fmt.Errorf("%w: %s necesita %s", ErrMissingRequire, id, requirementLabel[r])
			}
		}
	}
	for _, id := range order {
		a := byID[id]
		line := QuoteLine{FeatureID: id, Label: a.Label, Qty: qty[id], Price: a.Price * qty[id]}
		q.Addons = append(q.Addons, line)
		q.AddonsTotal += line.Price
	}
	// Moving up is compared without the site, which the next plan rarely includes.
	q.UpgradeIsCheaper = q.NextPlan != "" && q.BasePrice+q.AddonsTotal >= q.NextPlanPrice
	if site != nil {
		label := site.Label
		if site.Upgrade {
			label += " (mejora)"
		}
		// An upgrade over an included site keeps the same upkeep: no line.
		if site.Monthly > 0 {
			q.Addons = append(q.Addons, QuoteLine{FeatureID: site.FeatureID, Label: "Mantenimiento: " + label, Qty: 1, Price: site.Monthly})
			q.AddonsTotal += site.Monthly
		}
		q.Setup = append(q.Setup, QuoteLine{FeatureID: site.FeatureID, Label: "Construcción: " + label, Qty: 1, Price: site.Setup})
		q.SetupTotal = site.Setup
	}
	q.Total = q.BasePrice + q.AddonsTotal
	return q, nil
}

// Effective is what an account on planID with these add-ons can do. Add-ons
// that don't apply to the plan (e.g. bought on a lower plan before an
// upgrade) never take anything away.
func Effective(planID string, addons []Selection) Capabilities {
	p := MustGet(planID)
	c := p.Caps
	all := map[string][]Addon{}
	for _, s := range steps {
		for _, a := range s.Addons {
			all[a.FeatureID] = append(all[a.FeatureID], a)
		}
	}
	s, _ := stepFrom(p.ID)
	for _, sel := range addons {
		n := sel.Qty
		if n <= 0 {
			n = 1
		}
		// The website (and the add-ons that sold it before it was a product).
		if l, ok := siteByFeature(sel.FeatureID); ok {
			l.applyTo(&c)
			continue
		}
		// The add-on as priced on this plan's step; otherwise the step it
		// was bought on (an add-on from below the plan changes nothing the
		// plan doesn't already give, see the cap below).
		var a *Addon
		for i := range s.Addons {
			if s.Addons[i].FeatureID == sel.FeatureID {
				a = &s.Addons[i]
			}
		}
		if a == nil {
			if list := all[sel.FeatureID]; len(list) > 0 {
				a = &list[0]
			}
		}
		if a == nil || a.apply == nil {
			continue
		}
		a.apply(&c, n)
		c = keepBest(c, p.Caps)
	}
	return c
}

// keepBest never lets an add-on lower what the plan itself gives.
func keepBest(after, plan Capabilities) Capabilities {
	maxLimit := func(a, b int) int {
		if a == Unlimited || b == Unlimited {
			return Unlimited
		}
		if a > b {
			return a
		}
		return b
	}
	after.Branches = maxLimit(after.Branches, plan.Branches)
	after.Users = maxLimit(after.Users, plan.Users)
	if after.WhatsAppCommands < plan.WhatsAppCommands {
		after.WhatsAppCommands = plan.WhatsAppCommands
	}
	if plan.WhatsApp == "control" {
		after.WhatsApp = "control"
	}
	return after
}
