package plans

import "fmt"

// Budget is a range of what a business can put into software each month
// today (the signup asks it with these options). The ranges are cut at the
// catalog's prices, so each one says which plans fit: "Q350 a Q749" pays
// Plataforma but not Despegue.
type Budget struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Min   int    `json:"min"` // quetzales al mes, inclusive
	Max   int    `json:"max"` // exclusive; 0 = no top
}

// Budgets are the ranges, ascending, from the catalog's prices.
func Budgets() []Budget {
	out := []Budget{}
	lo := 0
	for _, p := range catalog {
		id := fmt.Sprintf("hasta_%d", p.Price)
		label := fmt.Sprintf("Menos de %s", FormatQ(p.Price))
		if lo > 0 {
			id = fmt.Sprintf("%d_%d", lo, p.Price)
			label = fmt.Sprintf("De %s a %s", FormatQ(lo), FormatQ(p.Price-1))
		}
		out = append(out, Budget{ID: id, Label: label, Min: lo, Max: p.Price})
		lo = p.Price
	}
	return append(out, Budget{ID: fmt.Sprintf("desde_%d", lo), Label: fmt.Sprintf("%s o más", FormatQ(lo)), Min: lo})
}

// GetBudget finds a range by id.
func GetBudget(id string) (Budget, bool) {
	for _, b := range Budgets() {
		if b.ID == id {
			return b, true
		}
	}
	return Budget{}, false
}
