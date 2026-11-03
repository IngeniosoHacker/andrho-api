// Package business holds AndRho's configuration per type of business. The
// signup's analysis (internal/analyzer) decides which modules a business
// needs from what it told us; on top of that, each type of business brings
// its own defaults: the modules businesses like it always use, the Odoo apps
// behind them, the order of the dashboard's sections, what AndRho watches
// first and the structure of its website.
//
//	servicios     plumbing, electricity, workshops, cleaning, consulting…:
//	              quotes, visits and appointments, recurring clients
//	restaurantes  food: point of sale with tables, ingredients, expiry,
//	              waste, purchases, menu and orders
//	ventas        reselling products it doesn't make: point of sale,
//	              stock, purchases, delivery, catalog
//
// Anything else (manufacturing, for now) has no type: the analysis alone
// decides. The type is inferred from the signup's sectors (or the
// description) and the owner can change it in Configuración.
package business

import "strings"

// Profile is one type of business.
type Profile struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Examples    []string `json:"examples"`
	// Modules are AndRho modules (analyzer.Modules keys) businesses of this
	// type use; the analysis adds the ones it didn't already pick.
	Modules []string `json:"modules"`
	// OdooApps are Odoo apps added to the ERP on top of what the modules
	// need (analyzer.OdooModules), e.g. tables for restaurants.
	OdooApps []string `json:"odoo_apps"`
	// Sections is the dashboard's sidebar order for this type (keys of the
	// dashboard's nav.js); sections not listed keep their place after these.
	Sections []string `json:"sections"`
	// Focus is what AndRho watches first, in the owner's words.
	Focus []string `json:"focus"`
	// Site is the structure of the business' website (the template's
	// sections and its main call to action).
	Site Site `json:"site"`
}

// Site is a website's structure for a type of business.
type Site struct {
	Headline string        `json:"headline"`
	CTA      string        `json:"cta"`
	Sections []SiteSection `json:"sections"`
}

// SiteSection is one block of the website.
type SiteSection struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Hint  string `json:"hint"`
}

// Profiles, keyed.
var Profiles = map[string]Profile{
	"servicios": {
		Key: "servicios", Name: "Servicios",
		Description: "Negocios que venden su trabajo: visitas, reparaciones, instalaciones, citas.",
		Examples:    []string{"plomería", "electricidad", "talleres", "limpieza", "consultoría", "clínicas"},
		Modules:     []string{"clientes", "agenda", "finanzas", "whatsapp", "sitio_web", "tracking"},
		OdooApps:    []string{"crm", "sale_management", "calendar", "project"},
		Sections:    []string{"inicio", "prospectos", "clientes", "web", "social", "resumen", "inventario", "sucursales", "usuarios"},
		Focus: []string{
			"Qué tan rápido respondes a quien te pide una cotización",
			"Cuántas cotizaciones se convierten en trabajos",
			"Qué clientes te vuelven a llamar y cuáles no",
		},
		Site: Site{
			Headline: "Tu servicio, en tu zona, cuando lo necesitas",
			CTA:      "Pide tu cotización",
			Sections: []SiteSection{
				{"servicios", "Lo que hacemos", "Cada servicio con una frase y su precio desde"},
				{"zona", "Dónde trabajamos", "Colonias, zonas o municipios que cubres"},
				{"proceso", "Cómo trabajamos", "De la llamada al trabajo terminado, en 3 pasos"},
				{"testimonios", "Lo que dicen nuestros clientes", "Reseñas reales con nombre y zona"},
				{"contacto", "Cotiza por WhatsApp", "Botón directo y horario de atención"},
			},
		},
	},
	"restaurantes": {
		Key: "restaurantes", Name: "Restaurantes",
		Description: "Comida: restaurantes, cafeterías, comedores, panaderías, food trucks.",
		Examples:    []string{"restaurantes", "cafeterías", "comedores", "panaderías", "pizzerías", "food trucks"},
		Modules:     []string{"ventas", "inventario", "vencimientos", "mermas", "compras", "clientes", "sitio_web"},
		OdooApps:    []string{"point_of_sale", "pos_restaurant", "product_expiry", "purchase"},
		Sections:    []string{"inicio", "inventario", "clientes", "sucursales", "web", "social", "resumen", "prospectos", "usuarios"},
		Focus: []string{
			"Qué platillos se venden y cuáles no",
			"Ingredientes por vencer y lo que se tira",
			"Horas y días con más clientes",
		},
		Site: Site{
			Headline: "Ven a probarlo o pídelo a domicilio",
			CTA:      "Haz tu pedido",
			Sections: []SiteSection{
				{"menu", "Menú", "Platillos con foto, descripción corta y precio"},
				{"horario", "Horario y ubicación", "Mapa, horario por día y parqueo"},
				{"pedidos", "Pedidos a domicilio", "WhatsApp o plataformas de entrega"},
				{"reservaciones", "Reservaciones", "Para grupos y eventos"},
				{"galeria", "Así se ve", "Fotos del lugar y los platillos"},
			},
		},
	},
	"ventas": {
		Key: "ventas", Name: "Ventas",
		Description: "Venta de productos que no fabricas: tiendas, distribuidoras, ventas en línea.",
		Examples:    []string{"tiendas", "ferreterías", "boutiques", "distribuidoras", "ventas en línea"},
		Modules:     []string{"ventas", "inventario", "compras", "clientes", "logistica", "sitio_web"},
		OdooApps:    []string{"point_of_sale", "purchase", "sale_management"},
		Sections:    []string{"inicio", "inventario", "clientes", "web", "social", "sucursales", "prospectos", "resumen", "usuarios"},
		Focus: []string{
			"Productos que se agotan y los que no se mueven",
			"Cuándo y cuánto pedirle a cada proveedor",
			"Qué clientes compran más y quién dejó de comprar",
		},
		Site: Site{
			Headline: "Lo que buscas, con entrega a tu puerta",
			CTA:      "Ver catálogo",
			Sections: []SiteSection{
				{"catalogo", "Productos destacados", "Los más vendidos, con foto y precio"},
				{"categorias", "Categorías", "Para encontrar rápido lo que buscan"},
				{"envios", "Envíos", "Zonas, tiempos y costo de entrega"},
				{"pagos", "Formas de pago", "Efectivo, tarjeta, transferencia"},
				{"contacto", "Pide por WhatsApp", "Botón directo y ubicación de la tienda"},
			},
		},
	},
}

