package analyzer

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// The signup's quick setup lets a business describe itself in a paragraph
// instead of answering the survey. Describe turns that paragraph into the
// survey's answers and the boarding pass (who is signing up, the company,
// how to reach them) plus whatever else it says about the brand and the
// tools in use: with Claude when the AI client is enabled, with keyword
// rules otherwise (or when the AI call fails). The signup then asks only the
// questions the paragraph didn't answer.

// MaxDescription is the longest description accepted.
const MaxDescription = 2000

const describeTask = `Lee la descripción que un negocio escribió en el registro de AndRho y conviértela en las respuestas de nuestra encuesta y en los datos de su pase de abordaje.
Extrae SOLO lo que la descripción dice o deja claro sin adivinar; omite cualquier campo que no diga.

"answers" usa SOLO estos valores:
- sectors: comercio, restaurantes, servicios, manufactura, logistica, salud, otro
- companySize: solo, 1-10, 11-50, 51-200, 200+  (personas que trabajan, contando al dueño)
- salesMethods: tienda, en_linea, redes, domicilio, mayoreo, pedido, credito, ruta
- hasWebsite: si, no
- socialNetworks: facebook, instagram, tiktok, ninguna
- messagingApps: whatsapp, whatsapp_business, messenger, instagram_dm, telegram, correo, llamadas
- management (con qué administra hoy): papel, excel, software, erp, varias, nada
- satisfaction (con su sistema actual, solo si lo dice): 1 a 5
- painPoints: datos_dispersos, no_me_dice, reportes_tarde, dificil, caro, no_conecta, pierdo_control, soporte, sin_sistema
- modules (áreas que quiere cubrir): finanzas, ventas, inventario, marketing, clientes, compras, produccion, logistica, rrhh, sitio_web
- expectations (máximo 3): mas_clientes, vender_mas, organizarse, digitalizarse, automatizar, decidir_mejor, reducir_perdidas, mas_vistas, mas_seguidores
- unsoldHandling (qué pasa con lo que no vende): se_pierde, se_transforma, descuento, se_dona, bodega, no_pasa
- restaurantExpiry (restaurantes, control de vencimientos): sistema, a_mano, peps, no_controlo
- sectorAnswers, con estas claves y valores:
  - "restaurantes.pedidos" (varios, separados por coma): mesa, para_llevar, apps_delivery, whatsapp, eventos
  - "comercio.inventario": sistema, conteo, a_ojo, no_se
  - "servicios.seguimiento": crm, calendario, whatsapp, cuaderno
  - "manufactura.produccion": sistema, hojas, experiencia, sobre_la_marcha
  - "logistica.seguimiento": gps, llamadas, papel, ninguno
  - "salud.expedientes": sistema, excel, papel, mezcla
  - "otro.descripcion": a qué se dedica, en una línea (solo si el sector es otro)

"contact": name (la persona que escribe), company (nombre del negocio), email, phone (WhatsApp o teléfono del negocio, tal como lo escribió).
"brand": essence (cómo describe su marca: estilo, valores, colores, tono; en sus palabras) y has_brand (true si dice que ya tiene marca o logo, false si dice que no tiene).
"details": websiteUrl (si da la dirección de su sitio), currentSoftware (nombres de los programas que usa), improvement (lo que más quiere mejorar, en menos de 70 caracteres).

Devuelve {"answers": {...}, "contact": {...}, "brand": {...}, "details": {...}, "summary": "una frase en español, de tú, que resuma lo que entendiste del negocio"}.`

// Contact is the boarding pass the description fills in.
type Contact struct {
	Name    string `json:"name,omitempty"`
	Company string `json:"company,omitempty"`
	Email   string `json:"email,omitempty"`
	Phone   string `json:"phone,omitempty"`
}

// Brand is what the description says about the brand. HasBrand is nil when
// it says nothing either way.
type Brand struct {
	Essence  string `json:"essence,omitempty"`
	HasBrand *bool  `json:"has_brand,omitempty"`
}

// Details are the survey's open-text answers.
type Details struct {
	WebsiteURL         string `json:"websiteUrl,omitempty"`
	CurrentSoftware    string `json:"currentSoftware,omitempty"`
	Improvement        string `json:"improvement,omitempty"`
	SatisfactionReason string `json:"satisfactionReason,omitempty"`
}

