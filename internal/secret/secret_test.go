package secret

import "testing"

func TestSealOpen(t *testing.T) {
	b, _ := New("k1")
	sealed, err := b.Seal("contraseña-odoo")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Open(sealed); err != nil || got != "contraseña-odoo" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	other, _ := New("k2")
	if _, err := other.Open(sealed); err == nil {
		t.Error("a different key must not open it")
	}
	if _, err := New(""); err == nil {
		t.Error("empty key must fail")
	}
}
