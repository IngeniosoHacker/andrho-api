// Package locate is the dashboard's search bar: like `locate` on Linux, a
// few words find where something lives, in AndRho's dashboard or in the
// ERP (Odoo). "productos" finds Inventario > Productos, "en vivo" the live
// sales, "pedidos" the shipping cards. Clicking an ERP result opens Odoo
// right on that screen (POST /erp/sso with next=Path).
//
// It understands the way owners talk, not Odoo's names: each place has
// synonyms in Guatemalan Spanish, and a typo of one letter still matches.
package locate

import (
	"sort"
	"strings"

	"github.com/IngeniosoHacker/andrho-api/internal/live"
)

// Place is somewhere the search can take you.
type Place struct {
	Key   string `json:"key"`
	Label string `json:"label"` // "Inventario › Productos"
	Hint  string `json:"hint"`
	// Where: "erp" (Path is an Odoo path) or "dashboard" (Path is a hash
	// route, "#/envivo").
	Where string `json:"where"`
	Path  string `json:"path"`
	// Model is an Odoo model that must exist (its app installed) for the
	// place to be offered; "" = always.
	Model string   `json:"-"`
	Terms []string `json:"-"`
}

// Places, roughly by how often a small seller needs them.
var Places = []Place{
	{Key: "live", Label: "Venta en vivo › Lives", Hint: "Tus lives: orden de venta, apartados e historial", Where: "erp",
		Path: "/odoo/action-andrho_live_sale.action_live_sale", Model: "andrho.live.sale",
		Terms: []string{"live", "lives", "en vivo", "vivo", "transmision", "directo", "historial de lives", "venta en vivo", "facebook live", "instagram live"}},
	{Key: "live_cards", Label: "Venta en vivo › Tarjetas de envío", Hint: "Pedidos confirmados, datos de envío e impresión", Where: "erp",
		Path: "/odoo/action-andrho_live_sale.action_live_card", Model: "andrho.live.card",
		Terms: []string{"tarjetas", "tarjeta", "envios", "envio", "pedidos", "pedido", "entregas", "imprimir tarjetas", "etiquetas", "guias", "paquetes"}},
	{Key: "live_buyers", Label: "Venta en vivo › Compradores", Hint: "Quién te compra y cuántas veces", Where: "erp",
		Path: "/odoo/action-andrho_live_sale.action_live_buyer", Model: "andrho.live.buyer",
		Terms: []string{"compradores", "compradoras", "clientas", "frecuentes", "clientes frecuentes", "lealtad", "fieles"}},
	{Key: "products", Label: "Inventario › Productos", Hint: "Tu mercancía: fotos, precios y existencias", Where: "erp",
		Path:  "/odoo/action-stock.product_template_action_product",
		Terms: []string{"productos", "producto", "mercancia", "mercaderia", "articulos", "articulo", "ropa", "prendas", "catalogo", "precios", "fotos", "subir mercancia", "cargar inventario"}},
	{Key: "inventory", Label: "Inventario", Hint: "Resumen de entradas, salidas y existencias", Where: "erp",
		Path:  "/odoo/action-stock.stock_picking_type_action",
		Terms: []string{"inventario", "existencias", "stock", "bodega", "almacen"}},
	{Key: "stock_count", Label: "Inventario › Conteo físico", Hint: "Ajusta cuántas unidades tienes", Where: "erp",
		Path:  "/odoo/action-stock.action_view_inventory_tree",
		Terms: []string{"conteo", "contar", "ajuste", "ajustar inventario", "inventario fisico", "cantidades"}},
	{Key: "transfers", Label: "Inventario › Movimientos", Hint: "Entradas, salidas y reservas", Where: "erp",
		Path:  "/odoo/action-stock.action_picking_tree_all",
		Terms: []string{"movimientos", "transferencias", "salidas", "entradas", "reservas", "apartados"}},
	{Key: "receipts", Label: "Inventario › Recepciones", Hint: "Mercancía que te llega (pacas, proveedores)", Where: "erp",
		Path:  "/odoo/action-stock.action_picking_tree_incoming",
		Terms: []string{"recepciones", "recibir", "compras", "pacas", "proveedor", "proveedores", "llego mercancia"}},
	{Key: "contacts", Label: "Contactos", Hint: "Clientes y proveedores", Where: "erp",
		Path:  "/odoo/action-base.action_partner_form",
		Terms: []string{"contactos", "contacto", "clientes", "cliente", "directorio", "telefonos"}},
	{Key: "sales", Label: "Ventas › Pedidos", Hint: "Cotizaciones y pedidos de venta", Where: "erp",
		Path: "/odoo/action-sale.action_orders", Model: "sale.order",
		Terms: []string{"ventas", "venta", "cotizaciones", "cotizacion", "ordenes de venta"}},
	{Key: "invoices", Label: "Facturación › Facturas", Hint: "Facturas a clientes (FEL)", Where: "erp",
		Path: "/odoo/action-account.action_move_out_invoice_type", Model: "account.move",
		Terms: []string{"facturas", "factura", "fel", "facturacion", "cobros"}},
	{Key: "pos", Label: "Punto de venta", Hint: "Tu caja", Where: "erp",
		Path: "/odoo/action-point_of_sale.action_pos_config_kanban", Model: "pos.config",
		Terms: []string{"caja", "punto de venta", "pos", "cobrar", "vender en tienda"}},
	{Key: "crm", Label: "CRM › Oportunidades", Hint: "Prospectos y seguimiento", Where: "erp",
		Path: "/odoo/action-crm.crm_lead_action_pipeline", Model: "crm.lead",
		Terms: []string{"crm", "prospectos", "oportunidades", "seguimiento"}},
	{Key: "purchases", Label: "Compras › Solicitudes", Hint: "Pedidos a proveedores", Where: "erp",
		Path: "/odoo/action-purchase.purchase_rfq", Model: "purchase.order",
		Terms: []string{"orden de compra", "pedir mercancia", "solicitud de compra"}},
	{Key: "d_envivo", Label: "AndRho › En vivo", Hint: "Organiza tu próximo live y mira cómo te fue", Where: "dashboard", Path: "#/envivo",
		Terms: []string{"organizar", "organizar venta", "en vivo", "live", "mi live", "metricas de lives", "publicar no vendidos"}},
	{Key: "d_inicio", Label: "AndRho › Inicio", Hint: "Tu resumen y sugerencias", Where: "dashboard", Path: "#/inicio",
		Terms: []string{"inicio", "resumen", "sugerencias", "metricas", "como voy"}},
	{Key: "d_social", Label: "AndRho › Social", Hint: "Tus redes medidas", Where: "dashboard", Path: "#/social",
		Terms: []string{"redes", "facebook", "instagram", "publicaciones", "seguidores"}},
	{Key: "d_config", Label: "AndRho › Configuración", Hint: "Plan, usuarios, Página de Facebook", Where: "dashboard", Path: "#/configuracion",
		Terms: []string{"configuracion", "plan", "mi plan", "usuarios", "pagina de facebook", "conectar facebook", "cuenta"}},
}

