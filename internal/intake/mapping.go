package intake

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// The analysis: which Odoo model a table belongs to and which of its
// columns is which Odoo field. OLAP-Data-lakehouse's AnalyzeColumnsWithLLM
// told dimensions from facts by column names ("name", "category" →
// dimension; "amount", "price", "quantity" → fact); here the same idea,
// with Spanish and English synonyms and a look at the values, scores each
// table against the structures of Odoo and keeps the dimension/fact split
// for the columns no field took (for the Creator who reviews it).

// Field is one field of a target, as internal/etl names it.
type Field struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Synonyms []string `json:"-"`
	Weight   float64  `json:"-"`
}

// Target is an Odoo structure a table can be loaded into.
type Target struct {
	Kind   string  `json:"kind"`
	Model  string  `json:"model"`
	Label  string  `json:"label"`
	Load   bool    `json:"load"` // internal/etl loads it on its own
	Fields []Field `json:"fields"`
	hints  []string
}

var (
	fName    = Field{"name", "Nombre", []string{"nombre", "name", "descripcion", "nombre completo", "full name", "razon social", "titulo", "title"}, 1}
	fEmail   = Field{"email", "Correo", []string{"correo", "email", "e mail", "mail", "correo electronico"}, 1.5}
	fPhone   = Field{"phone", "Teléfono", []string{"telefono", "tel", "celular", "movil", "whatsapp", "phone", "mobile", "cel"}, 1.5}
	fVat     = Field{"vat", "NIT", []string{"nit", "vat", "rtu", "rfc", "ruc", "cuit", "tax id", "nit cliente"}, 1.5}
	fStreet  = Field{"street", "Dirección", []string{"direccion", "address", "calle", "domicilio", "direccion fiscal"}, 1.5}
	fCity    = Field{"city", "Ciudad", []string{"ciudad", "municipio", "city", "localidad", "poblacion"}, 1.5}
	fCompany = Field{"company", "Empresa", []string{"empresa", "compania", "company", "organizacion", "negocio"}, 1}
)

func partnerFields(extra ...string) []Field {
	n := fName
	n.Synonyms = append(append([]string{}, fName.Synonyms...), extra...)
	return []Field{n, fEmail, fPhone, fVat, fStreet, fCity, fCompany}
}

// Targets, in tie-break order.
var Targets = []Target{
	{Kind: "products", Model: "product.product", Label: "Productos", Load: true, hints: []string{"producto", "productos", "articulo", "articulos", "item", "items", "inventario", "catalogo", "stock", "product", "products", "existencias", "mercaderia"},
		Fields: []Field{
			{"name", "Nombre", []string{"nombre", "producto", "descripcion", "articulo", "name", "item", "nombre producto", "nombre del producto", "product", "product name", "descripcion producto"}, 1},
			{"code", "Código", []string{"codigo", "sku", "code", "referencia", "ref", "clave", "codigo interno", "item code", "id producto", "codigo producto"}, 2},
			{"price", "Precio de venta", []string{"precio", "precio venta", "precio de venta", "price", "pvp", "precio publico", "sale price", "precio unitario", "unit price", "list price"}, 2},
			{"cost", "Costo", []string{"costo", "cost", "precio compra", "precio de compra", "costo unitario", "unit cost", "standard price"}, 2},
			{"qty", "Existencia", []string{"existencia", "existencias", "stock", "cantidad", "qty", "inventario", "disponible", "on hand", "unidades", "quantity"}, 2},
			{"category", "Categoría", []string{"categoria", "category", "familia", "linea", "grupo", "tipo de producto", "clase"}, 2},
			{"barcode", "Código de barras", []string{"codigo de barras", "barras", "barcode", "ean", "upc", "gtin"}, 2},
		}},
	{Kind: "customers", Model: "res.partner", Label: "Clientes", Load: true, hints: []string{"cliente", "clientes", "customer", "customers", "contacto", "contactos", "partner", "partners", "compradores"},
		Fields: partnerFields("cliente", "nombre cliente", "nombre del cliente", "customer", "customer name")},
	{Kind: "suppliers", Model: "res.partner", Label: "Proveedores", Load: true, hints: []string{"proveedor", "proveedores", "supplier", "suppliers", "vendor", "vendors"},
		Fields: partnerFields("proveedor", "nombre proveedor", "nombre del proveedor", "supplier", "vendor")},
	{Kind: "sales", Model: "sale.order", Label: "Ventas", hints: []string{"venta", "ventas", "factura", "facturas", "pedido", "pedidos", "orden", "ordenes", "order", "orders", "sale", "sales", "invoice", "invoices", "ticket", "tickets"},
		Fields: []Field{
			{"date", "Fecha", []string{"fecha", "date", "fecha venta", "fecha de venta", "fecha factura", "dia", "created", "fecha pedido"}, 2},
			{"number", "Número", []string{"factura", "numero", "no", "folio", "documento", "no documento", "invoice", "order", "pedido", "numero factura", "serie"}, 1},
			{"customer", "Cliente", []string{"cliente", "customer", "comprador", "nombre cliente"}, 1},
			{"product", "Producto", []string{"producto", "articulo", "item", "product", "descripcion"}, 1},
			{"qty", "Cantidad", []string{"cantidad", "qty", "unidades", "quantity"}, 1},
			{"price", "Precio unitario", []string{"precio", "precio unitario", "unit price", "price"}, 1},
			{"total", "Total", []string{"total", "monto", "importe", "subtotal", "amount", "valor", "venta"}, 2},
		}},
	{Kind: "employees", Model: "hr.employee", Label: "Personal", hints: []string{"empleado", "empleados", "colaborador", "colaboradores", "personal", "planilla", "nomina", "staff", "employee", "employees", "rrhh", "equipo"},
		Fields: []Field{
			{"name", "Nombre", []string{"nombre", "name", "nombre completo", "empleado", "colaborador", "full name"}, 1},
			{"job", "Puesto", []string{"puesto", "cargo", "job", "position", "rol", "job title"}, 2},
			{"department", "Departamento", []string{"departamento", "area", "department", "seccion"}, 2},
			{"email", "Correo", fEmail.Synonyms, 1},
			{"phone", "Teléfono", fPhone.Synonyms, 1},
			{"hire_date", "Fecha de ingreso", []string{"fecha ingreso", "fecha de ingreso", "hire date", "inicio labores", "fecha contratacion", "ingreso"}, 2},
			{"salary", "Salario", []string{"salario", "sueldo", "salary", "wage"}, 2},
		}},
}

