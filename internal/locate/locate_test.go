package locate

import "testing"

func top(q string, has func(string) bool) string {
	r := Search(q, has, 5)
	if len(r) == 0 {
		return ""
	}
	return r[0].Key
}

func TestSearch(t *testing.T) {
	cases := map[string]string{
		"inventario":    "inventory",
		"productos":     "products",
		"Mercancía":     "products",
		"live":          "live",
		"en vivo":       "live",
		"tarjetas":      "live_cards",
		"pedidos":       "live_cards",
		"clientas":      "live_buyers",
		"prodcutos":     "", // two letters swapped: distance 2, no match
		"inventaro":     "inventory",
		"conteo":        "stock_count",
		"pacas":         "receipts",
		"organizar":     "d_envivo",
		"configuracion": "d_config",
	}
	for q, want := range cases {
		if got := top(q, nil); got != want {
			t.Errorf("%q -> %q, want %q", q, got, want)
		}
	}
	if r := Search("   ", nil, 5); len(r) != 0 {
		t.Fatal("empty query")
	}
	// Apps that aren't installed are left out.
	noSales := func(m string) bool { return m != "sale.order" }
	if top("ventas", noSales) == "sales" {
		t.Fatal("sale.order isn't installed")
	}
	if top("ventas", nil) != "sales" {
		t.Fatal("ventas")
	}
}

func TestValidERPPath(t *testing.T) {
	for _, p := range []string{"/odoo/action-stock.product_template_action_product", "/report/pdf/x/1"} {
		if !ValidERPPath(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	for _, p := range []string{"//evil.com", "https://evil.com", "/web/login", "/odoo\\..", "", "/odoo x"} {
		if ValidERPPath(p) {
			t.Errorf("%q should be invalid", p)
		}
	}
}
