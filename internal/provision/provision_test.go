package provision

import "testing"

func TestFillFromModules(t *testing.T) {
	r := Request{}
	r.Fill([]string{"analitica", "inventario", "whatsapp", "sitio_web", "redes"}, " +502 5555 5555 ")
	if r.ERP != "yes" || r.WhatsApp != "+502 5555 5555" || r.WebPage == nil || !*r.WebPage || r.Meta != "pendiente" {
		t.Errorf("Fill = %+v", r)
	}

	// Explicit values win over matches.
	no := false
	r = Request{ERP: "no", WhatsApp: "+50211112222", WebPage: &no}
	r.Fill([]string{"inventario", "whatsapp", "sitio_web"}, "+50299998888")
	if r.ERP != "no" || r.WhatsApp != "+50211112222" || *r.WebPage {
		t.Errorf("explicit values were overwritten: %+v", r)
	}

	// Nothing asks for anything.
	r = Request{ERP: "Sí"}
	r.Fill([]string{"analitica", "marketing"}, "+50255555555")
	if r.ERP != "yes" || r.WhatsApp != "" || r.WebPage != nil || r.Meta != "" {
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
