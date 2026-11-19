package handlers

import (
	"slices"
	"testing"
)

func TestParseCaption(t *testing.T) {
	cases := []struct{ in, name, code string }{
		{"Juan Pérez 123", "Juan Pérez", "123"},
		{"Código 45, María López", "María López", "45"},
		{"cod: A-17 Pedro", "Pedro", "A-17"},
		{"Ana", "Ana", ""},
		{"", "", ""},
		{"#88", "", "88"},
	}
	for _, c := range cases {
		n, k := parseCaption(c.in)
		if n != c.name || k != c.code {
			t.Errorf("%q: got (%q, %q) want (%q, %q)", c.in, n, k, c.name, c.code)
		}
	}
}

func TestParseSimple(t *testing.T) {
	in := parseSimple("Cuadrilla Losa Norte: Juan Pérez, Pedro Gómez y Luis, en el nivel 3, armado de losa")
	if in.Intent != "crew" || in.CrewName != "Losa Norte" || !slices.Equal(in.Members, []string{"Juan Pérez", "Pedro Gómez", "Luis"}) ||
		in.Zone != "nivel 3" || in.Activity != "armado de losa" {
		t.Fatalf("%+v", in)
	}
	if in := parseSimple("Baja de Juan Pérez"); in.Intent != "terminate" || in.WorkerName != "Juan Pérez" {
		t.Fatalf("%+v", in)
	}
	if in := parseSimple("dar de baja a código 45"); in.Intent != "terminate" || in.WorkerCode != "45" {
		t.Fatalf("%+v", in)
	}
	if in := parseSimple("¿cómo va todo?"); in.Intent != "other" {
		t.Fatalf("%+v", in)
	}
}

// Claude may only copy from the message: anything else is dropped.
func TestGrounded(t *testing.T) {
	msg := "hoy la cuadrilla de juan y pedro está en la torre B"
	got := grounded(msg, wfIntent{Intent: "crew", CrewName: "Cuadrilla Alfa", Members: []string{"Juan", "Pedro", "Mario"},
		Zone: "torre B", Activity: "colando concreto"})
	if got.CrewName != "" || !slices.Equal(got.Members, []string{"Juan", "Pedro"}) || got.Zone != "torre B" || got.Activity != "" {
		t.Fatalf("%+v", got)
	}
}
