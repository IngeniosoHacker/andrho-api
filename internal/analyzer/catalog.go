// Package analyzer turns what a business tells us (the survey's answers, or
// a free-text description) into the AndRho setup it needs: the modules,
// ranked, and three packages built from them.
//
// AndRho is modular. What separates the plans is the core (what every
// account on that plan gets) and how many modules it holds. The three
// packages are the account plans despegue, en_orbita and galactico (see
// accounts.plan); each takes as many of the business' modules as it fits,
// in priority order, and says what it leaves out.
package analyzer

// Module is one piece of AndRho a business can switch on.
type Module struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Area        string `json:"area"`
	// Provision is what turning the module on sets up for the account
	// (see internal/provision): "erp", "website", "whatsapp" or "".
	Provision string `json:"provision,omitempty"`
}

// Modules is every module, keyed. Keys match recommend.knownModules and
// andrho's SUGGESTED_MODULE_LABELS.
var Modules = map[string]Module{
	"analitica":      {"analitica", "Análisis con IA", "Modelos estadísticos que leen tus datos todo el tiempo y una IA que te lo explica.", "datos", ""},
	"unificacion":    {"unificacion", "Unificación de datos", "Todo lo que hoy está regado, en un solo lugar.", "datos", ""},
	"erp":            {"erp", "ERP con Odoo", "Tu sistema de gestión, instalado y administrado por AndRho.", "operacion", "erp"},
	"inventario":     {"inventario", "Inventario en tiempo real", "Existencias, entradas y salidas, con alertas antes de quedarte sin producto.", "operacion", "erp"},
	"ventas":         {"ventas", "Punto de venta", "Cobra, factura y registra cada venta, en tienda o en línea.", "operacion", "erp"},
	"vencimientos":   {"vencimientos", "Vencimientos y rotación", "Lo que vence primero, se usa primero; lo que se tira, se mide.", "operacion", "erp"},
	"mermas":         {"mermas", "Mermas", "Qué se pierde, cuánto cuesta y cómo aprovechar lo que no se vende.", "operacion", "erp"},
	"compras":        {"compras", "Compras y proveedores", "Órdenes de compra sugeridas según lo que vas a vender.", "operacion", "erp"},
	"produccion":     {"produccion", "Producción", "Planifica qué producir y con qué materia prima.", "operacion", "erp"},
	"finanzas":       {"finanzas", "Finanzas y contabilidad", "Ingresos, gastos y flujo de caja conectados a tus ventas.", "operacion", "erp"},
	"rrhh":           {"rrhh", "Personal y planilla", "Tu equipo, sus turnos y su planilla.", "operacion", "erp"},
	"logistica":      {"logistica", "Envíos", "Pedidos por entregar y su estado.", "operacion", "erp"},
	"rutas":          {"rutas", "Rutas", "Seguimiento de repartos y rutas.", "operacion", "erp"},
	"agenda":         {"agenda", "Agenda", "Citas y seguimiento de cada cliente.", "clientes", "erp"},
	"citas":          {"citas", "Citas y expedientes", "Agenda clínica y expediente por paciente.", "clientes", "erp"},
	"clientes":       {"clientes", "Clientes (CRM)", "Quién te compra, qué, cada cuánto y quién dejó de venir.", "clientes", "erp"},
	"marketing":      {"marketing", "Marketing y campañas", "Qué campaña trae ventas de verdad.", "crecimiento", ""},
	"sitio_web":      {"sitio_web", "Sitio web", "Tu sitio, hecho por Creators, con medición incluida.", "crecimiento", "website"},
	"tracking":       {"tracking", "Rastreador web", "Mide tu sitio actual: visitas, búsquedas e IA.", "crecimiento", ""},
	"redes":          {"redes", "Redes sociales", "Tus redes medidas junto con tus ventas.", "crecimiento", ""},
	"redes_creacion": {"redes_creacion", "Creación de redes", "Tus páginas en Meta y TikTok, listas para vender.", "crecimiento", ""},
	"whatsapp":       {"whatsapp", "WhatsApp Business", "Atiende y vende por WhatsApp desde AndRho.", "clientes", "whatsapp"},
	"creators":       {"creators", "Creators", "Diseñadores y programadores reales a tu disposición.", "crecimiento", ""},
}

// Plan is one package: its core and how many modules it holds (-1 = all).
type Plan struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Tagline     string   `json:"tagline"`
	Core        []string `json:"core"`
	ModuleLimit int      `json:"module_limit"`
}

// Plans in ascending order. Keys are accounts.plan values.
var Plans = []Plan{
	{
		Key: "despegue", Name: "Despegue", Tagline: "Lo esencial para dejar de decidir a ciegas.",
		Core: []string{
			"Tu panel con los datos del negocio en un solo lugar",
			"Rastreador para tu sitio web",
			"10 sugerencias de AndRho al mes",
			"Hasta 2 usuarios",
		},
		ModuleLimit: 3,
	},
	{
		Key: "en_orbita", Name: "En Órbita", Tagline: "Para crecer con cada área conectada.",
		Core: []string{
			"Todo lo de Despegue",
			"Análisis constante con IA que te lo explica",
			"30 sugerencias al mes",
			"Resumen semanal de tu negocio",
			"Hasta 10 usuarios",
		},
		ModuleLimit: 6,
	},
	{
		Key: "galactico", Name: "Galáctico", Tagline: "Tu negocio completo. Nada se queda en tierra.",
		Core: []string{
			"Todo lo de En Órbita",
			"Habla con AndRho por WhatsApp",
			"Creators: diseñadores y programadores a tu disposición",
			"Sugerencias ilimitadas",
			"Usuarios ilimitados",
		},
		ModuleLimit: -1,
	},
}

// planByRecommendation maps the survey's plans (internal/recommend) to the
// account plans.
var planByRecommendation = map[string]string{"basico": "despegue", "crecer": "en_orbita", "todo": "galactico"}

// Every account gets an ERP: Odoo with inventory as its base. BaseOdooModules
// is what every client's database installs; odooByModule adds the Odoo apps
// each AndRho module needs on top. Modules not listed live outside Odoo
// (analysis, marketing, the website, WhatsApp) or ride on stock itself
// (mermas are stock scraps). Names are Odoo 19 Community technical names,
// the ones VPS/odoo/provision-client.sh passes to `odoo -i`.
var BaseOdooModules = []string{"stock"}

var odooByModule = map[string][]string{
	"ventas":       {"point_of_sale"},
	"vencimientos": {"product_expiry"},
	"compras":      {"purchase"},
	"produccion":   {"mrp"},
	"finanzas":     {"account"},
	"rrhh":         {"hr"},
	"logistica":    {"stock_delivery"},
	"rutas":        {"stock_delivery"},
	"agenda":       {"calendar"},
	"citas":        {"calendar"},
	"clientes":     {"crm"},
}

// OdooModules is the list of Odoo modules for the AndRho modules in keys:
// the base first, then each module's apps in keys' order, without repeats.
func OdooModules(keys []string) []string {
	out := append([]string{}, BaseOdooModules...)
	seen := map[string]bool{}
	for _, m := range out {
		seen[m] = true
	}
	for _, k := range keys {
		for _, m := range odooByModule[k] {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}
