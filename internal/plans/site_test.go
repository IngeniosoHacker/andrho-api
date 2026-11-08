package plans

import (
	"errors"
	"testing"
)

func TestSiteOptions(t *testing.T) {
	// No site: every level at full price.
	o := SiteOptions("despegue")
	if len(o) != 3 || o[0].Included || o[0].Setup != 1500 || o[0].Monthly != 150 || o[2].Setup != 4000 || o[2].Monthly != 400 {
		t.Fatalf("despegue %+v", o)
	}
	// En órbita includes the store; pro costs the difference.
	o = SiteOptions("en_orbita")
	if len(o) != 2 || !o[0].Included || o[0].ID != "tienda" || o[0].Setup != 0 || !o[1].Upgrade || o[1].Setup != 1500 || o[1].Monthly != 150 {
		t.Fatalf("en_orbita %+v", o)
	}
	// Galaxy includes pro: nothing to buy.
	if o = SiteOptions("galaxy"); len(o) != 1 || !o[0].Included || o[0].ID != "pro" {
		t.Fatalf("galaxy %+v", o)
	}
}

func TestQuoteWithSite(t *testing.T) {
	q, err := CalculateAddonPrice("base", []Selection{{FeatureID: "sitio_basico"}})
	if err != nil || q.Total != 120+150 || q.SetupTotal != 1500 || len(q.Setup) != 1 || q.UpgradeIsCheaper {
		t.Fatalf("base %+v %v", q, err)
	}
	q, err = CalculateAddonPrice("en_orbita", []Selection{{FeatureID: "sitio_pro"}})
	if err != nil || q.Total != 1200+150 || q.SetupTotal != 1500 {
		t.Fatalf("pro on en_orbita %+v %v", q, err)
	}
	for _, c := range []struct{ plan, site string }{{"en_orbita", "sitio_tienda"}, {"en_orbita", "sitio_basico"}, {"galaxy", "sitio_pro"}} {
		if _, err := CalculateAddonPrice(c.plan, []Selection{{FeatureID: c.site}}); !errors.Is(err, ErrNotOffered) {
			t.Errorf("%s %s: %v", c.plan, c.site, err)
		}
	}
	if _, err := CalculateAddonPrice("base", []Selection{{FeatureID: "sitio_basico"}, {FeatureID: "sitio_pro"}}); !errors.Is(err, ErrQty) {
		t.Errorf("two sites: %v", err)
	}
}

func TestEffectiveSite(t *testing.T) {
	if c := Effective("base", []Selection{{FeatureID: "sitio_tienda"}}); !c.Website || !c.Store || c.FullSite {
		t.Fatalf("%+v", c)
	}
	if c := Effective("en_orbita", []Selection{{FeatureID: "sitio_pro"}}); !c.FullSite {
		t.Fatalf("%+v", c)
	}
	// Bought before the site was a product.
	if c := Effective("plataforma", []Selection{{FeatureID: "sitio_web_basico"}}); !c.Website {
		t.Fatalf("legacy %+v", c)
	}
	if c := Effective("despegue", nil); c.Website {
		t.Fatalf("despegue no longer includes a site: %+v", c)
	}
}

func TestBudgets(t *testing.T) {
	b := Budgets()
	if len(b) != 6 || b[0].ID != "hasta_120" || b[0].Max != 120 || b[1].ID != "120_349" || b[1].Label != "De Q120 a Q348" ||
		b[5].ID != "desde_2990" || b[5].Max != 0 || b[5].Label != "Q2,990 o más" {
		t.Fatalf("%+v", b)
	}
	if _, ok := GetBudget("750_1200"); !ok {
		t.Fatal("750_1200")
	}
}
