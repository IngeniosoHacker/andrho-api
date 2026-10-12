package auth

import "testing"

func TestSlugifyAccents(t *testing.T) {
	for in, want := range map[string]string{
		"Panadería Doña Ana": "panaderia-dona-ana",
		"Café Ñandú, S.A.":   "cafe-nandu-s-a",
		"AndRho":             "andrho",
		"¿?":                 "site",
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