// Description is what Describe understood.
type Description struct {
	Answers recommend.Answers `json:"answers"`
	Contact Contact           `json:"contact"`
	Brand   Brand             `json:"brand"`
	Details Details           `json:"details"`
	Summary string            `json:"summary"`
	Source  string            `json:"source"` // "ia" or "reglas"
}

// Describe reads text into answers. client may be nil or disabled.
func Describe(ctx context.Context, client *ai.Client, text string) Description {
	text = strings.TrimSpace(text)
	if client.Enabled() {
		var out Description
		if err := client.JSON(ctx, "analyzer", "", ai.WithContext(describeTask), text, 1200, &out); err == nil {
			out.Answers = Sanitize(out.Answers)
			out.Contact = cleanContact(out.Contact)
			out.Brand.Essence = clip(out.Brand.Essence, maxEssence)
			out.Details = cleanDetails(out.Details)
			out.Source = "ia"
			return out
		}
	}
	a := ruleAnswers(text)
	return Description{Answers: a, Contact: ruleContact(text), Summary: ruleSummary(a), Source: "reglas"}
}

const maxEssence = 1000

// clip trims s and cuts it to n characters.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}

var (
	emailRe = regexp.MustCompile(`(?i)^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	phoneRe = regexp.MustCompile(`^\+?[\d\s().-]{8,20}$`)
)

func cleanContact(c Contact) Contact {
	c = Contact{Name: clip(c.Name, 120), Company: clip(c.Company, 120), Email: clip(c.Email, 200), Phone: clip(c.Phone, 40)}
	if !emailRe.MatchString(c.Email) {
		c.Email = ""
	}
	if !phoneRe.MatchString(c.Phone) {
		c.Phone = ""
	}
	return c
}

func cleanDetails(d Details) Details {
	return Details{
		WebsiteURL: clip(d.WebsiteURL, 200), CurrentSoftware: clip(d.CurrentSoftware, 200),
		Improvement: clip(d.Improvement, 70), SatisfactionReason: clip(d.SatisfactionReason, 500),
	}
}

// allowed values per field, to drop anything the model invents.
var allowed = map[string]map[string]bool{
	"sectors":          set("comercio", "restaurantes", "servicios", "manufactura", "logistica", "salud", "otro"),
	"companySize":      set("solo", "1-10", "11-50", "51-200", "200+"),
	"salesMethods":     set("tienda", "en_linea", "redes", "domicilio", "mayoreo", "pedido", "credito", "ruta"),
	"hasWebsite":       set("si", "no"),
	"socialNetworks":   set("facebook", "instagram", "tiktok", "ninguna"),
	"messagingApps":    set("whatsapp", "whatsapp_business", "messenger", "instagram_dm", "telegram", "correo", "llamadas"),
	"management":       set("papel", "excel", "software", "erp", "varias", "nada"),
	"painPoints":       set("datos_dispersos", "no_me_dice", "reportes_tarde", "dificil", "caro", "no_conecta", "pierdo_control", "soporte", "sin_sistema"),
	"modules":          set("finanzas", "ventas", "inventario", "marketing", "clientes", "compras", "produccion", "logistica", "rrhh", "sitio_web"),
	"expectations":     set("mas_clientes", "vender_mas", "organizarse", "digitalizarse", "automatizar", "decidir_mejor", "reducir_perdidas", "mas_vistas", "mas_seguidores"),
	"unsoldHandling":   set("se_pierde", "se_transforma", "descuento", "se_dona", "bodega", "no_pasa"),
	"restaurantExpiry": set("sistema", "a_mano", "peps", "no_controlo"),
}

// sectorAnswerValues are the sector questions' keys and values (the
// survey's SECTOR_QUESTIONS); multi ones are comma-separated, a nil set
// takes free text.
var sectorAnswerValues = map[string]struct {
	values map[string]bool
	multi  bool
}{
	"restaurantes.pedidos":   {set("mesa", "para_llevar", "apps_delivery", "whatsapp", "eventos"), true},
	"comercio.inventario":    {set("sistema", "conteo", "a_ojo", "no_se"), false},
	"servicios.seguimiento":  {set("crm", "calendario", "whatsapp", "cuaderno"), false},
	"manufactura.produccion": {set("sistema", "hojas", "experiencia", "sobre_la_marcha"), false},
	"logistica.seguimiento":  {set("gps", "llamadas", "papel", "ninguno"), false},
	"salud.expedientes":      {set("sistema", "excel", "papel", "mezcla"), false},
	"otro.descripcion":       {nil, false},
}

func set(vs ...string) map[string]bool {
	m := map[string]bool{}
	for _, v := range vs {
		m[v] = true
	}
	return m
}

func keep(field string, in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range in {
		if allowed[field][v] && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func one(field, v string) string {
	if allowed[field][v] {
		return v
	}
	return ""
}

func sanitizeSectorAnswers(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		q, ok := sectorAnswerValues[k]
		switch {
		case !ok:
		case q.values == nil:
			if v = clip(v, 200); v != "" {
				out[k] = v
			}
		case q.multi:
			var vals []string
			for _, x := range strings.Split(v, ",") {
				if x = strings.TrimSpace(x); q.values[x] && !contains(vals, x) {
					vals = append(vals, x)
				}
			}
			if len(vals) > 0 {
				out[k] = strings.Join(vals, ",")
			}
		case q.values[strings.TrimSpace(v)]:
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// Sanitize keeps only the survey's known values (and at most 3 expectations).
func Sanitize(a recommend.Answers) recommend.Answers {
	exp := keep("expectations", a.Expectations)
	if len(exp) > 3 {
		exp = exp[:3]
	}
	sat := a.Satisfaction
	if sat < 0 || sat > 5 {
		sat = 0
	}
	return recommend.Answers{
		Sectors:        keep("sectors", a.Sectors),
		CompanySize:    one("companySize", a.CompanySize),
		SalesMethods:   keep("salesMethods", a.SalesMethods),
		SectorAnswers:  sanitizeSectorAnswers(a.SectorAnswers),
		RestaurantExp:  one("restaurantExpiry", a.RestaurantExp),
		UnsoldHandling: one("unsoldHandling", a.UnsoldHandling),
		HasWebsite:     one("hasWebsite", a.HasWebsite),
		SocialNetworks: keep("socialNetworks", a.SocialNetworks),
		MessagingApps:  keep("messagingApps", a.MessagingApps),
		Management:     one("management", a.Management),
		Satisfaction:   sat,
		PainPoints:     keep("painPoints", a.PainPoints),
		Modules:        keep("modules", a.Modules),
		Expectations:   exp,
	}
}

// ---------------------------------------------------------------------
// Rules: keywords -> answers.
// ---------------------------------------------------------------------

type rule struct {
	field string
	value string
	words []string
}

var rules = []rule{
	{"sectors", "restaurantes", []string{"restaurante", "comedor", "cafeter", "cocina", "comida", "pizzer", "panader", "bar", "food truck", "platillo"}},
	{"sectors", "comercio", []string{"tienda", "venta de", "vendo", "comercio", "abarrot", "ferreter", "boutique", "ropa", "zapat", "farmacia", "librer", "distribu", "importa"}},
	{"sectors", "servicios", []string{"servicio", "consultor", "agencia", "taller", "salón", "salon de", "barber", "academia", "colegio", "gimnasio", "limpieza"}},
	{"sectors", "manufactura", []string{"fábrica", "fabrica", "fabricamos", "producimos", "producción", "produccion", "manufactur", "maquila", "elaboramos", "planta de"}},
	{"sectors", "logistica", []string{"logística", "logistica", "transporte", "envíos", "envios", "mensajer", "flete", "reparto", "camiones"}},
	{"sectors", "salud", []string{"clínica", "clinica", "médic", "medic", "dental", "dentist", "hospital", "laboratorio", "paciente", "veterinari"}},

	{"salesMethods", "tienda", []string{"tienda física", "tienda fisica", "local", "sucursal", "mostrador", "punto de venta"}},
	{"salesMethods", "en_linea", []string{"en línea", "en linea", "online", "tienda virtual", "página web", "pagina web", "e-commerce", "ecommerce"}},
	{"salesMethods", "redes", []string{"redes", "facebook", "instagram", "tiktok", "lives", "en vivo"}},
	{"salesMethods", "domicilio", []string{"domicilio", "delivery", "a casa"}},
	{"salesMethods", "mayoreo", []string{"mayoreo", "por mayor", "distribuidores"}},
	{"salesMethods", "ruta", []string{"ruteo", "por ruta", "rutas de venta"}},

	{"socialNetworks", "facebook", []string{"facebook", "marketplace"}},
	{"socialNetworks", "instagram", []string{"instagram"}},
	{"socialNetworks", "tiktok", []string{"tiktok"}},
	{"messagingApps", "whatsapp", []string{"whatsapp", "wasap", "whats"}},
	{"messagingApps", "messenger", []string{"messenger"}},

	{"management", "papel", []string{"papel", "cuaderno", "libreta", "a mano"}},
	{"management", "excel", []string{"excel", "hoja de cálculo", "hoja de calculo", "google sheets", "sheets"}},
	{"management", "erp", []string{"erp", "odoo", "sap"}},

	{"painPoints", "datos_dispersos", []string{"regad", "dispers", "varios lados", "diferentes formatos", "no cuadra"}},
	{"painPoints", "no_me_dice", []string{"no sabemos", "no sé qué", "no se que", "no sabemos qué", "a ciegas", "intuición", "intuicion"}},
	{"painPoints", "pierdo_control", []string{"faltante", "me falta", "robo", "no sé dónde", "no se donde", "pierdo", "se pierde", "descontrol", "se vence", "vencen"}},
	{"painPoints", "reportes_tarde", []string{"reportes", "a fin de mes"}},

	{"modules", "inventario", []string{"inventario", "existencias", "bodega", "stock", "ingrediente", "producto"}},
	{"modules", "ventas", []string{"ventas", "vendemos", "vendo", "factur", "cobr", "caja"}},
	{"modules", "finanzas", []string{"contab", "finanz", "gastos", "flujo de caja", "dinero"}},
	{"modules", "clientes", []string{"clientes", "fidel"}},
	{"modules", "marketing", []string{"marketing", "publicidad", "anuncios", "campañas"}},
	{"modules", "rrhh", []string{"empleados", "planilla", "personal", "trabajadores", "turnos"}},
	{"modules", "compras", []string{"proveedor", "compras", "pedidos a"}},
	{"modules", "produccion", []string{"producción", "produccion", "materia prima"}},
	{"modules", "logistica", []string{"entregas", "envíos", "envios", "reparto"}},
	{"modules", "sitio_web", []string{"página web", "pagina web", "sitio web"}},

	{"expectations", "vender_mas", []string{"vender más", "vender mas", "más ventas", "mas ventas", "crecer"}},
	{"expectations", "mas_clientes", []string{"más clientes", "mas clientes", "nuevos clientes", "los mismos clientes"}},
	{"expectations", "organizarse", []string{"orden", "organiz", "control"}},
	{"expectations", "automatizar", []string{"automat", "sin tiempo", "no me da tiempo", "estrés", "estres"}},
	{"expectations", "reducir_perdidas", []string{"pérdida", "perdida", "merma", "desperdicio", "vence", "caduc"}},
}

var (
	numberEmployees = regexp.MustCompile(`(\d+)\s*(empleados|trabajadores|colaboradores|personas)`)
	branches        = regexp.MustCompile(`(\d+|varias|dos|tres|cuatro)\s*(sucursales|tiendas|locales|empresas)`)
	noWebsite       = regexp.MustCompile(`(no tengo|no tenemos|sin) (página|pagina|sitio)`)
)

// normalize lowercases and turns punctuation into spaces, so short words
// can be matched whole (" sap " must not match inside "whatsapp").
func normalize(s string) string {
	return " " + strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	}), " ") + " "
}

// matches finds w in t: whole word for short keywords, substring (a stem
// like "factur") otherwise.
func matches(t, w string) bool {
	if len(w) <= 4 {
		return strings.Contains(t, " "+strings.TrimSpace(w)+" ")
	}
	return strings.Contains(t, w)
}

func ruleAnswers(text string) recommend.Answers {
	t := normalize(text)
	// "No tengo página web" must not read as selling online.
	withoutSite := noWebsite.MatchString(t)
	t = noWebsite.ReplaceAllString(t, " ")
	found := map[string][]string{}
	for _, r := range rules {
		for _, w := range r.words {
			if matches(t, w) {
				found[r.field] = append(found[r.field], r.value)
				break
			}
		}
	}
	a := recommend.Answers{
		Sectors:        keep("sectors", found["sectors"]),
		SalesMethods:   keep("salesMethods", found["salesMethods"]),
		SocialNetworks: keep("socialNetworks", found["socialNetworks"]),
		MessagingApps:  keep("messagingApps", found["messagingApps"]),
		PainPoints:     keep("painPoints", found["painPoints"]),
		Modules:        keep("modules", found["modules"]),
		Expectations:   keep("expectations", found["expectations"]),
	}
	if len(a.Expectations) > 3 {
		a.Expectations = a.Expectations[:3]
	}
	if m := found["management"]; len(m) == 1 {
		a.Management = m[0]
	} else if len(m) > 1 {
		a.Management = "varias"
	}
	switch {
	case withoutSite:
		a.HasWebsite = "no"
		if !contains(a.Modules, "sitio_web") {
			a.Modules = append(a.Modules, "sitio_web")
		}
	case strings.Contains(t, "página web") || strings.Contains(t, "pagina web") || strings.Contains(t, "sitio web"):
		a.HasWebsite = "si"
	}
	if m := numberEmployees.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch {
		case n <= 1:
			a.CompanySize = "solo"
		case n <= 10:
			a.CompanySize = "1-10"
		case n <= 50:
			a.CompanySize = "11-50"
		case n <= 200:
			a.CompanySize = "51-200"
		default:
			a.CompanySize = "200+"
		}
	}
	if branches.MatchString(t) && a.CompanySize == "" {
		a.CompanySize = "11-50"
	}
	if branches.MatchString(t) && !contains(a.PainPoints, "datos_dispersos") {
		a.PainPoints = append(a.PainPoints, "datos_dispersos")
	}
	return a
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

var (
	ruleEmail   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	rulePhone   = regexp.MustCompile(`\+?\d[\d\s-]{6,16}\d`)
	ruleName    = regexp.MustCompile(`(?i:me llamo|mi nombre es)\s+([A-ZÁÉÍÓÚÑ][a-záéíóúñ]+(?:\s+(?:(?:de|del|la)\s+)?[A-ZÁÉÍÓÚÑ][a-záéíóúñ]+){0,3})`)
	ruleCompany = regexp.MustCompile(`(?i:se llama|llamad[oa]|mi (?:negocio|empresa|tienda|restaurante|marca) es)\s+[«"“]?([A-ZÁÉÍÓÚÑ0-9][\wÁÉÍÓÚÑáéíóúñ&'-]*(?:\s+(?:(?:de|del|la|las|los|el|y)\s+)?[A-ZÁÉÍÓÚÑ0-9][\wÁÉÍÓÚÑáéíóúñ&'-]*){0,4})`)
)

// ruleContact finds the boarding pass in the text without the AI: an
// email, a phone with at least 8 digits, "me llamo …" and "se llama …".
func ruleContact(text string) Contact {
	c := Contact{Email: ruleEmail.FindString(text)}
	for _, p := range rulePhone.FindAllString(text, -1) {
		if digits := strings.Count(strings.Map(func(r rune) rune {
			if unicode.IsDigit(r) {
				return 'd'
			}
			return -1
		}, p), "d"); digits >= 8 {
			c.Phone = strings.TrimSpace(p)
			break
		}
	}
	if m := ruleName.FindStringSubmatch(text); m != nil {
		c.Name = m[1]
	}
	if m := ruleCompany.FindStringSubmatch(text); m != nil {
		c.Company = strings.TrimRight(m[1], ".")
	}
	return cleanContact(c)
}

var sectorNames = map[string]string{
	"comercio": "comercio", "restaurantes": "restaurante", "servicios": "servicios",
	"manufactura": "manufactura", "logistica": "logística", "salud": "salud", "otro": "otro giro",
}

func ruleSummary(a recommend.Answers) string {
	if len(a.Sectors) == 0 && len(a.Modules) == 0 {
		return "Necesitamos un poco más de detalle para recomendarte con precisión; mientras, te mostramos lo esencial."
	}
	parts := []string{}
	for _, s := range a.Sectors {
		parts = append(parts, sectorNames[s])
	}
	out := "Entendimos un negocio de " + strings.Join(parts, " y ")
	if len(parts) == 0 {
		out = "Entendimos tu negocio"
	}
	if len(a.Modules) > 0 {
		names := []string{}
		for _, m := range a.Modules {
			if mod, ok := Modules[m]; ok {
				names = append(names, strings.ToLower(mod.Name))
			}
		}
		out += " que necesita " + strings.Join(names, ", ")
	}
	return out + "."
}
