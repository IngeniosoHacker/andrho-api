// Package insights is the pipeline behind the dashboard's AndRho panel:
// every time someone opens a section, its data is extracted (the tracker,
// the ERP), processed with plain statistics (week-over-week change,
// anomalies by z-score, trends, days of stock cover) into findings, and
// the findings are put into plain language (Claude when configured, rules
// otherwise) as a summary, comments, notifications and suggestions to
// accept or reject. The numbers always come from the statistics; the AI
// only translates them.
package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
	"github.com/IngeniosoHacker/andrho-api/internal/rstats"
)

// Finding is one fact the statistics found.
type Finding struct {
	Kind     string `json:"kind"`     // resumen | comentario | notificacion
	Severity string `json:"severity"` // info | buena | alerta
	Text     string `json:"text"`
}

// Proposal is a suggestion before it's stored: what to do and why.
// Evidence is the technical backing (for AndRho's team and Creators);
// Explain is the same for the business owner (the "¿Por qué?" view);
// Action is what accepting it does (explain.go).
type Proposal struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Evidence string   `json:"evidence"`
	Explain  *Explain `json:"explain,omitempty"`
	Action   *Action  `json:"action,omitempty"`
}

// Result is what the panel shows.
type Result struct {
	Section     string     `json:"section"`
	Summary     string     `json:"summary"`
	Findings    []Finding  `json:"findings"`
	Proposals   []Proposal `json:"proposals"`
	Source      string     `json:"source"` // ia | reglas
	HasData     bool       `json:"has_data"`
	GeneratedAt time.Time  `json:"generated_at"`
}

// Sources is where the data comes from; Odoo may be nil. Stats runs the R
// analyses (webstats.go); nil or unavailable means Go statistics only.
type Sources struct {
	Tracker *pgxpool.Pool
	SiteID  string
	Odoo    *odoo.Client
	Stats   *rstats.Client
	// BusinessType (internal/business) decides what Inicio leads with.
	BusinessType string
}

type analysis struct {
	findings  []Finding
	proposals []Proposal
	hasData   bool
}

func (a *analysis) add(kind, sev, format string, args ...any) {
	a.findings = append(a.findings, Finding{kind, sev, fmt.Sprintf(format, args...)})
}

func (a *analysis) propose(title, body, evidence string) {
	a.proposals = append(a.proposals, Proposal{Title: title, Body: body, Evidence: evidence})
}

func pct(x float64) string { return fmt.Sprintf("%+.0f%%", x*100) }

// count is "1 producto" / "3 productos".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Run extracts, processes and translates one section.
func Run(ctx context.Context, src Sources, client *ai.Client, accountID, section string) Result {
	var a analysis
	switch section {
	case "web":
		web(ctx, src, &a)
		webDeep(ctx, src, &a) // R: ANOVA + rank models (webstats.go)
	case "inventario":
		inventory(ctx, src, &a)
	case "clientes":
		customers(ctx, src, &a)
	case "prospectos":
		leads(ctx, src, &a)
	case "sucursales":
		branches(ctx, src, &a)
	case "envivo":
		liveSales(ctx, src, &a)
	case "inicio", "resumen":
		// The whole business at a glance: the headline of each area.
		for _, f := range headlineParts(src.BusinessType) {
			var part analysis
			f(ctx, src, &part)
			if part.hasData {
				a.hasData = true
				a.findings = append(a.findings, headline(part.findings)...)
				if len(part.proposals) > 0 {
					a.proposals = append(a.proposals, part.proposals[0])
				}
			}
		}
	}
	res := Result{Section: section, Findings: a.findings, Proposals: a.proposals, HasData: a.hasData, GeneratedAt: time.Now(), Source: "reglas"}
	if res.Findings == nil {
		res.Findings = []Finding{}
	}
	if res.Proposals == nil {
		res.Proposals = []Proposal{}
	}
	if !a.hasData {
		res.Summary = noDataSummary(section)
		// Connected, but missing the app this section reads: say that.
		for _, p := range a.proposals {
			if p.Action != nil && p.Action.Type == "erp_install" {
				res.Summary = p.Body
			}
		}
		return res
	}
	res.Summary = ruleSummary(a.findings)
	if client.Enabled() {
		if s, props, err := translate(ctx, client, accountID, section, a); err == nil {
			res.Summary, res.Source = s, "ia"
			if len(props) > 0 {
				res.Proposals = props
			}
		}
	}
	return res
}

