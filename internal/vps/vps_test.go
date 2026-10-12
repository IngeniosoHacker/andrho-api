package vps

import "testing"

func TestQuoteAndHostKeyLine(t *testing.T) {
	if got := quote("a'b"); got != `'a'\''b'` {
		t.Errorf("quote = %s", got)
	}
	if got := fieldsAfterHost("vps.andrho.com ssh-ed25519 AAAAC3Nza"); got != "* ssh-ed25519 AAAAC3Nza" {
		t.Errorf("host line = %s", got)
	}
	if got := fieldsAfterHost("ssh-ed25519 AAAAC3Nza"); got != "* ssh-ed25519 AAAAC3Nza" {
		t.Errorf("key line = %s", got)
	}
	if LastJSONLine("starting\n{\"a\":1}\n") != `{"a":1}` {
		t.Error("LastJSONLine")
	}
}

func TestDisabledAndBadInput(t *testing.T) {
	r, err := New(Config{})
	if err != nil || r.Enabled() {
		t.Fatalf("empty config should give a disabled runner")
	}
	if _, err := New(Config{Host: "x", PrivateKey: "nope"}); err == nil {
		t.Error("a bad key must fail at boot")
	}
	if !safeArg.MatchString("acme-shop") || safeArg.MatchString("x; rm -rf /") || safeArg.MatchString("$(id)") || !safeArg.MatchString("Panadería Doña Ana, S.A.") {
		t.Error("safeArg")
	}
}
