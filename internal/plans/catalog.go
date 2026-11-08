// Package plans is AndRho's plan catalog: the single source of the plans,
// their prices and limits, what each one can do (capabilities) and the
// complementos (add-ons) that sell the next plan's features one by one.
//
// Nothing else in AndRho writes a price or a limit by hand: the signup's
// suggestion (internal/analyzer), the website gate at signup (Parte B of
// todo.md), the suggestion quota and the UI (GET /plans, POST /plans/quote)
// all read it from here.
//
// Code asks capabilities, never plan names: `Effective(plan, addons).Website`,
// not `plan == "despegue"`.
package plans

import (
	"fmt"
	"strings"
)

// Unlimited marks a limit without a cap (sucursales, usuarios, sugerencias).
const Unlimited = -1

// Capabilities is what an account may do. Every gate in AndRho reads these.
type Capabilities struct {
	Branches int  `json:"branches"` // sucursales (-1 = ilimitadas)
	Users    int  `json:"users"`    // usuarios (-1 = ilimitados)
	Social   bool `json:"social"`   // Facebook e Instagram
	// WhatsApp: "" (no), "avisos" (avisos y reportes) or "control" (control
	// total por comandos, WhatsAppCommands al mes).
	WhatsApp         string `json:"whatsapp"`
	WhatsAppCommands int    `json:"whatsapp_commands"`
	OwnNumber        bool   `json:"own_number"` // número de WhatsApp propio
	// Sitio web y SEO, as flags (todo.md A4.6). Only En órbita (tienda) and
	// Galaxy (pro) include one; any other plan buys it (site.go):
	//   Website  = sitio + SEO básico (robots, sitemap, canonical,
	//              LocalBusiness, verificación de Search Console)
	//   Store    = + tienda en línea (Product schema, sitemap de productos)
	//   FullSite = + páginas por sucursal y servicio, blog y landings
	Website  bool `json:"website"`
	Store    bool `json:"store"`
	FullSite bool `json:"full_site"`
	// Tareas de Creators al mes (sin contar la construcción del sitio).
	CreatorTasks int `json:"creator_tasks"`
	// Soporte: "autoservicio" or "chat", plus horas en vivo al mes.
	Support          string `json:"support"`
	LiveSupportHours int    `json:"live_support_hours"`
	// What the dashboard already gated by plan before this catalog existed.
	Suggestions int `json:"suggestions"`  // sugerencias de AndRho al mes (-1 = ilimitadas)
	ModuleLimit int `json:"module_limit"` // módulos del análisis que caben en el plan (-1 = todos)
}

// SEOLevel names the website capability for people ("" without a site).
func (c Capabilities) SEOLevel() string {
	switch {
	case c.FullSite:
		return "completo"
	case c.Store:
		return "tienda"
	case c.Website:
		return "basico"
	}
	return ""
}