// headline keeps a part's summary and its first alert.
func headline(fs []Finding) []Finding {
	out := []Finding{}
	alert := false
	for _, f := range fs {
		if f.Kind == "resumen" || (f.Severity == "alerta" && !alert) {
			out = append(out, f)
			alert = alert || f.Severity == "alerta"
		}
	}
	return out
}

func ruleSummary(fs []Finding) string {
	parts := []string{}
	for _, f := range fs {
		if f.Kind == "resumen" {
			parts = append(parts, f.Text)
		}
	}
	for _, f := range fs {
		if f.Severity == "alerta" {
			parts = append(parts, f.Text)
			break
		}
	}
	if len(parts) == 0 && len(fs) > 0 {
		parts = append(parts, fs[0].Text)
	}
	return strings.Join(parts, " ")
}

func noDataSummary(section string) string {
	switch section {
	case "web":
		return "Todavía no hay visitas registradas. Instala el rastreador en tu sitio y aquí verás lo que pasa."
	case "social":
		return "Cuando conectes tus redes, aquí verás qué publicaciones atraen clientes."
	case "inventario", "clientes", "prospectos", "sucursales":
		return "Conecta tu ERP y AndRho empezará a leer esta sección por ti."
	case "envivo":
		return "Después de tu primer live, aquí verás cuánto vendiste, qué no se vendió y a qué hora te va mejor."
	}
	return "AndRho resume aquí lo más importante de esta sección cada vez que entras."
}

// ---------------------------------------------------------------------
// Web (tracker)
// ---------------------------------------------------------------------

