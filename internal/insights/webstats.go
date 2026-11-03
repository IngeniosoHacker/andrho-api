package insights

// The Web section's deeper statistics, run in R on AndRho's VPS (stats/*.R
// via internal/rstats, over the same SSH as the Odoo provisioning) when the
// section itself is opened:
//
//   - stats/web_anova.R: multifactor ANOVA (Type II, partial eta², Tukey,
//     Brown-Forsythe, Kruskal-Wallis cross-check) of engagement — time on
//     site, pages per visit, scroll — by traffic source, device, landing
//     page, weekday/weekend and country; once for all human visits and once
//     for Google visits only (which landing pages Google sends people to
//     that don't hold them).
//   - stats/seo_ranks.R: per-keyword rank trends (slope + p) and a factorial
//     ANOVA of position by on-page factors (keyword in title / h1, length).
//
// Only effects that are significant, non-trivial (partial eta² ≥ 1%) and
// confirmed by the distribution-free test become findings, and each one
// carries its evidence (n, ratio, p) so the AI translation can't inflate it.
// Without R the section keeps its plain Go statistics, plus a rule-based
// week-over-week rank comparison.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const rTimeout = 20 * time.Second

// ---------------------------------------------------------------------
// R output shapes
// ---------------------------------------------------------------------

type anovaEffect struct {
	Term        string  `json:"term"`
	DF          float64 `json:"df"`
	F           float64 `json:"F"`
	P           float64 `json:"p"`
	PartialEta2 float64 `json:"partial_eta2"`
	Significant bool    `json:"significant"`
}

type anovaLevel struct {
	Level string  `json:"level"`
	N     int     `json:"n"`
	Mean  float64 `json:"mean"`
}

type anovaPair struct {
	A     string   `json:"a"`
	B     string   `json:"b"`
	Diff  float64  `json:"diff"`
	Ratio *float64 `json:"ratio"`
	PAdj  float64  `json:"p_adj"`
}

type anovaCell struct {
	Term  string `json:"term"`
	Worst struct {
		Cell string  `json:"cell"`
		N    int     `json:"n"`
		Mean float64 `json:"mean"`
	} `json:"worst"`
	Best struct {
		Cell string  `json:"cell"`
		N    int     `json:"n"`
		Mean float64 `json:"mean"`
	} `json:"best"`
}

type anovaModel struct {
	Outcome   string                  `json:"outcome"`
	Transform string                  `json:"transform"`
	N         int                     `json:"n"`
	Skipped   string                  `json:"skipped"`
	R2        float64                 `json:"r2"`
	Effects   []anovaEffect           `json:"effects"`
	Levels    map[string][]anovaLevel `json:"levels"`
	Posthoc   map[string][]anovaPair  `json:"posthoc"`
	Cells     []anovaCell             `json:"cells"`
	Robust    map[string]*float64     `json:"robust"`
}

type anovaResult struct {
	Analysis string       `json:"analysis"`
	N        int          `json:"n"`
	Models   []anovaModel `json:"models"`
}

// ---------------------------------------------------------------------
// Labels (the panel speaks Spanish to a business owner)
// ---------------------------------------------------------------------

var factorLabel = map[string]string{
	"source": "el origen de la visita", "device": "el dispositivo", "landing": "la página de entrada",
	"daytype": "el día de la semana", "country": "el país",
}

var outcomeLabel = map[string]string{
	"duration_s": "el tiempo que se quedan", "pages": "las páginas que ven", "scroll": "cuánto bajan en cada página",
}

var levelLabel = map[string]string{
	"organic_search": "quienes llegan desde Google", "direct": "quienes entran directo", "referral": "quienes vienen de otros sitios",
	"ai_referral": "quienes vienen de asistentes de IA", "mobile": "celular", "desktop": "computadora", "tablet": "tablet",
	"semana": "entre semana", "fin_de_semana": "fin de semana", "otras": "las demás",
}

