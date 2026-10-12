// Package etl migrates a business' data from its previous system into its
// AndRho ERP (Odoo). The dashboard extracts (reads the CSV the business
// exported, maps its columns) and posts rows; Transform normalizes and
// validates them (amounts like "Q1,250.50", duplicates, missing names); Load
// creates or updates the records in Odoo. A dry run stops after Transform.
package etl

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

// Kinds that can be migrated, and their fields.
var Fields = map[string][]string{
	"products":  {"name", "code", "price", "cost", "qty", "category", "barcode"},
	"customers": {"name", "email", "phone", "vat", "street", "city", "company"},
}

// MaxRows is the most rows one run takes.
const MaxRows = 5000

// Issue is a problem with one row (1-based, as in the file after the header).
type Issue struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// Record is one clean row.
type Record struct {
	Row    int
	Values map[string]string
	Nums   map[string]float64
}

// Report is what a run did (or would do).
type Report struct {
	Kind    string           `json:"kind"`
	DryRun  bool             `json:"dry_run"`
	Rows    int              `json:"rows"`
	Valid   int              `json:"valid"`
	Created int              `json:"created"`
	Updated int              `json:"updated"`
	Skipped int              `json:"skipped"`
	Issues  []Issue          `json:"issues"`
	Preview []map[string]any `json:"preview"`
}

var amountJunk = regexp.MustCompile(`[^0-9,.\-]`)

// ParseAmount reads "Q1,250.50", "1.250,50", "$ 12" or "12" as a number.
// The last separator followed by 1–2 digits is the decimal one.
func ParseAmount(s string) (float64, bool) {
	s = amountJunk.ReplaceAllString(strings.TrimSpace(s), "")
	if s == "" || s == "-" {
		return 0, false
	}
	lastDot, lastComma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	dec := lastDot
	if lastComma > lastDot {
		dec = lastComma
	}
	if dec >= 0 && len(s)-dec-1 > 2 {
		dec = -1 // "1,250" or "1.250": thousands
	}
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '.' || r == ',':
			if i == dec {
				b.WriteRune('.')
			}
		default:
			b.WriteRune(r)
		}
	}
	f, err := strconv.ParseFloat(b.String(), 64)
	return f, err == nil
}

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var numeric = map[string]bool{"price": true, "cost": true, "qty": true}

var label = map[string]string{"price": "el precio", "cost": "el costo", "qty": "la existencia"}

// Transform cleans rows of kind. Rows without a name, with bad numbers or
// emails become issues; duplicates (same code, or same name without code;
// same email for customers) keep the last one.
func Transform(kind string, rows []map[string]string) ([]Record, []Issue) {
	var out []Record
	issues := []Issue{}
	seen := map[string]int{} // key -> index in out
	for i, raw := range rows {
		n := i + 1
		r := Record{Row: n, Values: map[string]string{}, Nums: map[string]float64{}}
		bad := false
		for _, f := range Fields[kind] {
			v := strings.Join(strings.Fields(raw[f]), " ")
			if v == "" {
				continue
			}
			if len(v) > 300 {
				v = v[:300]
			}
			if numeric[f] {
				num, ok := ParseAmount(v)
				if !ok || num < 0 {
					issues = append(issues, Issue{n, fmt.Sprintf("%s no es un número: %q", label[f], v)})
					bad = true
					continue
				}
				r.Nums[f] = num
				continue
			}
			r.Values[f] = v
		}
		if r.Values["name"] == "" {
			issues = append(issues, Issue{n, "falta el nombre"})
			continue
		}
		if e := r.Values["email"]; e != "" {
			if !emailRe.MatchString(e) {
				issues = append(issues, Issue{n, fmt.Sprintf("correo inválido: %q", e)})
				bad = true
			} else {
				r.Values["email"] = strings.ToLower(e)
			}
		}
		if bad {
			continue
		}
		key := strings.ToLower(r.Values["name"])
		if kind == "products" && r.Values["code"] != "" {
			key = "code:" + strings.ToLower(r.Values["code"])
		}
		if kind == "customers" && r.Values["email"] != "" {
			key = "email:" + r.Values["email"]
		}
		if at, dup := seen[key]; dup {
			issues = append(issues, Issue{n, fmt.Sprintf("repetido con la fila %d: se usa esta", out[at].Row)})
			out[at] = r
			continue
		}
		seen[key] = len(out)
		out = append(out, r)
	}
	return out, issues
}