func web(ctx context.Context, src Sources, a *analysis) {
	if src.Tracker == nil || src.SiteID == "" {
		return
	}
	rows, err := src.Tracker.Query(ctx,
		`SELECT d::date, COUNT(s.id) FILTER (WHERE s.traffic_source_type NOT IN ('ai_crawler', 'search_bot'))
		 FROM generate_series(current_date - 27, current_date, interval '1 day') d
		 LEFT JOIN sessions s ON s.site_id = $1 AND s.started_at::date = d::date
		 GROUP BY 1 ORDER BY 1`, src.SiteID)
	if err != nil {
		return
	}
	var daily []float64
	for rows.Next() {
		var d time.Time
		var n float64
		if rows.Scan(&d, &n) == nil {
			daily = append(daily, n)
		}
	}
	rows.Close()
	if len(daily) != 28 || Sum(daily) == 0 {
		return
	}
	a.hasData = true
	week, prev := Sum(daily[21:]), Sum(daily[14:21])
	if c, ok := Change(week, prev); ok {
		sev := "info"
		if c >= 0.15 {
			sev = "buena"
		} else if c <= -0.2 {
			sev = "alerta"
		}
		a.add("resumen", sev, "Tu sitio tuvo %.0f visitas esta semana (%s que la anterior).", week, pct(c))
		if c <= -0.2 {
			lvl, txt := reading("en las dos últimas semanas")
			a.proposeX("Averigua por qué bajaron tus visitas",
				"Tus visitas cayeron "+pct(c)+" esta semana. Revisa si dejaste de publicar, si cambió una campaña o si una página dejó de aparecer en Google.",
				fmt.Sprintf("%.0f visitas esta semana contra %.0f la anterior.", week, prev),
				withConfidence(&Explain{Kind: "cambio", Unit: "visitas",
					Headline: fmt.Sprintf("Esta semana te visitaron %.0f personas; la semana pasada, %.0f.", week, prev),
					Bars:     []ExplainBar{{Label: "Semana pasada", Value: prev}, {Label: "Esta semana", Value: week, Highlight: true}},
					Takeaway: "Cuando las visitas caen de golpe casi siempre hay una causa concreta: una campaña que terminó, una publicación que no salió o una página que Google dejó de mostrar."},
					lvl, txt), nil)
		}
	} else {
		a.add("resumen", "info", "Tu sitio tuvo %.0f visitas esta semana.", week)
	}
	if z, ok := ZScore(daily[:27]); ok && math.Abs(z) >= 2 {
		if z > 0 {
			a.add("notificacion", "buena", "Ayer tuviste un pico de visitas: %.0f, muy por encima de lo normal.", daily[26])
		} else {
			a.add("notificacion", "alerta", "Ayer tuviste muchas menos visitas de lo normal (%.0f).", daily[26])
		}
	}

	var organic, aiRef, total, crawls, crawlsPrev float64
	var avgScroll, avgDur float64
	var topPage string
	_ = src.Tracker.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE traffic_source_type = 'organic_search'),
		        COUNT(*) FILTER (WHERE traffic_source_type = 'ai_referral'),
		        COUNT(*) FILTER (WHERE traffic_source_type NOT IN ('ai_crawler', 'search_bot')),
		        COALESCE(AVG(duration_ms) FILTER (WHERE duration_ms > 0), 0) / 1000
		 FROM sessions WHERE site_id = $1 AND started_at >= now() - interval '30 days'`, src.SiteID).
		Scan(&organic, &aiRef, &total, &avgDur)
	_ = src.Tracker.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE occurred_at >= now() - interval '7 days'),
		        COUNT(*) FILTER (WHERE occurred_at < now() - interval '7 days' AND occurred_at >= now() - interval '14 days')
		 FROM bot_visits WHERE site_id = $1 AND traffic_source_type = 'ai_crawler'`, src.SiteID).Scan(&crawls, &crawlsPrev)
	_ = src.Tracker.QueryRow(ctx,
		`SELECT COALESCE(AVG(max_scroll_percent), 0) FROM pageviews WHERE site_id = $1 AND entered_at >= now() - interval '30 days'`, src.SiteID).Scan(&avgScroll)
	_ = src.Tracker.QueryRow(ctx,
		`SELECT COALESCE(landing_path, '') FROM sessions WHERE site_id = $1 AND started_at >= now() - interval '30 days'
		 AND traffic_source_type NOT IN ('ai_crawler', 'search_bot') GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 1`, src.SiteID).Scan(&topPage)

	if total > 0 {
		share := organic / total
		a.add("comentario", "info", "%.0f%% de tus visitas del mes llegan desde buscadores.", share*100)
		if share < 0.2 {
			lvl, txt := pattern("tus visitas del último mes")
			a.proposeX("Escribe para lo que tus clientes buscan",
				"Pocas visitas llegan desde Google. Un artículo por cada pregunta frecuente de tus clientes te pone en esas búsquedas; súbelo en Web > Artículos.",
				fmt.Sprintf("Buscadores: %.0f de %.0f visitas (%.0f%%).", organic, total, share*100),
				withConfidence(&Explain{Kind: "proporcion", Unit: "visitas",
					Headline: fmt.Sprintf("Solo %.0f de cada 100 visitas te encuentran buscando en Google.", share*100),
					Bars:     []ExplainBar{{Label: "Desde Google", Value: organic, Highlight: true}, {Label: "Por otros caminos", Value: total - organic}},
					Takeaway: "Google es la fuente de clientes que no cuesta por clic. Si respondes en tu sitio lo que tus clientes preguntan, apareces cuando lo buscan."},
					lvl, txt), creatorAction("contenido"))
		}
	}
	if topPage != "" {
		a.add("comentario", "info", "Tu página más visitada este mes es %s.", topPage)
	}
	if avgScroll > 0 && avgScroll < 50 {
		a.add("comentario", "alerta", "En promedio la gente solo ve el %.0f%% de cada página.", avgScroll)
		lvl, txt := pattern("las páginas vistas este mes")
		a.proposeX("Pon lo importante arriba",
			"La mayoría no llega a la mitad de tus páginas. Mueve el precio, el botón de contacto y lo que te diferencia a la parte de arriba.",
			fmt.Sprintf("Scroll promedio: %.0f%%.", avgScroll),
			withConfidence(&Explain{Kind: "proporcion", Unit: "% de la página",
				Headline: fmt.Sprintf("En promedio, la gente solo ve el %.0f%% de cada página.", avgScroll),
				Bars:     []ExplainBar{{Label: "Lo que ven", Value: math.Round(avgScroll), Highlight: true}, {Label: "Lo que no llegan a ver", Value: 100 - math.Round(avgScroll)}},
				Takeaway: "Todo lo que está más abajo de ese punto casi nadie lo lee. Si tu precio o tu botón de contacto están ahí, la mayoría se va sin verlos."},
				lvl, txt), creatorAction("web"))
	}
	if c, ok := Change(crawls, crawlsPrev); ok {
		a.add("comentario", map[bool]string{true: "buena", false: "info"}[c > 0], "Los rastreadores de IA leyeron tu sitio %.0f veces esta semana (%s).", crawls, pct(c))
	}
	if crawls > 0 && aiRef == 0 {
		lvl, txt := pattern("el último mes")
		a.proposeX("Haz que los asistentes de IA te recomienden",
			"ChatGPT y otros ya leen tu sitio pero todavía no te mandan clientes. Agrega preguntas frecuentes con respuestas directas y datos concretos (precios, zonas, horarios).",
			fmt.Sprintf("%.0f lecturas de IA esta semana, 0 visitas desde asistentes en 30 días.", crawls),
			withConfidence(&Explain{Kind: "comparacion", Unit: "veces",
				Headline: "ChatGPT y otros asistentes ya leen tu sitio, pero todavía no le recomiendan tu negocio a nadie.",
				Bars:     []ExplainBar{{Label: "Veces que la IA leyó tu sitio (semana)", Value: crawls}, {Label: "Clientes que la IA te mandó (mes)", Value: 0, Highlight: true}},
				Takeaway: "Los asistentes recomiendan negocios cuando encuentran respuestas claras: precios, zonas, horarios y preguntas frecuentes escritas tal cual."},
				lvl, txt), creatorAction("contenido"))
	}
}

