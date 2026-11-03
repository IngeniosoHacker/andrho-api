package business

import (
	"slices"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		sectors []string
		desc    string
		want    string
	}{
		{[]string{"restaurantes"}, "", "restaurantes"},
		{[]string{"comercio", "restaurantes"}, "", "restaurantes"}, // food wins
		{[]string{"salud"}, "", "servicios"},
		{[]string{"comercio"}, "", "ventas"},
		{[]string{"manufactura"}, "", ""}, // makes what it sells: no type yet
		{nil, "Somos una empresa de plomería y electricidad en zona 10", "servicios"},
		{nil, "Tenemos una pizzería con entregas", "restaurantes"},
		{nil, "Una ferretería en Mixco", "ventas"},
		{nil, "", ""},
		{[]string{"manufactura"}, "fábrica de muebles", ""}, // sectors given: description not used
	}
	for _, c := range cases {
		if got := Classify(c.sectors, c.desc); got != c.want {
			t.Errorf("Classify(%v, %q) = %q, want %q", c.sectors, c.desc, got, c.want)
		}
	}
}

func TestOdooApps(t *testing.T) {
	got := OdooApps("restaurantes", []string{"stock", "point_of_sale"})
	if !slices.Equal(got, []string{"stock", "point_of_sale", "pos_restaurant", "product_expiry", "purchase"}) {
		t.Errorf("restaurantes: %v", got)
	}
	if got := OdooApps("", []string{"stock"}); !slices.Equal(got, []string{"stock"}) {
		t.Errorf("no type: %v", got)
	}
}

func TestProfilesAreComplete(t *testing.T) {
	for _, k := range Keys {
		p, ok := Get(k)
		if !ok || p.Name == "" || len(p.Modules) == 0 || len(p.Sections) == 0 || len(p.Site.Sections) == 0 || p.Site.CTA == "" {
			t.Errorf("profile %q incomplete", k)
		}
	}
}
