package plans

import (
	"errors"
	"strings"
	"testing"
)

// Every row of the table in todo.md C3: step, feature, weight and price
// (the website rows left the table for site.go; their weight is Reserved).
func TestAddonTableMatchesC3(t *testing.T) {
	want := []struct {
		from, feature string
		weight        float64
		price         int
	}{
		{"base", "usuario_extra", 1, 60},
		{"base", "facebook_instagram", 3, 180},
		{"base", "soporte_chat", 1, 60},
		{"plataforma", "sucursal_extra", 1, 50},
		{"plataforma", "usuario_extra", 0.5, 25},
		{"plataforma", "whatsapp_avisos", 2, 100},
		{"plataforma", "creator_task", 3, 150},
		{"despegue", "sucursal_extra", 0.5, 30},
		{"despegue", "usuario_extra", 0.25, 15},
		{"despegue", "whatsapp_control", 3, 160},
		{"despegue", "creator_task", 2.5, 130},
		{"despegue", "soporte_vivo", 1, 55},
		{"en_orbita", "sucursales_ilimitadas", 2, 295},
		{"en_orbita", "usuarios_ilimitados", 2, 295},
		{"en_orbita", "whatsapp_propio", 4, 585},
		{"en_orbita", "creator_task", 4, 585},
		{"en_orbita", "soporte_vivo", 1, 150},
	}
	n := 0
	for _, s := range steps {
		n += len(s.Addons)
	}
	if n != len(want) {
		t.Fatalf("catalog has %d add-ons, table %d", n, len(want))
	}
	for _, w := range want {
		var got *Addon
		for _, a := range Addons(w.from) {
			if a.FeatureID == w.feature {
				a := a
				got = &a
			}
		}
		if got == nil {
			t.Errorf("%s: %s missing", w.from, w.feature)
			continue
		}
		if got.WeightValue() != w.weight || got.Price != w.price {
			t.Errorf("%s %s: weight %v price Q%d, want %v Q%d", w.from, w.feature, got.WeightValue(), got.Price, w.weight, w.price)
		}
	}
}

func TestCatalogC1(t *testing.T) {
	want := map[string]struct {
		price, branches, users, tasks int
	}{
		"base": {120, 1, 1, 0}, "plataforma": {349, 1, 2, 0}, "despegue": {750, 3, 6, 1},
		"en_orbita": {1200, 6, 15, 2}, "galaxy": {2990, Unlimited, Unlimited, 4},
	}
	for id, w := range want {
		p, ok := Get(id)
		if !ok || p.Price != w.price || p.Caps.Branches != w.branches || p.Caps.Users != w.users || p.Caps.CreatorTasks != w.tasks {
			t.Errorf("%s: %+v", id, p)
		}
	}
	if Normalize("galactico") != "galaxy" || Normalize(" Despegue ") != "despegue" || Normalize("nada") != "" {
		t.Fatal("normalize")
	}
	if MustGet("galaxy").Caps.SEOLevel() != "completo" || MustGet("en_orbita").Caps.SEOLevel() != "tienda" ||
		MustGet("despegue").Caps.SEOLevel() != "" || MustGet("plataforma").Caps.SEOLevel() != "" {
		t.Fatal("seo levels")
	}
}

func TestAddonPriceRounding(t *testing.T) {
	// Exact multiples stay (130), anything above rounds up to the next Q5.
	if AddonPrice(450, 10, 45) != 130 || AddonPrice(450, 2, 45) != 30 || AddonPrice(1790, 8, 64) != 295 {
		t.Fatal("rounding")
	}
}