// ---------------------------------------------------------------------
// ERP sections (Odoo)
// ---------------------------------------------------------------------

type orderLine struct {
	product string
	qty     float64
	date    string
}

func orderLines(ctx context.Context, cl *odoo.Client, since time.Time) []orderLine {
	var rows []map[string]any
	_ = cl.SearchRead(ctx, "pos.order.line", []any{[]any{"order_id.date_order", ">=", since.UTC().Format("2006-01-02 15:04:05")},
		[]any{"order_id.state", "in", []string{"paid", "done", "invoiced"}}}, []string{"product_id", "qty", "create_date"}, "", 20000, &rows)
	out := []orderLine{}
	for _, r := range rows {
		out = append(out, orderLine{odoo.M2O(r["product_id"]), odoo.Num(r["qty"]), odoo.Str(r["create_date"])})
	}
	return out
}

func inventory(ctx context.Context, src Sources, a *analysis) {
	if src.Odoo == nil {
		return
	}
	var products []map[string]any
	if err := src.Odoo.SearchRead(ctx, "product.product", []any{[]any{"type", "=", "consu"}, []any{"default_code", "!=", "TIPS"}},
		[]string{"name", "qty_available", "standard_price"}, "", 2000, &products); err != nil || len(products) == 0 {
		return
	}
	a.hasData = true
	sold := map[string]float64{}
	for _, l := range orderLines(ctx, src.Odoo, time.Now().AddDate(0, 0, -30)) {
		sold[l.product] += l.qty
	}
	type item struct {
		name       string
		qty, cover float64
		value      float64
	}
	var out, short, dead []item
	value := 0.0
	for _, p := range products {
		it := item{name: odoo.Str(p["name"]), qty: odoo.Num(p["qty_available"])}
		it.value = it.qty * odoo.Num(p["standard_price"])
		value += it.value
		it.cover = DaysOfCover(it.qty, sold[it.name]/30)
		switch {
		case it.qty <= 0 && sold[it.name] > 0:
			out = append(out, it)
		case it.cover >= 0 && it.cover < 7:
			short = append(short, it)
		case sold[it.name] == 0 && it.value > 0:
			dead = append(dead, it)
		}
	}
	a.add("resumen", "info", "Tienes %d productos con un valor de Q%.0f a costo.", len(products), value)
	names := func(items []item, n int) string {
		s := []string{}
		for i, it := range items {
			if i == n {
				break
			}
			s = append(s, it.name)
		}
		return strings.Join(s, ", ")
	}
	if len(out) > 0 {
		a.add("notificacion", "alerta", "Se agotó lo que sí se vende: %s.", names(out, 3))
		bars := []ExplainBar{}
		for i, it := range out {
			if i == 5 {
				break
			}
			bars = append(bars, ExplainBar{Label: it.name, Value: sold[it.name], Highlight: true})
		}
		lvl, txt := pattern("tus ventas de los últimos 30 días")
		a.proposeX("Reabastece lo agotado", "Estos productos se venden y ya no hay: "+names(out, 5)+". Cada día sin ellos es venta perdida.",
			count(len(out), "producto", "productos")+" con ventas en 30 días y existencia 0.",
			withConfidence(&Explain{Kind: "comparacion", Unit: "unidades vendidas en 30 días",
				Headline: "Estos productos se vendieron este mes y hoy no te queda ninguno.",
				Bars:     bars,
				Takeaway: "Cada cliente que los pide y no los encuentra es una venta que se va con la competencia."}, lvl, txt), nil)
	}
	if len(short) > 0 {
		sort.Slice(short, func(i, j int) bool { return short[i].cover < short[j].cover })
		a.add("notificacion", "alerta", "%s se acaba en %.0f días al ritmo de venta actual.", short[0].name, math.Max(1, short[0].cover))
		bars := []ExplainBar{}
		for i, it := range short {
			if i == 5 {
				break
			}
			bars = append(bars, ExplainBar{Label: it.name, Value: math.Round(math.Max(0, it.cover)*10) / 10, Highlight: true})
		}
		lvl, txt := pattern("tus ventas de los últimos 30 días")
		a.proposeX("Pide antes de quedarte sin producto",
			"Al ritmo de venta del último mes, "+names(short, 4)+" no llegan a una semana. Haz el pedido a tus proveedores esta semana.",
			count(len(short), "producto", "productos")+" con menos de 7 días de existencia.",
			withConfidence(&Explain{Kind: "comparacion", Unit: "días que te alcanza",
				Headline: "Al ritmo en que se venden, estos productos se acaban en menos de una semana.",
				Bars:     bars,
				Takeaway: "Si el proveedor tarda unos días en entregar, el pedido tiene que salir ya para no quedarte sin nada."}, lvl, txt), nil)
	}
	if len(dead) > 0 {
		sort.Slice(dead, func(i, j int) bool { return dead[i].value > dead[j].value })
		tied := 0.0
		for _, d := range dead {
			tied += d.value
		}
		a.add("comentario", "alerta", "Tienes Q%.0f en productos que no se vendieron en 30 días.", tied)
		lvl, txt := pattern("tus ventas de los últimos 30 días")
		a.proposeX("Mueve lo que no se vende", "Haz una promoción o un combo con "+names(dead, 3)+": es dinero dormido en tu bodega.",
			fmt.Sprintf("Q%.0f sin rotación en 30 días.", tied),
			withConfidence(&Explain{Kind: "proporcion", Unit: "quetzales en bodega",
				Headline: fmt.Sprintf("Tienes Q%.0f en productos que nadie compró en un mes.", tied),
				Bars:     []ExplainBar{{Label: "Sin venderse", Value: math.Round(tied), Highlight: true}, {Label: "El resto de tu inventario", Value: math.Round(math.Max(0, value-tied))}},
				Takeaway: "Ese dinero está quieto. Una promoción o un combo lo convierte en efectivo para comprar lo que sí se vende."}, lvl, txt), nil)
	}
}