func lvl(s string) string {
	parts := strings.Split(s, " × ")
	for i, p := range parts {
		if l, ok := levelLabel[p]; ok {
			parts[i] = l
		}
	}
	return strings.Join(parts, " en ")
}

func fmtP(p float64) string {
	if p < 0.001 {
		return "p<0,001"
	}
	return strings.Replace(fmt.Sprintf("p=%.3f", p), ".", ",", 1)
}

// ---------------------------------------------------------------------
// Engagement ANOVA
// ---------------------------------------------------------------------

type sessionCols struct {
	Source   []string  `json:"source"`
	Device   []string  `json:"device"`
	Landing  []string  `json:"landing"`
	Daytype  []string  `json:"daytype"`
	Country  []string  `json:"country"`
	Duration []float64 `json:"duration_s"`
	Pages    []float64 `json:"pages"`
	Scroll   []float64 `json:"scroll"`
}

func loadSessions(ctx context.Context, src Sources, organicOnly bool) (sessionCols, error) {
	var c sessionCols
	filter := "s.traffic_source_type NOT IN ('ai_crawler', 'search_bot')"
	if organicOnly {
		filter = "s.traffic_source_type = 'organic_search'"
	}
	rows, err := src.Tracker.Query(ctx, `
		SELECT s.traffic_source_type, COALESCE(s.device_type, ''), COALESCE(s.landing_path, '/'),
		       CASE WHEN extract(isodow FROM s.started_at) >= 6 THEN 'fin_de_semana' ELSE 'semana' END,
		       COALESCE(s.country, ''), s.duration_ms / 1000.0, s.pageview_count::float8,
		       COALESCE(p.scroll, 0)::float8
		FROM sessions s
		LEFT JOIN LATERAL (SELECT MAX(max_scroll_percent) AS scroll FROM pageviews WHERE session_id = s.id) p ON true
		WHERE s.site_id = $1 AND s.started_at >= now() - interval '90 days' AND `+filter+`
		  AND s.duration_ms IS NOT NULL AND s.pageview_count > 0
		ORDER BY s.started_at DESC LIMIT 20000`, src.SiteID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var so, de, la, dt, co string
		var du, pa, sc float64
		if rows.Scan(&so, &de, &la, &dt, &co, &du, &pa, &sc) != nil {
			continue
		}
		c.Source, c.Device, c.Landing, c.Daytype, c.Country = append(c.Source, so), append(c.Device, de), append(c.Landing, la), append(c.Daytype, dt), append(c.Country, co)
		c.Duration, c.Pages, c.Scroll = append(c.Duration, du), append(c.Pages, pa), append(c.Scroll, sc)
	}
	return c, rows.Err()
}