// Keys in display order.
var Keys = []string{"servicios", "restaurantes", "ventas"}

// Get returns the profile for key, ok=false for none/unknown.
func Get(key string) (Profile, bool) {
	p, ok := Profiles[key]
	return p, ok
}

// bySector maps the signup's sectors (analyzer) to a type. Manufacturing
// makes what it sells, so it isn't "ventas"; it has no type for now.
var bySector = map[string]string{
	"restaurantes": "restaurantes",
	"servicios":    "servicios",
	"salud":        "servicios",
	"logistica":    "servicios",
	"comercio":     "ventas",
}

// Food first (a restaurant also "sells"), then services, then retail.
var precedence = []string{"restaurantes", "servicios", "ventas"}

var keywords = map[string][]string{
	"restaurantes": {"restaurante", "comedor", "cafeter", "cocina", "comida", "pizzer", "panader", "food truck", "platillo", "almuerzo", "menú"},
	"servicios":    {"plomer", "electric", "taller", "limpieza", "consultor", "instalac", "reparac", "mantenimiento", "servicio", "clínica", "clinica", "salón", "barber", "fumiga", "jardiner", "carpinter"},
	"ventas":       {"tienda", "vendo", "venta de", "vendemos", "abarrot", "ferreter", "boutique", "distribui", "importa", "revend", "catálogo", "catalogo"},
}

// Classify infers the type from the signup's sectors, or from the
// description when there are none. "" = no type (the analysis decides).
func Classify(sectors []string, description string) string {
	found := map[string]bool{}
	for _, s := range sectors {
		if t := bySector[s]; t != "" {
			found[t] = true
		}
	}
	if len(found) == 0 && description != "" {
		d := strings.ToLower(description)
		for t, words := range keywords {
			for _, w := range words {
				if strings.Contains(d, w) {
					found[t] = true
					break
				}
			}
		}
	}
	for _, t := range precedence {
		if found[t] {
			return t
		}
	}
	return ""
}

// OdooApps returns the Odoo apps for type key, adding to base without
// repeats. pos_restaurant only makes sense with the point of sale.
func OdooApps(key string, base []string) []string {
	p, ok := Profiles[key]
	if !ok {
		return base
	}
	out := append([]string{}, base...)
	seen := map[string]bool{}
	for _, m := range out {
		seen[m] = true
	}
	for _, m := range p.OdooApps {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