func paidOrders(ctx context.Context, cl *odoo.Client, since time.Time) []map[string]any {
	var orders []map[string]any
	_ = cl.SearchRead(ctx, "pos.order", []any{[]any{"state", "in", []string{"paid", "done", "invoiced"}},
		[]any{"date_order", ">=", since.UTC().Format("2006-01-02 15:04:05")}},
		[]string{"date_order", "amount_total", "partner_id", "config_id"}, "date_order", 20000, &orders)
	return orders
}

func customers(ctx context.Context, src Sources, a *analysis) {
	if src.Odoo == nil {
		return
	}
	if !src.Odoo.HasModel(ctx, "pos.order") && !src.Odoo.HasModel(ctx, "sale.order") {
		missingModule(a, "Activa el registro de ventas en tu ERP", "point_of_sale", "Ventas",
			"Tu ERP todavía no registra ventas, así que AndRho no puede saber quién te compra, cada cuánto ni quién dejó de venir. Al aceptar, AndRho activa el punto de venta en tu ERP por ti.",
			"Con cada venta registrada, AndRho te avisa qué clientes dejaron de venir, quiénes compran más y cuándo escribirles.")
		return
	}
	orders := paidOrders(ctx, src.Odoo, time.Now().AddDate(0, 0, -120))
	if len(orders) == 0 {
		return
	}
	a.hasData = true
	weeks := make([]float64, 8)
	type cust struct {
		n    int
		last time.Time
	}
	by := map[string]*cust{}
	now := time.Now().UTC()
	for _, o := range orders {
		t, err := time.Parse("2006-01-02 15:04:05", odoo.Str(o["date_order"]))
		if err != nil {
			continue
		}
		if w := int(now.Sub(t).Hours() / 24 / 7); w < 8 {
			weeks[7-w] += odoo.Num(o["amount_total"])
		}
		if p := odoo.M2O(o["partner_id"]); p != "" {
			c := by[p]
			if c == nil {
				c = &cust{}
				by[p] = c
			}
			c.n++
			if t.After(c.last) {
				c.last = t
			}
		}
	}
	if c, ok := Change(weeks[7], weeks[6]); ok {
		sev := "info"
		if c <= -0.2 {
			sev = "alerta"
		} else if c >= 0.15 {
			sev = "buena"
		}
		a.add("resumen", sev, "Vendiste Q%.0f esta semana (%s que la anterior).", weeks[7], pct(c))
	}
	if s := Slope(weeks); s != 0 {
		dir := "suben"
		if s < 0 {
			dir = "bajan"
		}
		a.add("comentario", map[bool]string{true: "buena", false: "alerta"}[s > 0], "En las últimas 8 semanas tus ventas %s en promedio Q%.0f por semana.", dir, math.Abs(s))
	}
	var lost []string
	for name, c := range by {
		if c.n >= 3 && now.Sub(c.last) > 30*24*time.Hour {
			lost = append(lost, name)
		}
	}
	sort.Strings(lost)
	if len(lost) > 0 {
		a.add("notificacion", "alerta", "%s no han vuelto en más de un mes.", count(len(lost), "cliente frecuente", "clientes frecuentes"))
		list := lost
		if len(list) > 4 {
			list = list[:4]
		}
		bars := []ExplainBar{}
		for _, name := range list {
			bars = append(bars, ExplainBar{Label: name, Value: math.Round(now.Sub(by[name].last).Hours() / 24), Highlight: true})
		}
		lvl, txt := pattern("tus ventas de los últimos 4 meses")
		a.proposeX("Escríbele a quien dejó de venir", "Mándales un mensaje o una promoción a "+strings.Join(list, ", ")+". Recuperar un cliente cuesta menos que conseguir uno nuevo.",
			fmt.Sprintf("%d clientes con 3 o más compras y más de 30 días sin volver.", len(lost)),
			withConfidence(&Explain{Kind: "comparacion", Unit: "días sin comprar",
				Headline: count(len(lost), "cliente que compraba seguido ya no ha vuelto", "clientes que compraban seguido ya no han vuelto") + " en más de un mes.",
				Bars:     bars,
				Takeaway: "Un mensaje a tiempo recupera a muchos de ellos, y recuperar un cliente cuesta mucho menos que conseguir uno nuevo."}, lvl, txt), nil)
	}
	a.add("comentario", "info", "%d clientes identificados compraron en los últimos 4 meses.", len(by))
}