func runAnova(ctx context.Context, src Sources, analysis string, organicOnly bool) (*anovaResult, error) {
	cols, err := loadSessions(ctx, src, organicOnly)
	if err != nil || len(cols.Source) < 60 {
		return nil, err
	}
	factors := []string{"source", "device", "landing", "daytype", "country"}
	if organicOnly {
		factors = []string{"landing", "device", "country"}
	}
	minN := 20
	if organicOnly {
		minN = 12
	}
	var out anovaResult
	err = src.Stats.Run(ctx, "web_anova", map[string]any{
		"analysis": analysis, "alpha": 0.05, "min_level_n": minN,
		"factors": factors, "outcomes": []string{"duration_s", "pages", "scroll"}, "data": cols,
	}, &out, rTimeout)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

var outcomeUnit = map[string]string{"duration_s": "segundos en tu sitio", "pages": "páginas por visita", "scroll": "% de la página que ven"}

var outcomeVerb = map[string]string{"duration_s": "se queda", "pages": "ve", "scroll": "baja"}

// anovaExplain is the owner's view of an ANOVA effect: the typical value of
// every level of the factor, the weak one highlighted.
func anovaExplain(m anovaModel, factor, best, worst string, organic bool) *Explain {
	bars := []ExplainBar{}
	var bv, wv float64
	for _, l := range m.Levels[factor] {
		if l.Level == "otras" || l.Level == "desconocido" {
			continue
		}
		v := math.Round(l.Mean*10) / 10
		bars = append(bars, ExplainBar{Label: strings.TrimPrefix(lvl(l.Level), "quienes "), Value: v, Highlight: l.Level == worst})
		if l.Level == best {
			bv = v
		}
		if l.Level == worst {
			wv = v
		}
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Value > bars[j].Value })
	if len(bars) > 6 {
		bars = bars[:6]
	}
	who := "visitas"
	if organic {
		who = "visitas que llegan desde Google"
	}
	unit := outcomeUnit[m.Outcome]
	// "se queda 41 segundos" / "ve 2.3 páginas" / "ve el 60% de la página"
	// Traffic sources read as plural ("quienes llegan desde Google").
	plural := factor == "source"
	amount := func(v float64) string {
		n := map[bool]string{true: "n", false: ""}[plural]
		switch m.Outcome {
		case "duration_s":
			return fmt.Sprintf("se queda%s %.0f segundos", n, v)
		case "pages":
			return fmt.Sprintf("ve%s %.1f páginas", n, v)
		}
		return fmt.Sprintf("ve%s el %.0f%% de cada página", n, v)
	}
	subject := func(level string) string {
		switch factor {
		case "landing":
			return "Quien entra por " + level
		case "device":
			return "Quien te visita en " + lvl(level)
		}
		l := lvl(level)
		return strings.ToUpper(l[:1]) + l[1:]
	}
	head := fmt.Sprintf("%s %s en promedio; %s, %s.", subject(worst), amount(wv), strings.ToLower(subject(best)[:1])+subject(best)[1:], amount(bv))
	take := "Lo que funciona en el grupo de arriba se puede copiar en el de abajo: ahí está la mejora más rápida."
	switch {
	case factor == "device" && worst == "mobile":
		take = "La mayoría te visita desde el celular. Si ahí se van antes, pierdes a la mayor parte de tus posibles clientes."
	case factor == "source" && worst == "organic_search":
		take = "Google te manda gente, pero no encuentra lo que buscaba y se va antes que el resto. Si tus páginas responden justo esa búsqueda, esas visitas se convierten en clientes."
	case factor == "landing" && organic:
		take = "Google ya te manda gente a esa página; si no encuentran lo que buscaban, se van con otro."
	case factor == "landing":
		take = fmt.Sprintf("Las dos páginas reciben visitas, pero %s retiene mucho más. Lo que hace bien se puede copiar en %s.", best, worst)
	}
	l, t := proven(m.N, who)
	return withConfidence(&Explain{Kind: "comparacion", Unit: unit, Headline: head, Bars: bars, Takeaway: take}, l, t)
}

// strongest returns the significant Tukey pair with the biggest gap between
// the best and worst level of a factor (ratio on the outcome's own scale).
func strongest(m anovaModel, factor string) (best, worst string, ratio float64, p float64, ok bool) {
	for _, pr := range m.Posthoc[factor] {
		r := pr.Diff
		if pr.Ratio != nil {
			r = *pr.Ratio
		}
		hi, lo, rr := pr.A, pr.B, r
		if (pr.Ratio != nil && r < 1) || (pr.Ratio == nil && r < 0) {
			hi, lo = pr.B, pr.A
			if pr.Ratio != nil {
				rr = 1 / r
			} else {
				rr = -r
			}
		}
		if hi == "otras" || lo == "otras" || hi == "desconocido" || lo == "desconocido" {
			continue
		}
		if !ok || rr > ratio {
			best, worst, ratio, p, ok = hi, lo, rr, pr.PAdj, true
		}
	}
	return
}

func levelN(m anovaModel, factor, level string) int {
	for _, l := range m.Levels[factor] {
		if l.Level == level {
			return l.N
		}
	}
	return 0
}

