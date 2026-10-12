package analyzer

import (
	"context"
	"testing"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

func TestAnalyzePackages(t *testing.T) {
	a := recommend.Answers{
		Sectors: []string{"restaurantes"}, CompanySize: "1-10", Management: "papel", HasWebsite: "no",
		SocialNetworks: []string{"instagram"}, MessagingApps: []string{"whatsapp"},
		Modules: []string{"inventario", "finanzas", "marketing"}, Expectations: []string{"organizarse", "reducir_perdidas"},
	}
	r := Analyze(a)
	if len(r.Packages) != 3 || r.Recommended != "galactico" {
		t.Fatalf("want 3 packages recommending galactico, got %d / %q", len(r.Packages), r.Recommended)
	}
	if len(r.Needs) < 7 {
		t.Fatalf("a paper-based restaurant should need many modules, got %v", r.Needs)
	}
	for i, p := range r.Packages {
		if p.ModuleLimit >= 0 && len(p.Modules) > p.ModuleLimit {
			t.Errorf("%s holds %d modules, limit %d", p.Key, len(p.Modules), p.ModuleLimit)
		}
		if len(p.Modules)+len(p.Missing) != len(r.Needs) {
			t.Errorf("%s: modules+missing != needs", p.Key)
		}
		if i > 0 && len(p.Modules) < len(r.Packages[i-1].Modules) {
			t.Errorf("%s holds fewer modules than the plan below it", p.Key)
		}
		if p.Recommended != (p.Key == "galactico") {
			t.Errorf("%s recommended=%v", p.Key, p.Recommended)
		}
	}
	if len(r.Packages[2].Missing) != 0 {
		t.Errorf("galactico should leave nothing out: %v", r.Packages[2].Missing)
	}
	if r.Packages[0].Modules[0].Key != "analitica" {
		t.Errorf("the analytics core should come first, got %s", r.Packages[0].Modules[0].Key)
	}
	for _, m := range r.Needs {
		if m.Key == "creators" {
			t.Error("creators is galactico's core, not a module need")
		}
	}
}

func TestDescribeRules(t *testing.T) {
	d := Describe(context.Background(), ai.New("", "", nil),
		"Tengo un restaurante con 8 empleados, llevo todo en un cuaderno y en Excel. Vendemos por WhatsApp e Instagram y "+
			"a domicilio. Se me vencen los ingredientes y quiero vender más. No tengo página web.")
	a := d.Answers
	if d.Source != "reglas" {
		t.Fatalf("source = %q", d.Source)
	}
	checks := map[string]bool{
		"sector restaurantes": contains(a.Sectors, "restaurantes"),
		"size 1-10":           a.CompanySize == "1-10",
		"management varias":   a.Management == "varias",
		"no website":          a.HasWebsite == "no",
		"whatsapp":            contains(a.MessagingApps, "whatsapp"),
		"instagram":           contains(a.SocialNetworks, "instagram"),
		"domicilio":           contains(a.SalesMethods, "domicilio"),
		"vender_mas":          contains(a.Expectations, "vender_mas"),
		"reducir_perdidas":    contains(a.Expectations, "reducir_perdidas"),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("missing %s in %+v", name, a)
		}
	}
	if d.Summary == "" {
		t.Error("empty summary")
	}
}

func TestSanitizeDropsInventedValues(t *testing.T) {
	a := Sanitize(recommend.Answers{
		Sectors: []string{"restaurantes", "astronautas"}, CompanySize: "enorme",
		Expectations: []string{"vender_mas", "organizarse", "automatizar", "mas_clientes"},
	})
	if len(a.Sectors) != 1 || a.CompanySize != "" || len(a.Expectations) != 3 {
		t.Errorf("sanitize = %+v", a)
	}
}