func leads(ctx context.Context, src Sources, a *analysis) {
	if src.Odoo == nil {
		return
	}
	if !src.Odoo.HasModel(ctx, "crm.lead") {
		missingModule(a, "Activa el seguimiento de prospectos", "crm", "CRM",
			"Tu ERP todavía no guarda a las personas interesadas en comprarte. Al aceptar, AndRho activa el CRM en tu ERP y empieza a ordenar tus prospectos por ti.",
			"Con el CRM activo, AndRho te dice a quién escribirle hoy antes de que el interés se enfríe.")
		return
	}
	var ls []map[string]any
	if err := src.Odoo.SearchRead(ctx, "crm.lead", []any{[]any{"active", "=", true}}, []string{"name", "expected_revenue", "write_date", "stage_id"}, "", 2000, &ls); err != nil || len(ls) == 0 {
		return
	}
	a.hasData = true
	total, stale := 0.0, []string{}
	for _, l := range ls {
		total += odoo.Num(l["expected_revenue"])
		if t, err := time.Parse("2006-01-02 15:04:05", odoo.Str(l["write_date"])); err == nil && time.Since(t) > 14*24*time.Hour {
			stale = append(stale, odoo.Str(l["name"]))
		}
	}
	a.add("resumen", "info", "Tienes %d prospectos con Q%.0f en juego.", len(ls), total)
	if len(stale) > 0 {
		a.add("notificacion", "alerta", "%s más de dos semanas sin seguimiento.", count(len(stale), "prospecto lleva", "prospectos llevan"))
		lvl, txt := reading("en tu CRM hoy")
		a.proposeX("Dale seguimiento a tus prospectos", "Llama o escribe hoy a: "+strings.Join(stale[:min(3, len(stale))], ", ")+". Un prospecto sin respuesta se enfría.",
			fmt.Sprintf("%d sin movimiento en 14 días.", len(stale)),
			withConfidence(&Explain{Kind: "proporcion", Unit: "prospectos",
				Headline: count(len(stale), "persona interesada lleva", "personas interesadas llevan") + " más de dos semanas sin que nadie le responda.",
				Bars:     []ExplainBar{{Label: "Sin seguimiento", Value: float64(len(stale)), Highlight: true}, {Label: "Con seguimiento reciente", Value: float64(len(ls) - len(stale))}},
				Takeaway: "Mientras más tiempo pasa sin contacto, menos probable es que compren. Una llamada hoy vale más que tres la próxima semana."}, lvl, txt), nil)
	} else {
		a.add("comentario", "buena", "Todos tus prospectos tienen seguimiento reciente.")
	}
}

