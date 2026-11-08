package provision

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/IngeniosoHacker/andrho-api/internal/brand"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

func asset(kind, name, status string, meta any) db.Asset {
	raw, _ := json.Marshal(meta)
	return db.Asset{Kind: kind, Filename: name, Status: status, Meta: raw}
}

func TestAnalyzeData(t *testing.T) {
	status, meta := AnalyzeData("productos.csv", []byte("Código;Nombre;Precio\nA1;Pan;1,50\n"))
	if status != DataAnalyzed || len(meta.Tables) != 1 || !meta.Tables[0].Load {
		t.Fatalf("csv: %s %+v", status, meta)
	}
	status, meta = AnalyzeData("ventas.csv", []byte("Fecha,Cliente,Total\n2026-01-01,Ana,10\n"))
	if status != DataReview || meta.Tables[0].Kind != "sales" {
		t.Fatalf("sales: %s %+v", status, meta)
	}
	status, meta = AnalyzeData("foto.bin", []byte{0, 1, 2, 3})
	if status != DataError || meta.Error == "" {
		t.Fatalf("junk: %s %+v", status, meta)
	}
}

func TestPaletteSources(t *testing.T) {
	logo := asset("logo", "logo.png", "listo", ImageMeta{Palette: []brand.Swatch{{Hex: "#dc1e28", Share: 1}}})
	img := asset("brand_image", "local.jpg", "listo", ImageMeta{Palette: []brand.Swatch{{Hex: "#00a078", Share: 1}}})
	if p, src := Palette(brand.Kit{Palette: []string{"#111111"}}, []db.Asset{logo, img}); src != "elegida" || p[0] != "#111111" {
		t.Errorf("chosen: %v %s", p, src)
	}
	if p, src := Palette(brand.Kit{Colors: "tengo"}, []db.Asset{logo, img}); src != "imagenes" || p[0] != "#00a078" {
		t.Errorf("images: %v %s", p, src)
	}
	if p, src := Palette(brand.Kit{Colors: "logo"}, []db.Asset{logo, img}); src != "logo" || p[0] != "#dc1e28" {
		t.Errorf("logo: %v %s", p, src)
	}
	if p, src := Palette(brand.Kit{Colors: "crear"}, []db.Asset{logo}); src != "" || len(p) != 0 {
		t.Errorf("crear: %v %s", p, src)
	}
}

func TestInstructions(t *testing.T) {
	k := brand.Kit{Logo: "crear", Colors: "crear"}
	got := strings.Join(Instructions(KindBrand, "Pan de Ana", "cálido y de barrio", k, nil), "\n")
	for _, want := range []string{"Diseña el logo de Pan de Ana", "«cálido y de barrio»", "propón una paleta"} {
		if !strings.Contains(got, want) {
			t.Errorf("brand instructions miss %q:\n%s", want, got)
		}
	}

	_, prod := AnalyzeData("inventario.csv", []byte("Código,Nombre,Precio\nA1,Pan,1.50\n"))
	loaded := prod
	loaded.Reports = []TableReport{{Table: prod.Tables[0].Table, Kind: "products"}}
	loaded.Reports[0].Report.Created = 12
	_, sales := AnalyzeData("ventas.csv", []byte("Fecha,Cliente,Total\n2026-01-01,Ana,10\n"))
	got = strings.Join(Instructions(KindData, "Pan de Ana", "", brand.Kit{Data: "tengo"}, []db.Asset{
		asset("data", "inventario.csv", DataLoaded, loaded),
		asset("data", "ventas.csv", DataReview, sales),
	}), "\n")
	for _, want := range []string{"AndRho cargó 12 registros nuevos", "parece ventas (sale.order)", "Total = Total"} {
		if !strings.Contains(got, want) {
			t.Errorf("data instructions miss %q:\n%s", want, got)
		}
	}

	got = strings.Join(Instructions("website", "Pan de Ana", "", brand.Kit{Logo: "despues", Colors: "logo"}, []db.Asset{
		asset("logo", "logo.png", "listo", ImageMeta{Palette: []brand.Swatch{{Hex: "#dc1e28", Share: 1}}}),
	}), "\n")
	if !strings.Contains(got, "#dc1e28") || !strings.Contains(got, "src/index.css") || !strings.Contains(got, "Usa su logo") {
		t.Errorf("website instructions:\n%s", got)
	}
}