// TargetFor returns the target of a kind.
func TargetFor(kind string) (Target, bool) {
	for _, t := range Targets {
		if t.Kind == kind {
			return t, true
		}
	}
	return Target{}, false
}

// Confidence of a mapping.
const (
	High = "alta"  // loaded on its own
	Mid  = "media" // a Creator confirms it
	Low  = "baja"  // a Creator decides what it is
)

// Mapping is the analysis of one table.
type Mapping struct {
	Table      string              `json:"table"`
	Rows       int                 `json:"rows"`
	Truncated  bool                `json:"truncated,omitempty"`
	Kind       string              `json:"kind"`  // "" when nothing fits
	Model      string              `json:"model"` // the Odoo model
	Label      string              `json:"label"`
	Load       bool                `json:"load"` // AndRho loads it on its own (High and loadable)
	Confidence string              `json:"confidence"`
	Columns    map[string]string   `json:"columns"` // source column -> field key
	Fields     map[string]string   `json:"fields"`  // field key -> its label
	Unmapped   []string            `json:"unmapped"`
	Dimensions []string            `json:"dimensions"` // OLAP's split of the unmapped columns
	Facts      []string            `json:"facts"`
	Sample     []map[string]string `json:"sample"` // first rows, as they came
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// norm lowercases, drops accents and punctuation: "Código de barras (EAN)"
// → "codigo de barras ean".
func normalize(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, strings.ToLower(s))
	if err != nil {
		out = strings.ToLower(s)
	}
	return strings.TrimSpace(nonAlnum.ReplaceAllString(out, " "))
}

// match scores how well a column name fits a field: 3 exact, 2 the
// synonym as whole words inside the name ("precio de venta q"), 0 none.
func match(col string, f Field) float64 {
	best := 0.0
	for _, s := range f.Synonyms {
		switch {
		case col == s:
			return 3
		case strings.HasPrefix(col, s+" ") || strings.HasSuffix(col, " "+s) || strings.Contains(col, " "+s+" "):
			// Longer synonyms are more specific ("codigo de barras" over "codigo").
			if v := 2 + float64(len(s))/100; v > best {
				best = v
			}
		}
	}
	return best
}

var (
	emailVal = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	phoneVal = regexp.MustCompile(`^\+?[\d\s\-().]{7,20}$`)
	dateVal  = regexp.MustCompile(`^\d{1,4}[-/.]\d{1,2}[-/.]\d{1,4}`)
	factName = regexp.MustCompile(`(monto|importe|total|precio|costo|cantidad|qty|amount|price|cost|count|saldo|valor|descuento|impuesto|iva|peso|stock)`)
)

// share is the fraction of non-empty sample values that pass ok.
func share(t Table, col string, ok func(string) bool) float64 {
	n, yes := 0, 0
	for i, r := range t.Rows {
		if i == 200 {
			break
		}
		v := strings.TrimSpace(r[col])
		if v == "" {
			continue
		}
		n++
		if ok(v) {
			yes++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(yes) / float64(n)
}

func isNumber(v string) bool {
	digits := 0
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case strings.ContainsRune(" .,-$Qq€", r):
		default:
			return false
		}
	}
	return digits > 0
}

type scored struct {
	target  Target
	score   float64
	columns map[string]string
}