// Result is a place and how well it matched.
type Result struct {
	Place
	Score int `json:"score"`
}

// Search ranks places for q. has(model) says whether an ERP model exists
// (nil = assume yes); places whose app isn't installed are left out.
func Search(q string, has func(model string) bool, limit int) []Result {
	nq := live.Normalize(q)
	if nq == "" {
		return []Result{}
	}
	out := []Result{}
	for _, p := range Places {
		if p.Model != "" && has != nil && !has(p.Model) {
			continue
		}
		if s := score(nq, p); s > 0 {
			out = append(out, Result{Place: p, Score: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func score(q string, p Place) int {
	best := 0
	cands := append([]string{live.Normalize(p.Label)}, p.Terms...)
	for i, t := range cands {
		t = live.Normalize(t)
		s := 0
		switch {
		case t == q:
			s = 100
		case strings.HasPrefix(t, q):
			s = 80
		case strings.Contains(" "+t+" ", " "+q+" "):
			s = 70
		case len(q) >= 3 && strings.Contains(t, q):
			s = 50
		case close(q, t):
			s = 40
			// A typo of the whole term beats a typo of one of its words.
			if len(strings.Fields(t)) == len(strings.Fields(q)) {
				s = 45
			}
		}
		if i == 0 && s > 0 {
			s -= 5 // a synonym says it better than the label
		}
		if s > best {
			best = s
		}
	}
	return best
}

// close: every word of q is a word of t (or its prefix) with at most one
// letter wrong, for words of four letters or more.
func close(q, t string) bool {
	tw := strings.Fields(t)
	for _, w := range strings.Fields(q) {
		ok := false
		for _, x := range tw {
			if strings.HasPrefix(x, w) || (len(w) >= 4 && len(x) >= 4 && levenshtein(w, x) <= 1) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// ValidERPPath is what POST /erp/sso accepts as next: a path inside Odoo.
func ValidERPPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\ \t\r\n") || len(p) > 300 {
		return false
	}
	return strings.HasPrefix(p, "/odoo") || strings.HasPrefix(p, "/report/")
}
