package insights

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

// liveSales reads the ERP's live sales (andrho_live_sale): how much of
// what is shown sells, what doesn't, and the hour that sells best. The
// words are the owner's: no statistics in the text (they go in Evidence).
func liveSales(ctx context.Context, src Sources, a *analysis) {
	if src.Odoo == nil || !src.Odoo.HasModel(ctx, "andrho.live.sale") {
		return
	}
	var lives []map[string]any
	if err := src.Odoo.SearchRead(ctx, "andrho.live.sale", []any{[]any{"state", "=", "done"}},
		[]string{"name", "started_at", "date", "item_count", "sold_count", "unsold_count", "revenue"}, "date desc", 20, &lives); err != nil || len(lives) == 0 {
		return
	}
	a.hasData = true
	var items, sold, unsold int
	var revenue float64
	byHour := map[int][]float64{}
	for _, l := range lives {
		items += int(odoo.Num(l["item_count"]))
		sold += int(odoo.Num(l["sold_count"]))
		unsold += int(odoo.Num(l["unsold_count"]))
		revenue += odoo.Num(l["revenue"])
		when := odoo.Str(l["started_at"])
		if when == "" {
			when = odoo.Str(l["date"])
		}
		if t, err := time.Parse("2006-01-02 15:04:05", when); err == nil {
			h := t.Add(-6 * time.Hour).Hour() // Guatemala, UTC-6
			byHour[h] = append(byHour[h], odoo.Num(l["revenue"]))
		}
	}
	last := lives[0]
	a.add("resumen", "info", "En tus últimos %s vendiste Q%.0f: %d de %d artículos.", count(len(lives), "live", "lives"), revenue, sold, items)
	if lastItems := int(odoo.Num(last["item_count"])); lastItems > 0 {
		a.add("comentario", "info", "En tu último live (%s) vendiste %d de %d artículos, Q%.0f.", odoo.Str(last["name"]),
			int(odoo.Num(last["sold_count"])), lastItems, odoo.Num(last["revenue"]))
	}
	if items > 0 {
		through := float64(sold) / float64(items)
		switch {
		case through >= 0.8:
			a.add("notificacion", "buena", "Vendes casi todo lo que muestras: puedes llevar más artículos a tu próximo live.")
			a.propose("Lleva más artículos al próximo live",
				"Se te está acabando la mercancía antes que el público. Prueba con más artículos de los que más se vendieron.",
				fmt.Sprintf("Vendidos %d de %d artículos en %d lives (%.0f%%).", sold, items, len(lives), through*100))
		case through < 0.5 && unsold > 0:
			a.add("notificacion", "alerta", "Más de la mitad de lo que muestras no se vende en el live.")
			a.propose("Publica lo que no se vendió",
				"Después de cada live, publica lo que quedó: AndRho sube una publicación por artículo y quien comente “Mío” primero se lo lleva.",
				fmt.Sprintf("Sin vender %d de %d artículos en %d lives.", unsold, items, len(lives)))
		}
	}
	if len(byHour) >= 2 {
		type hr struct {
			h   int
			avg float64
			n   int
		}
		hours := []hr{}
		for h, v := range byHour {
			sum := 0.0
			for _, x := range v {
				sum += x
			}
			hours = append(hours, hr{h, sum / float64(len(v)), len(v)})
		}
		sort.Slice(hours, func(i, j int) bool { return hours[i].avg > hours[j].avg })
		best, worst := hours[0], hours[len(hours)-1]
		if best.avg > 0 && worst.avg < best.avg*0.7 {
			a.propose(fmt.Sprintf("Haz tus lives a las %d:00", best.h),
				fmt.Sprintf("A esa hora vendes más en cada live que a las %d:00.", worst.h),
				fmt.Sprintf("Promedio por live: Q%.0f a las %d:00 (%d lives) contra Q%.0f a las %d:00 (%d lives).", best.avg, best.h, best.n, worst.avg, worst.h, worst.n))
		}
	}
}
