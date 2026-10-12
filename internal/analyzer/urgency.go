package analyzer

import "sort"

// Urgency ranks what AndRho should build first from what businesses tell
// us: the areas they ask for in the waiting list (modules), what the
// Analyzer suggested to them (suggested_modules), the pains that point to a
// module, and what accounts actually switched on at signup (weighted most:
// they're already on board).

// Priority is one module's place in the ranking.
type Priority struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Share     int     `json:"share"` // % of the top score
	Requested int     `json:"requested"`
	Suggested int     `json:"suggested"`
	Pains     int     `json:"pains"`
	Accounts  int     `json:"accounts"`
}

// painModules: which module answers each pain point.
var painModules = map[string]string{
	"datos_dispersos": "unificacion", "no_me_dice": "analitica", "reportes_tarde": "analitica",
	"pierdo_control": "inventario", "sin_sistema": "erp", "no_conecta": "unificacion",
}

// Urgency ranks modules. Each map is value -> count.
func Urgency(requested, suggested, pains, accounts map[string]int) []Priority {
	by := map[string]*Priority{}
	get := func(k string) *Priority {
		if by[k] == nil {
			name := k
			if m, ok := Modules[k]; ok {
				name = m.Name
			}
			by[k] = &Priority{Key: k, Name: name}
		}
		return by[k]
	}
	for k, n := range requested {
		get(k).Requested += n
	}
	for k, n := range suggested {
		get(k).Suggested += n
	}
	for pain, n := range pains {
		if k, ok := painModules[pain]; ok {
			get(k).Pains += n
		}
	}
	for k, n := range accounts {
		get(k).Accounts += n
	}
	out := []Priority{}
	top := 0.0
	for _, p := range by {
		if p.Key == "creators" {
			continue
		}
		p.Score = float64(p.Requested) + 0.5*float64(p.Suggested) + 0.5*float64(p.Pains) + 1.5*float64(p.Accounts)
		if p.Score > top {
			top = p.Score
		}
		out = append(out, *p)
	}
	for i := range out {
		if top > 0 {
			out[i].Share = int(out[i].Score / top * 100)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Key < out[j].Key
	})
	return out
}