// assign maps columns to the target's fields, each field to its best column.
func assign(t Table, cols []string, target Target) scored {
	type cand struct {
		col, field string
		s          float64
		order      int
	}
	var cands []cand
	for i, c := range t.Columns {
		for _, f := range target.Fields {
			if s := match(cols[i], f); s > 0 {
				cands = append(cands, cand{c, f.Key, s, i})
			}
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].s != cands[b].s {
			return cands[a].s > cands[b].s
		}
		return cands[a].order < cands[b].order
	})
	res := scored{target: target, columns: map[string]string{}}
	usedField := map[string]bool{}
	for _, c := range cands {
		if usedField[c.field] || res.columns[c.col] != "" {
			continue
		}
		usedField[c.field] = true
		res.columns[c.col] = c.field
	}
	// The values speak for columns the names didn't place: emails and
	// phones are unmistakable.
	for i, c := range t.Columns {
		if res.columns[c] != "" {
			continue
		}
		_ = i
		for _, f := range target.Fields {
			if usedField[f.Key] {
				continue
			}
			if (f.Key == "email" && share(t, c, emailVal.MatchString) >= 0.8) ||
				(f.Key == "phone" && share(t, c, phoneVal.MatchString) >= 0.8 && share(t, c, func(v string) bool { return len(v) >= 8 }) >= 0.8 && !strings.Contains(cols[i], "precio")) {
				usedField[f.Key] = true
				res.columns[c] = f.Key
				break
			}
		}
	}
	for _, f := range target.Fields {
		if usedField[f.Key] {
			res.score += f.Weight
		}
	}
	return res
}

// hint is 3 when the table (or file) name says what it is.
func hint(name string, target Target) float64 {
	words := strings.Fields(normalize(name))
	for _, w := range words {
		for _, h := range target.hints {
			if w == h {
				return 3
			}
		}
	}
	return 0
}

// Analyze maps one table; file is the uploaded file's name (a hint too,
// when the table has no name of its own).
func Analyze(t Table, file string) Mapping {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = normalize(c)
	}
	var all []scored
	for _, target := range Targets {
		s := assign(t, cols, target)
		h := hint(t.Name, target)
		if h == 0 {
			h = hint(file, target) * 0.7
		}
		s.score += h
		// Customers and suppliers share every field: the name (or a
		// "proveedor" column) decides, customers by default.
		if target.Kind == "suppliers" {
			for _, c := range cols {
				if strings.Contains(c, "proveedor") || strings.Contains(c, "supplier") {
					s.score += 2
					break
				}
			}
			s.score -= 0.5
		}
		// Sales without a date or an amount aren't sales.
		if target.Kind == "sales" && !(has(s.columns, "date") && (has(s.columns, "total") || has(s.columns, "qty"))) {
			s.score = 0
		}
		// The rest need a name.
		if target.Kind != "sales" && !has(s.columns, "name") {
			s.score *= 0.4
		}
		all = append(all, s)
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].score > all[b].score })
	best := all[0]

	m := Mapping{Table: t.Name, Rows: t.Total, Truncated: t.Truncated, Columns: map[string]string{}, Fields: map[string]string{}}
	if best.score >= 3 {
		m.Kind, m.Model, m.Label = best.target.Kind, best.target.Model, best.target.Label
		m.Columns = best.columns
		for _, f := range best.target.Fields {
			if has(best.columns, f.Key) {
				m.Fields[f.Key] = f.Label
			}
		}
		margin := best.score - all[1].score
		if partners(best.target.Kind) && partners(all[1].target.Kind) && len(all) > 2 {
			margin = best.score - all[2].score
		}
		switch {
		case best.target.Load && has(best.columns, "name") && len(best.columns) >= 2 && margin >= 1.5:
			m.Confidence = High
		default:
			m.Confidence = Mid
		}
	} else {
		m.Label, m.Confidence = "Sin identificar", Low
	}
	m.Load = m.Confidence == High

	for i, c := range t.Columns {
		if m.Columns[c] != "" {
			continue
		}
		m.Unmapped = append(m.Unmapped, c)
		if factName.MatchString(cols[i]) || share(t, c, isNumber) >= 0.9 && !dateVal.MatchString(first(t, c)) {
			m.Facts = append(m.Facts, c)
		} else {
			m.Dimensions = append(m.Dimensions, c)
		}
	}
	for i, r := range t.Rows {
		if i == 5 {
			break
		}
		m.Sample = append(m.Sample, r)
	}
	return m
}

func partners(kind string) bool { return kind == "customers" || kind == "suppliers" }

func has(cols map[string]string, field string) bool {
	for _, f := range cols {
		if f == field {
			return true
		}
	}
	return false
}

func first(t Table, col string) string {
	for _, r := range t.Rows {
		if v := strings.TrimSpace(r[col]); v != "" {
			return v
		}
	}
	return ""
}

// Apply renames a table's rows to the mapping's fields (what internal/etl
// takes); unmapped columns are dropped.
func (m Mapping) Apply(t Table) []map[string]string {
	out := make([]map[string]string, 0, len(t.Rows))
	for _, r := range t.Rows {
		rec := map[string]string{}
		for col, field := range m.Columns {
			rec[field] = r[col]
		}
		out = append(out, rec)
	}
	return out
}