func TestCalculateAddonPrice(t *testing.T) {
	q, err := CalculateAddonPrice("plataforma", []Selection{{FeatureID: "whatsapp_avisos"}, {FeatureID: "usuario_extra", Qty: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if q.BasePrice != 349 || q.AddonsTotal != 150 || q.Total != 499 || q.NextPlan != "despegue" || q.NextPlanPrice != 750 || q.UpgradeIsCheaper || q.SetupTotal != 0 {
		t.Fatalf("%+v", q)
	}
	if len(q.Addons) != 2 || q.Addons[1].FeatureID != "usuario_extra" || q.Addons[1].Qty != 2 || q.Addons[1].Price != 50 {
		t.Fatalf("lines %+v", q.Addons)
	}

	// Nothing selected: the plan alone, upgrading isn't cheaper.
	q, _ = CalculateAddonPrice("base", nil)
	if q.Total != 120 || q.AddonsTotal != 0 || q.UpgradeIsCheaper {
		t.Fatalf("empty %+v", q)
	}
}

func TestUpgradeIsCheaperBothWays(t *testing.T) {
	// Despegue + WhatsApp control + Creator + soporte = 750+160+130+55 = 1095 < 1200.
	q, err := CalculateAddonPrice("despegue", []Selection{{FeatureID: "whatsapp_control"}, {FeatureID: "creator_task"}, {FeatureID: "soporte_vivo"}})
	if err != nil || q.Total != 1095 || q.UpgradeIsCheaper {
		t.Fatalf("%+v %v", q, err)
	}
	// + 9 users (135) = 1230 ≥ 1200.
	q, _ = CalculateAddonPrice("despegue", []Selection{{FeatureID: "whatsapp_control"}, {FeatureID: "creator_task"}, {FeatureID: "soporte_vivo"}, {FeatureID: "usuario_extra", Qty: 9}})
	if q.Total != 1230 || !q.UpgradeIsCheaper {
		t.Fatalf("%+v", q)
	}
	// The site's upkeep doesn't make moving up "cheaper": En órbita includes it, but it isn't a step add-on.
	q, _ = CalculateAddonPrice("despegue", []Selection{{FeatureID: "whatsapp_control"}, {FeatureID: "sitio_pro"}})
	if q.UpgradeIsCheaper || q.Total != 750+160+400 {
		t.Fatalf("site %+v", q)
	}
	// Exactly the next price counts as cheaper (>=): base + all three = 120+300 = 420 ≥ 349.
	q, _ = CalculateAddonPrice("base", []Selection{{FeatureID: "usuario_extra"}, {FeatureID: "facebook_instagram"}, {FeatureID: "soporte_chat"}})
	if !q.UpgradeIsCheaper {
		t.Fatalf("%+v", q)
	}
	// Buying every add-on of every step separately is always dearer than the next plan.
	for _, s := range steps {
		sel := []Selection{}
		for _, a := range s.Addons {
			sel = append(sel, Selection{FeatureID: a.FeatureID, Qty: a.MaxQty})
		}
		q, err := CalculateAddonPrice(s.From, sel)
		if err != nil || !q.UpgradeIsCheaper {
			t.Errorf("%s all add-ons: %+v %v", s.From, q, err)
		}
	}
}

func TestQuantityCaps(t *testing.T) {
	if _, err := CalculateAddonPrice("despegue", []Selection{{FeatureID: "usuario_extra", Qty: 9}}); err != nil {
		t.Fatal(err)
	}
	if _, err := CalculateAddonPrice("despegue", []Selection{{FeatureID: "usuario_extra", Qty: 10}}); !errors.Is(err, ErrQty) {
		t.Fatalf("10 users: %v", err)
	}
	// Repeating a feature adds up against the same cap.
	if _, err := CalculateAddonPrice("plataforma", []Selection{{FeatureID: "sucursal_extra"}, {FeatureID: "sucursal_extra", Qty: 2}}); !errors.Is(err, ErrQty) {
		t.Fatalf("3 branches: %v", err)
	}
	if _, err := CalculateAddonPrice("plataforma", []Selection{{FeatureID: "sucursal_extra", Qty: -1}}); !errors.Is(err, ErrQty) {
		t.Fatalf("negative: %v", err)
	}
	// A yes/no feature can't be bought twice.
	if _, err := CalculateAddonPrice("base", []Selection{{FeatureID: "facebook_instagram", Qty: 2}}); !errors.Is(err, ErrQty) {
		t.Fatalf("twice: %v", err)
	}
}

func TestOnlyNextPlanAddons(t *testing.T) {
	// WhatsApp control is two plans above Plataforma.
	if _, err := CalculateAddonPrice("plataforma", []Selection{{FeatureID: "whatsapp_control"}}); !errors.Is(err, ErrNotOffered) {
		t.Fatalf("got %v", err)
	}
	if _, err := CalculateAddonPrice("nada", nil); !errors.Is(err, ErrUnknownPlan) {
		t.Fatalf("got %v", err)
	}
}

func TestGalaxyHasNoNextPlan(t *testing.T) {
	next, feats, err := NextPlanFeatures("galaxy", nil)
	if err != nil || next != nil || len(feats) != 0 {
		t.Fatalf("%v %v %v", next, feats, err)
	}
	q, err := CalculateAddonPrice("galaxy", nil)
	if err != nil || q.Total != 2990 || q.NextPlan != "" || q.UpgradeIsCheaper {
		t.Fatalf("%+v %v", q, err)
	}
	if _, err := CalculateAddonPrice("galaxy", []Selection{{FeatureID: "creator_task"}}); !errors.Is(err, ErrNotOffered) {
		t.Fatalf("got %v", err)
	}
	// The legacy id works the same.
	if next, _, _ := NextPlanFeatures("galactico", nil); next != nil {
		t.Fatal("galactico has a next plan")
	}
}

// Requirements: met by the plan, the feature is selectable; missing, it
// isn't and the quote refuses it, saying what's missing.
func TestDependencies(t *testing.T) {
	_, feats, _ := NextPlanFeatures("despegue", nil)
	for _, f := range feats {
		if f.FeatureID == "whatsapp_control" && (!f.Selectable || len(f.Requires) != 1 || !f.Requires[0].Met) {
			t.Fatalf("control on despegue: %+v", f)
		}
	}
	saved := catalog[index["despegue"]].Caps
	defer func() { catalog[index["despegue"]].Caps = saved }()
	catalog[index["despegue"]].Caps.WhatsApp = ""
	_, feats, _ = NextPlanFeatures("despegue", nil)
	for _, f := range feats {
		switch f.FeatureID {
		case "whatsapp_control":
			if f.Selectable || f.Requires[0].Met || f.Requires[0].Label == "" {
				t.Errorf("%s: %+v", f.FeatureID, f)
			}
		case "creator_task":
			if !f.Selectable {
				t.Errorf("creator_task not selectable")
			}
		}
	}
	if _, err := CalculateAddonPrice("despegue", []Selection{{FeatureID: "whatsapp_control"}}); !errors.Is(err, ErrMissingRequire) {
		t.Fatalf("got %v", err)
	}
}

func TestNextPlanFeaturesOnlyDifferencesNeededFirst(t *testing.T) {
	next, feats, err := NextPlanFeatures("plataforma", []string{"analitica", "whatsapp", "creators"})
	if err != nil || next == nil || next.ID != "despegue" {
		t.Fatalf("%v %v", next, err)
	}
	if len(feats) != 4 {
		t.Fatalf("got %d features", len(feats))
	}
	if feats[0].FeatureID != "whatsapp_avisos" || feats[1].FeatureID != "creator_task" || !feats[0].Needed || !feats[1].Needed || feats[2].Needed {
		t.Fatalf("order: %s %s %s", feats[0].FeatureID, feats[1].FeatureID, feats[2].FeatureID)
	}
	// The rest keep the table's order.
	if feats[2].FeatureID != "sucursal_extra" || feats[3].FeatureID != "usuario_extra" {
		t.Fatalf("rest: %+v", feats)
	}
	if feats[0].Addon == nil || feats[0].Addon.Price != 100 || feats[2].Addon.MaxQty != 2 || !feats[2].Addon.PerUnit {
		t.Fatalf("addon %+v %+v", feats[0].Addon, feats[2].Addon)
	}
}

func TestEffective(t *testing.T) {
	c := Effective("plataforma", []Selection{{FeatureID: "sitio_basico"}, {FeatureID: "creator_task"}, {FeatureID: "usuario_extra", Qty: 3}})
	if !c.Website || c.CreatorTasks != 1 || c.Users != 5 {
		t.Fatalf("%+v", c)
	}
	if c := Effective("plataforma", nil); c.Website || c.CreatorTasks != 0 {
		t.Fatalf("%+v", c)
	}
	// An add-on from a lower plan never lowers what a higher plan gives.
	if c := Effective("galaxy", []Selection{{FeatureID: "usuario_extra"}, {FeatureID: "whatsapp_control"}}); c.Users != Unlimited || c.WhatsAppCommands != 1500 {
		t.Fatalf("%+v", c)
	}
	if c := Effective("en_orbita", []Selection{{FeatureID: "creator_task"}}); c.CreatorTasks != 4 {
		t.Fatalf("en_orbita +2: %+v", c)
	}
}

func TestIncludes(t *testing.T) {
	got := Includes(MustGet("galaxy"))
	if got[0] != "Sucursales ilimitadas" || got[1] != "Usuarios ilimitados" {
		t.Fatalf("%v", got)
	}
	if got[2] != "Factura electrónica FEL" {
		t.Fatalf("galaxy without FEL: %v", got)
	}
	for _, line := range Includes(MustGet("base")) {
		if strings.Contains(line, "FEL") {
			t.Fatalf("base must not include FEL: %v", line)
		}
	}
	if !MustGet("plataforma").Caps.FEL || MustGet("base").Caps.FEL {
		t.Fatal("FEL starts at plataforma")
	}
	if FormatQ(2990) != "Q2,990" || FormatQ(120) != "Q120" {
		t.Fatal("FormatQ")
	}
}
