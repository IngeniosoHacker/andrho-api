package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/brand"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/etl"
	"github.com/IngeniosoHacker/andrho-api/internal/intake"
	"github.com/IngeniosoHacker/andrho-api/internal/matching"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

// The long signup's kit becomes work: a "brand" task when the business
// wants AndRho to make its logo or its colors, a "datos" task when it
// uploads data from its previous system (and, once its ERP is ready, the
// tables that map well are loaded on their own; the Creator reviews them
// and migrates the rest). The files (account_assets) travel with every
// task of the account; the instructions are written when tasks are listed
// (Instructions), so what the business uploads later counts too.

// Task kinds of the kit.
const (
	KindBrand = "brand"
	KindData  = "datos"
)

// Asset statuses of data files.
const (
	DataAnalyzed = "analizado" // something to load once the ERP is ready
	DataLoading  = "cargando"
	DataLoaded   = "cargado"
	DataReview   = "revisar" // a Creator has to look at it
	DataError    = "error"
)

// createTask opens a Creator task, assigned by internal/matching when
// someone fits.
func (p *Provisioner) createTask(ctx context.Context, acc Account, kind, skill, title string, brief any, days int) error {
	raw, _ := json.Marshal(brief)
	due := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	task := db.CreatorTask{ID: uuid.NewString(), AccountID: acc.ID, Kind: kind, Title: title, Brief: raw, Status: "abierta", DueAt: &due}
	if res, found, err := matching.Assign(ctx, p.DB, acc.ID, skill); err == nil && found {
		task.CreatorID, task.Status = &res.UserID, "aceptada"
		task.Match, _ = json.Marshal(res)
	}
	if _, err := db.CreateCreatorTask(ctx, p.DB, task); err != nil {
		return err
	}
	_ = db.LogEvent(ctx, p.DB, uuid.NewString(), acc.ID, nil, "creator_task", "Tarea para Creators: "+title)
	return nil
}

// BrandTask opens the logo/colors task when the business asked AndRho to
// make them (kit "crear").
func (p *Provisioner) BrandTask(ctx context.Context, acc Account, brief Brief) error {
	// Only the long signup (and the panel after it) asks this; the quick
	// signup's "no tengo marca" stays in the website brief, as before.
	if brief.Kit == nil {
		return nil
	}
	k := *brief.Kit
	logo, colors := k.Logo == "crear", k.Colors == "crear"
	var title string
	switch {
	case logo && colors:
		title = "Logo y colores de " + acc.Company
	case logo:
		title = "Logo de " + acc.Company
	case colors:
		title = "Colores de " + acc.Company
	default:
		return nil
	}
	if id, err := db.OpenTaskOfKind(ctx, p.DB, acc.ID, KindBrand); err != nil || id != "" {
		return err
	}
	return p.createTask(ctx, acc, KindBrand, "diseno", title, brief, 7)
}

// DataTask opens the migration task the first time the business uploads
// data (one open at a time: later files join it).
func (p *Provisioner) DataTask(ctx context.Context, acc Account) error {
	if id, err := db.OpenTaskOfKind(ctx, p.DB, acc.ID, KindData); err != nil || id != "" {
		return err
	}
	return p.createTask(ctx, acc, KindData, "erp", "Migrar los datos de "+acc.Company,
		map[string]any{"company": acc.Company, "client_id": acc.ClientID}, 5)
}

