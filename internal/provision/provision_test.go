package provision

import (
	"slices"
	"testing"
)

func TestFillFromModules(t *testing.T) {
	r := Request{}
	r.Fill([]string{"analitica", "inventario", "whatsapp", "sitio_web", "redes"}, " +502 5555 5555 ")
	if !slices.Equal(r.OdooModules, []string{"stock"}) || r.WhatsApp != "+502 5555 5555" || r.WebPage == nil || !*r.WebPage || r.Meta != "pendiente" {
		t.Errorf("Fill = %+v", r)
	}

	// Explicit values win over matches.
	no := false
	r = Request{WhatsApp: "+50211112222", WebPage: &no}
	r.Fill([]string{"inventario", "whatsapp", "sitio_web"}, "+50299998888")
	if r.WhatsApp != "+50211112222" || *r.WebPage {
		t.Errorf("explicit values were overwritten: %+v", r)
	}

	// Nothing asks for anything, and the ERP still has inventory.
	r = Request{}
	r.Fill([]string{"analitica", "marketing"}, "+50255555555")
	if !slices.Equal(r.OdooModules, []string{"stock"}) || r.WhatsApp != "" || r.WebPage != nil || r.Meta != "" {
		t.Errorf("Fill = %+v", r)
	}
}

func TestDBMode(t *testing.T) {
	cases := []struct{ mode, plan, want string }{
		{"shared", "galactico", "shared"},
		{"dedicated", "despegue", "dedicated"},
		{"mixed", "galactico", "dedicated"},
		{"mixed", "en_orbita", "shared"},
		{"", "galactico", "shared"},
	}
	for _, c := range cases {
		if got := (&Provisioner{DBMode: c.mode}).dbModeFor(c.plan); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.mode, c.plan, got, c.want)
		}
	}
}

func TestOdooModulesFromSignup(t *testing.T) {
	r := Request{}
	r.Fill([]string{"ventas", "clientes", "compras", "logistica", "rutas", "marketing"}, "")
	want := []string{"stock", "point_of_sale", "crm", "purchase", "stock_delivery"}
	if !slices.Equal(r.OdooModules, want) {
		t.Errorf("OdooModules = %v, want %v", r.OdooModules, want)
	}
}

func TestERPInputModules(t *testing.T) {
	// A job from before the list existed installs inventory.
	if got := (erpInput{DBMode: "shared"}).modules(); !slices.Equal(got, []string{"stock"}) {
		t.Errorf("old job = %v", got)
	}
	// The base is never dropped; bad names never reach the script.
	got := (erpInput{OdooModules: []string{"crm", "stock", "x;rm -rf", "Purchase"}}).modules()
	if !slices.Equal(got, []string{"stock", "crm"}) {
		t.Errorf("modules = %v", got)
	}
}