// anovaFindings turns one analysis into findings and at most `maxProps` proposals.
func anovaFindings(res *anovaResult, organic bool, a *analysis, maxProps int, seen map[string]bool) {
	props := 0
	for _, m := range res.Models {
		if m.Skipped != "" {
			continue
		}
		effects := append([]anovaEffect(nil), m.Effects...)
		sort.Slice(effects, func(i, j int) bool { return effects[i].PartialEta2 > effects[j].PartialEta2 })
		for _, e := range effects {
			if !e.Significant || e.PartialEta2 < 0.01 || strings.Contains(e.Term, ":") {
				continue
			}
			if kp := m.Robust[e.Term]; kp == nil || *kp >= 0.05 {
				continue // not confirmed without the normality assumption
			}
			best, worst, ratio, p, ok := strongest(m, e.Term)
			if !ok {
				continue
			}
			// Same factor and same weak level = same advice, whichever
			// analysis (all visits or Google only) found it first.
			key := e.Term + "|" + worst
			gap := fmt.Sprintf("%.1f veces", ratio)
			if m.Transform == "none" {
				gap = fmt.Sprintf("%.0f puntos de scroll", ratio)
			}
			scope, sample := "tus visitas", "visitas"
			if organic {
				scope, sample = "las visitas que llegan desde Google", "visitas desde Google"
			}
			evidence := fmt.Sprintf("ANOVA multifactorial (90 días, %d %s): %s explica %.0f%% de la variación en «%s» (F=%.1f, %s); %s vs %s: %s (Tukey %s, n=%d y %d).",
				m.N, sample, factorLabel[e.Term], e.PartialEta2*100, outcomeLabel[m.Outcome], e.F, fmtP(e.P),
				lvl(best), lvl(worst), gap, fmtP(p), levelN(m, e.Term, best), levelN(m, e.Term, worst))
			sev := "info"
			if e.PartialEta2 >= 0.04 {
				sev = "alerta"
			}
			a.add("comentario", sev, "En %s, %s cambia %s: %s menos para %s que para %s.",
				scope, factorLabel[e.Term], outcomeLabel[m.Outcome], gap, lvl(worst), lvl(best))
			if props >= maxProps || seen[key] {
				continue
			}
			seen[key] = true
			ex := anovaExplain(m, e.Term, best, worst, organic)
			switch {
			case e.Term == "landing" && organic:
				a.proposeX("Arregla "+worst+" para quien llega de Google",
					fmt.Sprintf("Google manda gente a %s pero se va antes que en %s. Revisa que el título y el primer párrafo respondan exactamente lo que buscaron (mira sus keywords en Web > Keywords) y pon el siguiente paso a la vista.", worst, best),
					evidence, ex, creatorAction("web"))
				props++
			case e.Term == "landing":
				a.proposeX("Mejora la página "+worst,
					fmt.Sprintf("Quien entra por %s se queda %s menos que quien entra por %s. Copia lo que funciona de %s: lo que muestra arriba, sus enlaces y su llamada a la acción.", worst, gap, best, best),
					evidence, ex, creatorAction("web"))
				props++
			case e.Term == "device" && worst == "mobile":
				a.proposeX("Arregla tu sitio en celular",
					"En celular la gente se va mucho antes que en computadora. Revisa que cargue rápido, que el texto se lea sin hacer zoom y que el botón de contacto se vea sin bajar.",
					evidence, ex, creatorAction("web"))
				props++
			case e.Term == "source" && worst == "organic_search":
				a.proposeX("Lo que buscan en Google no es lo que encuentran",
					"Quien llega desde Google se va antes que el resto: tus páginas aparecen para búsquedas que no responden. Ajusta títulos y contenido a las keywords sugeridas en Web > Keywords.",
					evidence, ex, creatorAction("web"))
				props++
			}
		}
		for _, c := range m.Cells {
			if strings.Contains(c.Worst.Cell, "otras") || c.Worst.N < 10 {
				continue
			}
			ratio := 0.0
			if c.Worst.Mean > 0 {
				ratio = c.Best.Mean / c.Worst.Mean
			}
			if ratio < 1.5 || m.Transform == "none" {
				continue
			}
			a.add("notificacion", "alerta", "La combinación que peor funciona en %s es %s (%d visitas): %.1f veces menos que %s.",
				outcomeLabel[m.Outcome], lvl(c.Worst.Cell), c.Worst.N, ratio, lvl(c.Best.Cell))
		}
	}
}

