package plans

import "fmt"

// The website is its own product, not a plan feature: building it is
// charged once (Setup) and keeping it up is a monthly fee (Monthly) added
// to whatever plan the business has. Three levels; En órbita includes
// "tienda" and Galaxy "pro" (construction and upkeep). On a plan that
// includes a level, a higher one costs the difference ("sitio pro" on En
// órbita: Q1,500 once + Q150/mes).
//
// A level is bought like an add-on (account_plan.addons, FeatureID
// "sitio_basico" | "sitio_tienda" | "sitio_pro"), but on any plan.

// SiteLevel is one level of the website.
type SiteLevel struct {
	ID        string `json:"id"` // basico | tienda | pro
	FeatureID string `json:"feature_id"`
	Label     string `json:"label"`
	Detail    string `json:"detail"`
	Setup     int    `json:"setup"`   // quetzales, once
	Monthly   int    `json:"monthly"` // quetzales al mes (mantenimiento)
	rank      int
}

// SiteLevels, ascending. The prices are the catalog's; nothing else writes them.
var SiteLevels = []SiteLevel{
	{ID: "basico", FeatureID: "sitio_basico", Label: "Sitio web + SEO básico", rank: 1, Setup: 1500, Monthly: 150,
		Detail: "Tu sitio en <nombre>.web.andrho.com, hecho por Creators y listo para Google."},
	{ID: "tienda", FeatureID: "sitio_tienda", Label: "Sitio web con tienda en línea", rank: 2, Setup: 2500, Monthly: 250,
		Detail: "Tu sitio y tu tienda: tus productos se venden en línea y aparecen en Google."},
	{ID: "pro", FeatureID: "sitio_pro", Label: "Sitio pro", rank: 3, Setup: 4000, Monthly: 400,
		Detail: "Tienda, una página por sucursal y por servicio, y blog o landings de Creators."},
}

// legacySite maps the add-ons that sold the site before it was a product.
var legacySite = map[string]string{"sitio_web_basico": "sitio_basico", "tienda_en_linea": "sitio_tienda", "sitio_completo": "sitio_pro"}

func siteByFeature(featureID string) (SiteLevel, bool) {
	if to, ok := legacySite[featureID]; ok {
		featureID = to
	}
	for _, l := range SiteLevels {
		if l.FeatureID == featureID {
			return l, true
		}
	}
	return SiteLevel{}, false
}

// IsSite says whether a feature id is a website level.
func IsSite(featureID string) bool {
	_, ok := siteByFeature(featureID)
	return ok
}

// siteRank is the level capabilities give (0 = no site).
func siteRank(c Capabilities) int {
	switch {
	case c.FullSite:
		return 3
	case c.Store:
		return 2
	case c.Website:
		return 1
	}
	return 0
}

// IncludedSite is the level a plan includes (false when none).
func IncludedSite(planID string) (SiteLevel, bool) {
	r := siteRank(MustGet(planID).Caps)
	if r == 0 {
		return SiteLevel{}, false
	}
	return SiteLevels[r-1], true
}

func (l SiteLevel) applyTo(c *Capabilities) {
	c.Website = true
	c.Store = c.Store || l.rank >= 2
	c.FullSite = c.FullSite || l.rank >= 3
}

// SiteOption is a level as offered on a plan: what it costs there.
type SiteOption struct {
	SiteLevel
	// Included: the plan already has it (no charge). Otherwise Setup and
	// Monthly are what this plan pays (the difference over what it includes).
	Included bool `json:"included"`
	Upgrade  bool `json:"upgrade"` // over a level the plan includes
}

// SiteOptions lists the levels for planID: the included one (if any) and
// the higher ones with their price on this plan.
func SiteOptions(planID string) []SiteOption {
	inc, has := IncludedSite(planID)
	out := []SiteOption{}
	for _, l := range SiteLevels {
		switch {
		case has && l.rank < inc.rank:
			continue
		case has && l.rank == inc.rank:
			o := SiteOption{SiteLevel: l, Included: true}
			o.Setup, o.Monthly = 0, 0
			out = append(out, o)
		default:
			o := SiteOption{SiteLevel: l, Upgrade: has}
			if has {
				o.Setup, o.Monthly = l.Setup-inc.Setup, l.Monthly-inc.Monthly
			}
			out = append(out, o)
		}
	}
	return out
}

// siteOffer prices a site level on planID (an error when the plan already
// includes it or more).
func siteOffer(planID, featureID string) (SiteOption, error) {
	l, ok := siteByFeature(featureID)
	if !ok {
		return SiteOption{}, fmt.Errorf("%w: %q", ErrNotOffered, featureID)
	}
	for _, o := range SiteOptions(planID) {
		if o.ID == l.ID && !o.Included {
			return o, nil
		}
	}
	return SiteOption{}, fmt.Errorf("%w: %s ya viene en %s", ErrNotOffered, featureID, Normalize(planID))
}
