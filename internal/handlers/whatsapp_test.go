package handlers

import (
	"strings"
	"testing"
	"time"
)

func TestWALinkCode(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		c := newCode(6)
		if len(c) != 6 || strings.ContainsAny(c, "01IOL") {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 190 {
		t.Fatal("codes repeat too much")
	}
	if hashCode(" abc123 ") != hashCode("ABC123") {
		t.Fatal("hash must ignore case and spaces")
	}
	for text, want := range map[string]string{
		"Vincular AndRho ABC234":            "ABC234",
		"hola, vincular   andrho xyz789 :)": "xyz789",
		"Pedido AR-7F3K":                    "",
		"vincular andrho ABC":               "",
	} {
		got := ""
		if m := waLinkPattern.FindStringSubmatch(text); m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("%q: got %q want %q", text, got, want)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	if maskPhone("") != "" || maskPhone("50255551234") != "•••• 1234" || maskPhone("123") != "••••" {
		t.Fatal(maskPhone("50255551234"))
	}
}

func TestCodeGuesses(t *testing.T) {
	g := &codeGuesses{bad: map[string][]time.Time{}, max: 3, span: time.Hour}
	now := time.Now()
	for range 3 {
		if g.blocked("502", now) {
			t.Fatal("blocked too soon")
		}
		g.miss("502", now)
	}
	if !g.blocked("502", now) || g.blocked("503", now) {
		t.Fatal("limit per number")
	}
	if g.blocked("502", now.Add(61*time.Minute)) {
		t.Fatal("must reset after the hour")
	}
}