// ---------------------------------------------------------------------
// Rank tracking (seo_* tables written by WebTracker + the dashboard)
// ---------------------------------------------------------------------

type rankSeries struct {
	Keyword   string    `json:"keyword"`
	Days      []float64 `json:"days"`
	Positions []float64 `json:"positions"`
}

type rankTrend struct {
	Keyword      string  `json:"keyword"`
	N            int     `json:"n"`
	SlopePerWeek float64 `json:"slope_per_week"`
	P            float64 `json:"p"`
	First        float64 `json:"first"`
	Last         float64 `json:"last"`
	Significant  bool    `json:"significant"`
}

type rankFactor struct {
	Term        string   `json:"term"`
	F           float64  `json:"F"`
	P           float64  `json:"p"`
	PartialEta2 float64  `json:"partial_eta2"`
	Significant bool     `json:"significant"`
	KruskalP    *float64 `json:"kruskal_p"`
	Levels      []struct {
		Level           string  `json:"level"`
		N               int     `json:"n"`
		GeoMeanPosition float64 `json:"geo_mean_position"`
	} `json:"levels"`
}

type rankResult struct {
	Trends  []rankTrend `json:"trends"`
	Factors *struct {
		N       int          `json:"n"`
		Effects []rankFactor `json:"effects"`
	} `json:"factors"`
}

var accents = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n")

func foldText(s string) string {
	return accents.Replace(strings.ToLower(strings.Join(strings.Fields(s), " ")))
}

type rankedKeyword struct {
	keyword, title, h1 string
	position, words    float64
}