// missingModule proposes that AndRho install an Odoo app the section needs:
// the "AndRho runs your ERP, you just accept" pipeline.
func missingModule(a *analysis, title, module, label, body, takeaway string) {
	a.add("notificacion", "info", "Tu ERP todavía no tiene %s activado.", label)
	a.proposeX(title, body, "Módulo de Odoo no instalado: "+module+".",
		withConfidence(&Explain{Kind: "pasos", Headline: "Tu ERP todavía no tiene " + label + ", por eso esta sección está vacía.",
			Takeaway: takeaway}, "alta", "Lo revisamos directamente en tu ERP."),
		&Action{Type: "erp_install", Modules: []string{module}, Label: "Activar " + label})
}

func branches(ctx context.Context, src Sources, a *analysis) {
	if src.Odoo == nil {
		return
	}
	orders := paidOrders(ctx, src.Odoo, time.Now().AddDate(0, 0, -28))
	if len(orders) == 0 {
		return
	}
	a.hasData = true
	today := time.Now().UTC().Format("2006-01-02")
	weekday := time.Now().UTC().Weekday()
	todayBy, sameDay := map[string]float64{}, map[string][]float64{}
	perDay := map[string]map[string]float64{}
	for _, o := range orders {
		t, err := time.Parse("2006-01-02 15:04:05", odoo.Str(o["date_order"]))
		if err != nil {
			continue
		}
		pos := odoo.M2O(o["config_id"])
		day := t.Format("2006-01-02")
		if day == today {
			todayBy[pos] += odoo.Num(o["amount_total"])
		} else if t.Weekday() == weekday {
			if perDay[pos] == nil {
				perDay[pos] = map[string]float64{}
			}
			perDay[pos][day] += odoo.Num(o["amount_total"])
		}
	}
	for pos, days := range perDay {
		for _, v := range days {
			sameDay[pos] = append(sameDay[pos], v)
		}
	}
	total := 0.0
	for _, v := range todayBy {
		total += v
	}
	a.add("resumen", "info", "Hoy van Q%.0f en ventas entre todas tus cajas.", total)
	for pos, hist := range sameDay {
		mean, _ := MeanStd(hist)
		if c, ok := Change(todayBy[pos], mean); ok && c <= -0.4 {
			a.add("notificacion", "alerta", "%s vende hoy %s menos que un %s normal.", pos, pct(-c)[1:], spanishWeekday(weekday))
		}
	}
}

