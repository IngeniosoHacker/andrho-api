package live

import "testing"

func TestNormalizeAndMio(t *testing.T) {
	cases := map[string]string{"¡Mío!! 😍": "mio", "  Blusa  Roja #3 ": "blusa roja 3", "MÍO": "mio", "Ñandú": "nandu"}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, s := range []string{"mio", "Mío", "mío!", "MIO 🙋‍♀️", " mio. "} {
		if !IsMio(s) {
			t.Errorf("IsMio(%q) = false", s)
		}
	}
	for _, s := range []string{"mia", "es mio", "mio?? cuanto", "lo quiero", "mi o", ""} {
		if IsMio(s) {
			t.Errorf("IsMio(%q) = true", s)
		}
	}
}

func TestNames(t *testing.T) {
	got := Names([]Item{{Name: "Blusa de seda roja talla M"}, {Name: "Jeans 32"}, {Name: "¡!"}})
	want := []string{"blusa seda 1", "jeans 2", "articulo 3"}
	for i, w := range want {
		if got[i].LiveName != w {
			t.Errorf("%d: %q, want %q", i, got[i].LiveName, w)
		}
	}
}

func TestOrder(t *testing.T) {
	items := []Item{{ID: 1, Price: 50}, {ID: 2, Price: 300}, {ID: 3, Price: 80}, {ID: 4, Price: 200}, {ID: 5, Price: 20}, {ID: 6, Price: 120}}
	got := Order(items)
	ids := []int{}
	for _, it := range got {
		ids = append(ids, it.ID)
	}
	// opener 200 (4), then cheapest 20 (5), priciest of the rest 120 (6), 50 (1), 80 (3), closer 300 (2).
	want := []int{4, 5, 6, 1, 3, 2}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order %v, want %v", ids, want)
		}
	}
	if len(Order(items[:2])) != 2 || len(Order(nil)) != 0 {
		t.Fatal("short lists")
	}
}

func TestMatch(t *testing.T) {
	av := []Item{{ID: 1, LiveName: "blusa roja 1"}, {ID: 2, LiveName: "blusa azul 2"}, {ID: 12, LiveName: "jeans 12"}}
	cases := []struct {
		comment string
		id      int
		ok      bool
	}{
		{"Blusa roja 1", 1, true},
		{"blusa roja 1!!", 1, true},
		{"2", 2, true},
		{"la 12", 12, true},
		{"#12", 12, true},
		{"yo quiero la blusa azul 2 porfa", 2, true},
		{"blusa", 0, false},
		{"7", 0, false},
		{"blusa roja 1 y blusa azul 2", 0, false},
		{"hola", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		it, ok := Match(c.comment, av)
		if ok != c.ok || it.ID != c.id {
			t.Errorf("Match(%q) = %d,%v want %d,%v", c.comment, it.ID, ok, c.id, c.ok)
		}
	}
}

func TestMessages(t *testing.T) {
	if FirstName("ana MARÍA lópez") != "Ana" || FirstName("") != "" {
		t.Fatal("FirstName")
	}
	if Money(85) != "Q85" || Money(1250.5) != "Q1,250.50" || Money(1234567) != "Q1,234,567" {
		t.Fatalf("Money %s %s", Money(1250.5), Money(1234567))
	}
	if ConfirmText("blusa roja 1", 85) != "¿Confirmas la compra de blusa roja 1 por Q85?" {
		t.Fatal(ConfirmText("blusa roja 1", 85))
	}
	if got := AfterYes("Ana", 10, "5a calle", "zona 1"); got != "¡Listo, Ana! Tu pedido quedó apartado. Como cliente frecuente tienes 10% de descuento. Lo enviamos a 5a calle, zona 1. Si cambió, escríbenos tu nueva dirección." {
		t.Fatal(got)
	}
	if p, ok := Phone("+502 5555-1234"); !ok || p != "5555-1234" {
		t.Fatal(p)
	}
	if _, ok := Phone("1234"); ok {
		t.Fatal("short phone")
	}
	if a, z := Address("5a calle 1-23, zona 1"); a != "5a calle 1-23" || z != "zona 1" {
		t.Fatalf("%q %q", a, z)
	}
	if a, z := Address("Col. El Milagro, Mixco"); a != "Col. El Milagro" || z != "Mixco" {
		t.Fatalf("%q %q", a, z)
	}
	if c := PostCaption(85, "", true); c != "Q85\nAplica descuento para clientes frecuentes.\n\nComenta «Mío» si lo quieres. La primera persona en comentar se lo lleva y le escribimos por Messenger para confirmar." {
		t.Fatal(c)
	}
}