// Load writes records into Odoo, matching existing ones (products by
// internal code, then name; customers by email, then name).
func Load(ctx context.Context, cl *odoo.Client, kind string, recs []Record, rep *Report) error {
	for _, r := range recs {
		var err error
		if kind == "products" {
			err = loadProduct(ctx, cl, r, rep)
		} else {
			err = loadCustomer(ctx, cl, r, rep)
		}
		if err != nil {
			rep.Skipped++
			rep.Issues = append(rep.Issues, Issue{r.Row, err.Error()})
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func findID(ctx context.Context, cl *odoo.Client, model string, domain []any) (int, error) {
	var ids []int
	if err := cl.Exec(ctx, model, "search", []any{domain}, map[string]any{"limit": 1}, &ids); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return ids[0], nil
}

func loadProduct(ctx context.Context, cl *odoo.Client, r Record, rep *Report) error {
	v := r.Values
	vals := map[string]any{"name": v["name"], "type": "consu", "is_storable": true}
	if v["code"] != "" {
		vals["default_code"] = v["code"]
	}
	if v["barcode"] != "" {
		vals["barcode"] = v["barcode"]
	}
	if n, ok := r.Nums["price"]; ok {
		vals["list_price"] = n
	}
	if n, ok := r.Nums["cost"]; ok {
		vals["standard_price"] = n
	}
	if c := v["category"]; c != "" {
		cat, err := findID(ctx, cl, "product.category", []any{[]any{"name", "=", c}})
		if err != nil {
			return err
		}
		if cat == 0 {
			if err := cl.Exec(ctx, "product.category", "create", []any{map[string]any{"name": c}}, nil, &cat); err != nil {
				return err
			}
		}
		vals["categ_id"] = cat
	}
	domain := []any{[]any{"name", "=", v["name"]}}
	if v["code"] != "" {
		domain = []any{[]any{"default_code", "=", v["code"]}}
	}
	id, err := findID(ctx, cl, "product.product", domain)
	if err != nil {
		return err
	}
	if id > 0 {
		if err := cl.Exec(ctx, "product.product", "write", []any{[]int{id}, vals}, nil, nil); err != nil {
			return err
		}
		rep.Updated++
	} else {
		if err := cl.Exec(ctx, "product.product", "create", []any{vals}, nil, &id); err != nil {
			return err
		}
		rep.Created++
	}
	if qty, ok := r.Nums["qty"]; ok {
		return setStock(ctx, cl, id, qty)
	}
	return nil
}

// setStock counts qty in the main warehouse (an inventory adjustment).
func setStock(ctx context.Context, cl *odoo.Client, productID int, qty float64) error {
	var wh []map[string]any
	if err := cl.SearchRead(ctx, "stock.warehouse", nil, []string{"lot_stock_id"}, "id", 1, &wh); err != nil || len(wh) == 0 {
		return fmt.Errorf("sin bodega para la existencia")
	}
	loc := wh[0]["lot_stock_id"].([]any)[0]
	var quant int
	if err := cl.Exec(ctx, "stock.quant", "create", []any{map[string]any{"product_id": productID, "location_id": loc, "inventory_quantity": qty}}, nil, &quant); err != nil {
		return err
	}
	return cl.Exec(ctx, "stock.quant", "action_apply_inventory", []any{[]int{quant}}, nil, nil)
}

func loadCustomer(ctx context.Context, cl *odoo.Client, r Record, rep *Report) error {
	v := r.Values
	vals := map[string]any{"name": v["name"], "customer_rank": 1}
	for f, of := range map[string]string{"email": "email", "phone": "phone", "vat": "vat", "street": "street", "city": "city"} {
		if v[f] != "" {
			vals[of] = v[f]
		}
	}
	if v["company"] != "" {
		vals["company_name"] = v["company"]
	}
	domain := []any{[]any{"name", "=", v["name"]}}
	if v["email"] != "" {
		domain = []any{[]any{"email", "=ilike", v["email"]}}
	}
	id, err := findID(ctx, cl, "res.partner", domain)
	if err != nil {
		return err
	}
	if id > 0 {
		rep.Updated++
		return cl.Exec(ctx, "res.partner", "write", []any{[]int{id}, vals}, nil, nil)
	}
	rep.Created++
	return cl.Exec(ctx, "res.partner", "create", []any{vals}, nil, &id)
}