// Plan is one plan of the catalog.
type Plan struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`  // "En órbita"
	Label   string       `json:"label"` // "Pro"
	Price   int          `json:"price"` // quetzales al mes
	Tagline string       `json:"tagline"`
	Caps    Capabilities `json:"capabilities"`
}

// Catalog, in ascending order (todo.md C1). Suggestions and ModuleLimit for
// base and plataforma are new (those plans didn't exist): 3/1 and 5/2.
var catalog = []Plan{
	{ID: "base", Name: "Base", Label: "Simple", Price: 120, Tagline: "Tu negocio en un panel, sin complicarte.",
		Caps: Capabilities{Branches: 1, Users: 1, Support: "autoservicio", Suggestions: 3, ModuleLimit: 1}},
	{ID: "plataforma", Name: "Plataforma", Label: "Básico", Price: 349, Tagline: "Tus ventas y tus redes, en el mismo lugar.",
		Caps: Capabilities{Branches: 1, Users: 2, Social: true, Support: "chat", Suggestions: 5, ModuleLimit: 2}},
	{ID: "despegue", Name: "Despegue", Label: "En crecimiento", Price: 750, Tagline: "Lo esencial para dejar de decidir a ciegas.",
		Caps: Capabilities{Branches: 3, Users: 6, Social: true, WhatsApp: "avisos", CreatorTasks: 1, Support: "chat",
			Suggestions: 10, ModuleLimit: 3}},
	{ID: "en_orbita", Name: "En órbita", Label: "Pro", Price: 1200, Tagline: "Para crecer con cada área conectada.",
		Caps: Capabilities{Branches: 6, Users: 15, Social: true, WhatsApp: "control", WhatsAppCommands: 400, Website: true, Store: true,
			CreatorTasks: 2, Support: "chat", LiveSupportHours: 1, Suggestions: 30, ModuleLimit: 6}},
	{ID: "galaxy", Name: "Galaxy", Label: "Ilimitado", Price: 2990, Tagline: "Tu negocio completo. Nada se queda en tierra.",
		Caps: Capabilities{Branches: Unlimited, Users: Unlimited, Social: true, WhatsApp: "control", WhatsAppCommands: 1500, OwnNumber: true,
			Website: true, Store: true, FullSite: true, CreatorTasks: 4, Support: "chat", LiveSupportHours: 2,
			Suggestions: Unlimited, ModuleLimit: Unlimited}},
}

// legacy maps plan ids used before this catalog to their catalog id.
var legacy = map[string]string{"galactico": "galaxy"}

// Normalize returns the catalog id for id (trimmed, lower-case, legacy ids
// mapped), or "" when it isn't a plan.
func Normalize(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if to, ok := legacy[id]; ok {
		id = to
	}
	if _, ok := index[id]; ok {
		return id
	}
	return ""
}

var index = map[string]int{}

func init() {
	for i, p := range catalog {
		index[p.ID] = i
	}
	for i := range steps {
		priceStep(&steps[i])
	}
}

// All returns the plans in ascending order (a copy).
func All() []Plan { return append([]Plan(nil), catalog...) }

// IDs returns the plan ids in ascending order.
func IDs() []string {
	out := make([]string, len(catalog))
	for i, p := range catalog {
		out[i] = p.ID
	}
	return out
}

// Get returns the plan for id (legacy ids accepted).
func Get(id string) (Plan, bool) {
	i, ok := index[Normalize(id)]
	if !ok {
		return Plan{}, false
	}
	return catalog[i], true
}

// MustGet is Get for ids known to be valid; an unknown id falls back to base.
func MustGet(id string) Plan {
	if p, ok := Get(id); ok {
		return p
	}
	return catalog[0]
}

// Next returns the plan after id (false for Galaxy or an unknown id).
func Next(id string) (Plan, bool) {
	i, ok := index[Normalize(id)]
	if !ok || i+1 >= len(catalog) {
		return Plan{}, false
	}
	return catalog[i+1], true
}

// Rank is the plan's position (0 = base), -1 when unknown.
func Rank(id string) int {
	if i, ok := index[Normalize(id)]; ok {
		return i
	}
	return -1
}

// Includes is what a plan includes, in words, for the plan cards ("lista de
// lo incluido"). Built from the capabilities, never written per plan.
func Includes(p Plan) []string {
	c := p.Caps
	count := func(n int, one, many string) string {
		if n == Unlimited {
			return many + " ilimitados"
		}
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	out := []string{
		strings.Replace(count(c.Branches, "sucursal", "sucursales"), "sucursales ilimitados", "Sucursales ilimitadas", 1),
		count(c.Users, "usuario", "usuarios"),
	}
	if c.Users == Unlimited {
		out[1] = "Usuarios ilimitados"
	}
	if c.Social {
		out = append(out, "Facebook e Instagram")
	}
	switch c.WhatsApp {
	case "avisos":
		out = append(out, "WhatsApp: avisos y reportes")
	case "control":
		line := fmt.Sprintf("WhatsApp: control total, %s comandos al mes", thousands(c.WhatsAppCommands))
		if c.OwnNumber {
			line += " y número propio"
		}
		out = append(out, line)
	}
	switch c.SEOLevel() {
	case "basico":
		out = append(out, "Sitio web y SEO básico (construcción y mantenimiento)")
	case "tienda":
		out = append(out, "Sitio web con tienda en línea (construcción y mantenimiento)")
	case "completo":
		out = append(out, "Sitio pro: tienda, páginas por sucursal y blog (construcción y mantenimiento)")
	}
	if c.CreatorTasks > 0 {
		out = append(out, count(c.CreatorTasks, "tarea de Creators al mes", "tareas de Creators al mes"))
	}
	if c.Suggestions == Unlimited {
		out = append(out, "Sugerencias ilimitadas")
	} else {
		out = append(out, fmt.Sprintf("%d sugerencias de AndRho al mes", c.Suggestions))
	}
	support := map[string]string{"autoservicio": "Soporte: autoservicio", "chat": "Soporte por chat"}[c.Support]
	if c.LiveSupportHours > 0 {
		support += fmt.Sprintf(" + %d h en vivo", c.LiveSupportHours)
	}
	return append(out, support)
}

// thousands formats 1500 as "1,500" (the catalog's style).
func thousands(n int) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	return s[:len(s)-3] + "," + s[len(s)-3:]
}

// FormatQ formats quetzales as the catalog does ("Q1,200").
func FormatQ(n int) string { return "Q" + thousands(n) }