func seoRanks(ctx context.Context, src Sources, a *analysis) {
	rows, err := src.Tracker.Query(ctx, `
		SELECT c.keyword, (EXTRACT(EPOCH FROM date_trunc('day', c.checked_at) - MIN(date_trunc('day', c.checked_at)) OVER (PARTITION BY c.keyword)) / 86400)::float8,
		       MIN(c.position)::float8
		FROM seo_rank_checks c JOIN seo_keywords k ON k.site_id = c.site_id AND k.keyword = c.keyword AND k.tracked
		WHERE c.site_id = $1 AND c.error IS NULL AND c.position IS NOT NULL AND c.checked_at >= now() - interval '60 days'
		GROUP BY c.keyword, date_trunc('day', c.checked_at)
		ORDER BY 1, 2`, src.SiteID)
	if err != nil {
		return // tables not migrated yet, or no SEO data
	}
	byKw := map[string]*rankSeries{}
	order := []string{}
	for rows.Next() {
		var kw string
		var d, p float64
		if rows.Scan(&kw, &d, &p) != nil {
			continue
		}
		if byKw[kw] == nil {
			byKw[kw] = &rankSeries{Keyword: kw}
			order = append(order, kw)
		}
		byKw[kw].Days = append(byKw[kw].Days, d)
		byKw[kw].Positions = append(byKw[kw].Positions, p)
	}
	rows.Close()
	if len(order) == 0 {
		return
	}
	a.hasData = true
	all := make([]rankSeries, 0, len(order))
	for _, k := range order {
		all = append(all, *byKw[k])
	}

	// Latest position + the ranking page's on-page facts, for the factor model.
	var ranked []rankedKeyword
	prow, err := src.Tracker.Query(ctx, `
		WITH last AS (
		  SELECT DISTINCT ON (keyword) keyword, position, split_part(regexp_replace(url, '^https?://[^/]+', ''), '?', 1) AS path
		  FROM seo_rank_checks WHERE site_id = $1 AND error IS NULL AND position IS NOT NULL
		  ORDER BY keyword, checked_at DESC)
		SELECT l.keyword, l.position::float8, COALESCE(pv.title, ''), COALESCE(pv.seo_meta->>'h1', ''), COALESCE(pv.content_word_count, 0)::float8
		FROM last l
		LEFT JOIN LATERAL (SELECT title, seo_meta, content_word_count FROM pageviews
		                   WHERE site_id = $1 AND path = COALESCE(NULLIF(l.path, ''), '/') ORDER BY entered_at DESC LIMIT 1) pv ON true`, src.SiteID)
	if err == nil {
		for prow.Next() {
			var r rankedKeyword
			if prow.Scan(&r.keyword, &r.position, &r.title, &r.h1, &r.words) == nil && r.title != "" {
				ranked = append(ranked, r)
			}
		}
		prow.Close()
	}

	if !src.Stats.Available() {
		rankFallback(all, a)
		return
	}
	pages := map[string][]any{"keyword": {}, "position": {}, "in_title": {}, "in_h1": {}, "words": {}}
	for _, r := range ranked {
		k := foldText(r.keyword)
		pages["keyword"] = append(pages["keyword"], r.keyword)
		pages["position"] = append(pages["position"], r.position)
		pages["in_title"] = append(pages["in_title"], strings.Contains(foldText(r.title), k))
		pages["in_h1"] = append(pages["in_h1"], strings.Contains(foldText(r.h1), k))
		pages["words"] = append(pages["words"], r.words)
	}
	var out rankResult
	if err := src.Stats.Run(ctx, "seo_ranks", map[string]any{"alpha": 0.05, "series": all, "pages": pages}, &out, rTimeout); err != nil {
		rankFallback(all, a)
		return
	}

	var up, down []rankTrend
	for _, t := range out.Trends {
		if !t.Significant || math.Abs(t.SlopePerWeek) < 0.5 {
			continue
		}
		if t.SlopePerWeek < 0 {
			up = append(up, t)
		} else {
			down = append(down, t)
		}
	}
	sort.Slice(down, func(i, j int) bool { return down[i].SlopePerWeek > down[j].SlopePerWeek })
	sort.Slice(up, func(i, j int) bool { return up[i].SlopePerWeek < up[j].SlopePerWeek })
	for i, t := range down {
		if i == 3 {
			break
		}
		a.add("notificacion", "alerta", "\"%s\" viene cayendo en Google semana a semana: pasó del lugar %.0f al %.0f.", t.Keyword, t.First, t.Last)
	}
	if len(down) > 0 {
		t := down[0]
		series := []ExplainPoint{}
		for _, k := range all {
			if k.Keyword != t.Keyword {
				continue
			}
			start := time.Now().AddDate(0, 0, -int(k.Days[len(k.Days)-1]))
			for i, d := range k.Days {
				series = append(series, ExplainPoint{Label: start.AddDate(0, 0, int(d)).Format("02/01"), Value: k.Positions[i]})
			}
		}
		lvl, txt := proven(t.N, "revisiones diarias de Google")
		a.proposeX("Recupera \""+t.Keyword+"\" en Google",
			"Esta búsqueda baja semana a semana, no es un mal día. Actualiza la página que aparece (fecha, datos, una sección nueva) y revisa en Web > Competencia quién te está pasando.",
			fmt.Sprintf("Regresión de posición en %d revisiones: %+.1f posiciones/semana, %s.", t.N, t.SlopePerWeek, fmtP(t.P)),
			withConfidence(&Explain{Kind: "tendencia", LowerIsBetter: true, Unit: "lugar en Google",
				Headline: fmt.Sprintf("Cuando alguien busca \"%s\", tu sitio aparece cada vez más abajo: del lugar %.0f al %.0f.", t.Keyword, t.First, t.Last),
				Series:   series,
				Takeaway: "Casi nadie pasa de la primera página de Google. Si sigues bajando a este ritmo, esta búsqueda dejará de traerte clientes."}, lvl, txt),
			creatorAction("web"))
	}
	for i, t := range up {
		if i == 2 {
			break
		}
		a.add("notificacion", "buena", "\"%s\" sube en Google semana a semana: ya estás en el lugar %.0f.", t.Keyword, t.Last)
	}

	if out.Factors == nil {
		return
	}
	for _, e := range out.Factors.Effects {
		if e.Term != "in_title" || !e.Significant || e.KruskalP == nil || *e.KruskalP >= 0.05 || len(e.Levels) != 2 {
			continue
		}
		var yes, no float64
		for _, l := range e.Levels {
			if l.Level == "sí" {
				yes = l.GeoMeanPosition
			} else {
				no = l.GeoMeanPosition
			}
		}
		if yes == 0 || no <= yes {
			continue
		}
		missing := []string{}
		for _, r := range ranked {
			if !strings.Contains(foldText(r.title), foldText(r.keyword)) {
				missing = append(missing, "\""+r.keyword+"\"")
			}
		}
		a.add("comentario", "info", "En tu sitio, las páginas con la keyword en el título aparecen en promedio en la posición %.0f; sin ella, en la %.0f.", yes, no)
		if len(missing) > 0 {
			if len(missing) > 4 {
				missing = append(missing[:4], "…")
			}
			lvl, txt := proven(out.Factors.N, "búsquedas de tu propio sitio")
			a.proposeX("Pon cada keyword en el título de su página",
				"En tu propio sitio, las páginas que llevan la búsqueda en el título están mucho más arriba en Google. Faltan: "+strings.Join(missing, ", ")+".",
				fmt.Sprintf("ANOVA factorial de la posición sobre %d keywords: título con keyword ~#%.0f vs sin ella ~#%.0f (F=%.1f, %s, η² parcial %.0f%%).",
					out.Factors.N, yes, no, e.F, fmtP(e.P), e.PartialEta2*100),
				withConfidence(&Explain{Kind: "comparacion", LowerIsBetter: true, Unit: "lugar promedio en Google",
					Headline: fmt.Sprintf("En tu sitio, las páginas que dicen la búsqueda en su título aparecen en el lugar %.0f; las que no, en el %.0f.", yes, no),
					Bars:     []ExplainBar{{Label: "Con la búsqueda en el título", Value: math.Round(yes)}, {Label: "Sin ella", Value: math.Round(no), Highlight: true}},
					Takeaway: "El título es lo primero que Google lee. Decir en él exactamente lo que la gente busca es el cambio más barato para subir."}, lvl, txt),
				creatorAction("web"))
		}
	}
}