func spanishWeekday(d time.Weekday) string {
	return []string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}[d]
}

// ---------------------------------------------------------------------
// Translation (Claude)
// ---------------------------------------------------------------------

const translateSystem = `Eres AndRho, el analista de datos de una pyme en Guatemala. Recibes hallazgos ya calculados con estadística.
Tu trabajo es explicarlos al dueño del negocio en español claro, cálido y directo, sin tecnicismos ni inventar números.
Nunca menciones términos estadísticos (ANOVA, p, F, Tukey, regresión, eta, significancia, varianza): di qué pasa y qué hacer.
Devuelve {"resumen": "2 o 3 frases con lo más importante", "sugerencias": [{"base": índice (desde 0) de la propuesta en la que se basa o -1, "titulo": "acción concreta en menos de 8 palabras", "cuerpo": "qué hacer y por qué, en 1 o 2 frases", "evidencia": "el dato que la respalda"}]} con 0 a 3 sugerencias basadas solo en los hallazgos y las propuestas.`

func translate(ctx context.Context, client *ai.Client, accountID, section string, a analysis) (string, []Proposal, error) {
	type propIn struct {
		Indice    int    `json:"indice"`
		Titulo    string `json:"titulo"`
		Cuerpo    string `json:"cuerpo"`
		Evidencia string `json:"evidencia"`
	}
	props := make([]propIn, len(a.proposals))
	for i, p := range a.proposals {
		props[i] = propIn{i, p.Title, p.Body, p.Evidence}
	}
	in, _ := json.Marshal(map[string]any{"seccion": section, "hallazgos": a.findings, "propuestas": props})
	var out struct {
		Resumen     string `json:"resumen"`
		Sugerencias []struct {
			Base                      *int `json:"base"`
			Titulo, Cuerpo, Evidencia string
		} `json:"sugerencias"`
	}
	if err := client.JSON(ctx, "insights", accountID, ai.WithContext(translateSystem), string(in), 800, &out); err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(out.Resumen) == "" {
		return "", nil, fmt.Errorf("insights: empty summary")
	}
	res := []Proposal{}
	for _, s := range out.Sugerencias {
		if s.Titulo == "" || s.Cuerpo == "" {
			continue
		}
		p := Proposal{Title: s.Titulo, Body: s.Cuerpo, Evidence: s.Evidencia}
		// The picture, the action and the technical evidence stay those of
		// the computed proposal it rewrites: Claude only words it.
		if s.Base != nil && *s.Base >= 0 && *s.Base < len(a.proposals) {
			src := a.proposals[*s.Base]
			p.Explain, p.Action, p.Evidence = src.Explain, src.Action, src.Evidence
		}
		res = append(res, p)
	}
	return out.Resumen, res, nil
}

// headlineParts is what Inicio summarizes, most important first, by type of
// business: a service business lives on its prospects, not its stock.
func headlineParts(businessType string) []func(context.Context, Sources, *analysis) {
	switch businessType {
	case "servicios":
		return []func(context.Context, Sources, *analysis){leads, customers, web}
	case "restaurantes", "ventas":
		return []func(context.Context, Sources, *analysis){inventory, customers, web}
	case "en_vivo":
		return []func(context.Context, Sources, *analysis){liveSales, inventory, customers}
	}
	return []func(context.Context, Sources, *analysis){web, customers, inventory}
}