// DataMeta is a data file's meta: what intake read and what was loaded.
type DataMeta struct {
	Format  string           `json:"format"`
	Tables  []intake.Mapping `json:"tables"`
	Reports []TableReport    `json:"reports,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// TableReport is the load of one table.
type TableReport struct {
	Table  string     `json:"table"`
	Kind   string     `json:"kind"`
	Report etl.Report `json:"report"`
	Error  string     `json:"error,omitempty"`
}

// AnalyzeData reads an upload and maps its tables to Odoo. The status
// says what happens next: analizado (loaded when the ERP is ready) or
// revisar (nothing AndRho can load on its own; a Creator looks at it).
func AnalyzeData(filename string, data []byte) (string, DataMeta) {
	format, tables, err := intake.Read(filename, data)
	meta := DataMeta{Format: string(format), Tables: []intake.Mapping{}}
	if err != nil {
		meta.Error = "No pudimos leer el archivo: guárdalo como Excel, CSV o JSON."
		if format != intake.Unknown {
			meta.Error = "El archivo no tiene filas con datos que podamos leer."
		}
		return DataError, meta
	}
	status := DataReview
	for _, t := range tables {
		m := intake.Analyze(t, filename)
		if m.Load {
			status = DataAnalyzed
		}
		meta.Tables = append(meta.Tables, m)
	}
	return status, meta
}

// odooFor opens the account's ERP (db.ErrNotFound before it exists).
func (p *Provisioner) odooFor(ctx context.Context, accountID string) (*odoo.Client, error) {
	conn, err := db.GetOdooConnection(ctx, p.DB, accountID)
	if err != nil {
		return nil, err
	}
	pw, err := p.Secrets.Open(conn.Secret)
	if err != nil {
		return nil, err
	}
	return odoo.New(conn.URL, conn.DBName, conn.Login, pw), nil
}

const maxIssues = 50

// MigrateData loads the account's analyzed data files into its ERP: every
// table mapped with high confidence to products, customers or suppliers
// (internal/etl). Without an ERP yet it does nothing; the erp job calls it
// again when the ERP is ready.
func (p *Provisioner) MigrateData(ctx context.Context, acc Account) {
	assets, err := db.ListAssets(ctx, p.DB, acc.ID, "data")
	if err != nil {
		return
	}
	var pending []db.Asset
	for _, a := range assets {
		if a.Status == DataAnalyzed {
			pending = append(pending, a)
		}
	}
	if len(pending) == 0 {
		return
	}
	cl, err := p.odooFor(ctx, acc.ID)
	if err != nil {
		return // no ERP yet
	}
	for _, a := range pending {
		if ok, _ := db.ClaimAsset(ctx, p.DB, a.ID, DataAnalyzed, DataLoading); !ok {
			continue
		}
		p.migrateFile(ctx, cl, acc, a.ID)
	}
}

func (p *Provisioner) migrateFile(ctx context.Context, cl *odoo.Client, acc Account, id string) {
	a, err := db.GetAsset(ctx, p.DB, acc.ID, id)
	if err != nil {
		return
	}
	var meta DataMeta
	_ = json.Unmarshal(a.Meta, &meta)
	_, tables, err := intake.Read(a.Filename, a.Data)
	status := DataLoaded
	if err != nil {
		meta.Error = "No pudimos volver a leer el archivo."
		status = DataError
	}
	byName := map[string]intake.Table{}
	for _, t := range tables {
		byName[t.Name] = t
	}
	var summary []string
	for _, m := range meta.Tables {
		if !m.Load {
			status = worse(status, DataReview)
			continue
		}
		t, ok := byName[m.Table]
		if !ok {
			status = worse(status, DataReview)
			continue
		}
		rows := m.Apply(t)
		recs, issues := etl.Transform(m.Kind, rows)
		rep := etl.Report{Kind: m.Kind, Rows: len(rows), Valid: len(recs), Issues: issues, Preview: []map[string]any{}}
		tr := TableReport{Table: m.Table, Kind: m.Kind}
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
		err := etl.Load(lctx, cl, m.Kind, recs, &rep)
		cancel()
		if err != nil {
			log.Printf("provision: %s migrate %s/%s: %v", acc.ClientID, a.Filename, m.Table, err)
			tr.Error = "La carga se interrumpió; un Creator la termina."
			status = worse(status, DataError)
		}
		if len(rep.Issues) > 0 || t.Truncated {
			status = worse(status, DataReview)
		}
		full, _ := json.Marshal(rep)
		_, _ = p.DB.Exec(ctx, `INSERT INTO etl_runs (id, account_id, user_id, kind, dry_run, filename, report) VALUES ($1, $2, NULL, $3, false, $4, $5)`,
			uuid.NewString(), acc.ID, m.Kind, clip(a.Filename+" · "+m.Table, 200), full)
		if len(rep.Issues) > maxIssues {
			rep.Issues = rep.Issues[:maxIssues]
		}
		tr.Report = rep
		meta.Reports = append(meta.Reports, tr)
		summary = append(summary, strconv.Itoa(rep.Created+rep.Updated)+" "+etl.Labels[m.Kind])
	}
	raw, _ := json.Marshal(meta)
	if err := db.UpdateAsset(ctx, p.DB, a.ID, status, raw); err != nil {
		log.Printf("provision: %s asset %s: %v", acc.ClientID, a.ID, err)
	}
	if len(summary) > 0 {
		_ = db.LogEvent(ctx, p.DB, uuid.NewString(), acc.ID, nil, "erp_import",
			"AndRho cargó a tu ERP los datos de "+a.Filename+": "+strings.Join(summary, ", "))
	}
}

// worse keeps the status that needs more attention.
func worse(a, b string) string {
	rank := map[string]int{DataLoaded: 0, DataReview: 1, DataError: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ImageMeta is an image file's meta.
type ImageMeta struct {
	Palette []brand.Swatch `json:"palette"`
}

// Palette is the brand's palette and where it came from: the colors the
// person kept (elegida), else those of its brand images (imagenes), else
// those of its logo (logo).
func Palette(k brand.Kit, assets []db.Asset) ([]string, string) {
	if len(k.Palette) > 0 {
		return k.Palette, "elegida"
	}
	var images, logo [][]brand.Swatch
	for _, a := range assets {
		var m ImageMeta
		if json.Unmarshal(a.Meta, &m) != nil || len(m.Palette) == 0 {
			continue
		}
		switch a.Kind {
		case "brand_image":
			images = append(images, m.Palette)
		case "logo":
			logo = [][]brand.Swatch{m.Palette} // the latest logo
		}
	}
	if len(images) > 0 && k.Colors != "logo" {
		return brand.Hexes(brand.Merge(images...)), "imagenes"
	}
	if len(logo) > 0 && k.Colors != "crear" {
		return brand.Hexes(brand.Merge(logo...)), "logo"
	}
	return []string{}, ""
}

var paletteSource = map[string]string{"elegida": "la eligió el cliente", "imagenes": "sacada de sus imágenes", "logo": "sacada de su logo"}

// Instructions is what the Creator has to do on a task, in plain steps,
// from the kit and the account's files.
func Instructions(kind, company, essence string, k brand.Kit, assets []db.Asset) []string {
	var logos, images, data []db.Asset
	for _, a := range assets {
		switch a.Kind {
		case "logo":
			logos = append(logos, a)
		case "brand_image":
			images = append(images, a)
		case "data":
			data = append(data, a)
		}
	}
	palette, source := Palette(k, assets)
	var out []string
	add := func(s string, args ...any) { out = append(out, fmt.Sprintf(s, args...)) }
	colors := func() {
		switch {
		case len(palette) > 0:
			add("Usa la paleta de la marca (%s): %s.", paletteSource[source], strings.Join(palette, " "))
		case k.Colors == "crear":
			add("No tiene colores: propón una paleta de 3 a 5 colores (principal, acento y neutros) que vaya con su esencia y entrégala en códigos hex.")
		case k.Colors == "despues":
			add("Sus colores llegan después: si no han llegado al empezar, propón una paleta y avísale.")
		}
		if len(images) > 0 {
			add("Tiene %d %s de su marca adjuntas como referencia.", len(images), plural(len(images), "imagen", "imágenes"))
		}
	}
	switch kind {
	case KindBrand:
		if k.Logo == "crear" || (k.Logo == "" && len(logos) == 0) {
			add("Diseña el logo de %s.", company)
			if essence != "" {
				add("Su esencia, en sus palabras: «%s».", essence)
			}
			add("Entrégalo en SVG y en PNG con fondo transparente, en versión horizontal y en ícono cuadrado.")
		} else if len(logos) > 0 {
			add("Ya tiene logo (adjunto): no lo cambies; construye a partir de él.")
		}
		colors()
		add("Sube los archivos a una carpeta compartida y entrega su enlace.")
	case "website":
		switch {
		case len(logos) > 0:
			add("Usa su logo (adjunto).")
		case k.Logo == "crear":
			add("Su logo lo está haciendo otra tarea de Marca: usa un texto con el nombre mientras llega.")
		case k.Logo == "despues":
			add("Su logo llega después: usa un texto con el nombre mientras llega.")
		}
		colors()
		if len(palette) > 0 {
			add("Pon la paleta en los tokens de color de la plantilla (src/index.css) para que todo el sitio la use.")
		}
	case KindData:
		if len(data) == 0 {
			add("Todavía no ha subido archivos; aparecen aquí cuando lo haga.")
		}
		for _, a := range data {
			var m DataMeta
			_ = json.Unmarshal(a.Meta, &m)
			if m.Error != "" && len(m.Tables) == 0 {
				add("%s: %s Pídele otro formato.", a.Filename, m.Error)
				continue
			}
			loaded := map[string]TableReport{}
			for _, r := range m.Reports {
				loaded[r.Table] = r
			}
			for _, t := range m.Tables {
				where := a.Filename
				if t.Table != "" && !strings.EqualFold(t.Table, strings.TrimSuffix(a.Filename, extOf(a.Filename))) {
					where += " · " + t.Table
				}
				rep, done := loaded[t.Table]
				switch {
				case done && rep.Error != "":
					add("%s → %s (%s): la carga se interrumpió; termínala desde Migración.", where, t.Label, t.Model)
				case done:
					line := fmt.Sprintf("%s → %s (%s): AndRho cargó %s y actualizó %s", where, t.Label, t.Model,
						count(rep.Report.Created, "registro nuevo", "registros nuevos"), count(rep.Report.Updated, "existente", "existentes"))
					if n := len(rep.Report.Issues); n > 0 {
						line += fmt.Sprintf("; %s fuera (los motivos están en Archivos adjuntos): corrígelas y cárgalas a mano.", count(n, "fila quedó", "filas quedaron"))
					} else {
						line += ". Revísalo en el ERP."
					}
					out = append(out, line)
				case t.Load && a.Status == DataAnalyzed:
					add("%s → %s (%s), %d filas: se carga solo cuando su ERP esté listo.", where, t.Label, t.Model, t.Rows)
				case t.Kind != "":
					add("%s → parece %s (%s), %d filas. Confirma las columnas (%s) y mígralo.", where, strings.ToLower(t.Label), t.Model, t.Rows, columnsText(t))
				default:
					add("%s: %d filas sin identificar (columnas: %s). Decide a qué parte del ERP van.", where, t.Rows, strings.Join(firstN(t.Unmapped, 8), ", "))
				}
				if t.Truncated {
					add("%s trae más de %d filas: AndRho leyó las primeras; migra el resto.", where, intake.MaxRows)
				}
			}
		}
		if k.Data == "despues" && len(data) == 0 {
			add("El cliente dijo que sube sus datos después.")
		}
	}
	return out
}

func columnsText(m intake.Mapping) string {
	var parts []string
	for col, f := range m.Columns {
		parts = append(parts, col+" = "+m.Fields[f])
	}
	if len(parts) == 0 {
		return "ninguna reconocida"
	}
	return strings.Join(firstN(sorted(parts), 8), "; ")
}

func sorted(s []string) []string {
	out := append([]string{}, s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func extOf(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

func count(n int, one, many string) string {
	return strconv.Itoa(n) + " " + plural(n, one, many)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