// Without R: latest vs a week earlier, no significance claims.
func rankFallback(list []rankSeries, a *analysis) {
	better, worse := 0, 0
	for _, k := range list {
		n := len(k.Positions)
		if n < 2 {
			continue
		}
		last, lastDay := k.Positions[n-1], k.Days[n-1]
		for i := n - 2; i >= 0; i-- {
			if lastDay-k.Days[i] >= 7 {
				if last < k.Positions[i] {
					better++
				} else if last > k.Positions[i] {
					worse++
				}
				break
			}
		}
	}
	if better+worse > 0 {
		a.add("comentario", "info", "Esta semana %d de tus keywords subieron en Google y %d bajaron.", better, worse)
	}
}

// webDeep runs the R analyses for the Web section.
func webDeep(ctx context.Context, src Sources, a *analysis) {
	if src.Tracker == nil || src.SiteID == "" {
		return
	}
	seoRanks(ctx, src, a)
	if !src.Stats.Available() {
		return
	}
	seen := map[string]bool{}
	// Google visits first: "Google sends people to X and they leave" is the
	// more specific advice for the same weak page.
	if res, err := runAnova(ctx, src, "organico", true); err == nil && res != nil {
		a.hasData = true
		anovaFindings(res, true, a, 1, seen)
	}
	if res, err := runAnova(ctx, src, "todas", false); err == nil && res != nil {
		a.hasData = true
		anovaFindings(res, false, a, 2, seen)
	}
}
