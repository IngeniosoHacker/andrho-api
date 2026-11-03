package sites

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHost(t *testing.T) {
	c := Config{HostPattern: "web.{id}.andrho.com"}
	if h, err := c.Host("panaderia-ana"); err != nil || h != "web.panaderia-ana.andrho.com" {
		t.Fatalf("got %q, %v", h, err)
	}
	if _, err := c.Host("../x"); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestSetupWithoutCredentialsSaysWhatsMissing(t *testing.T) {
	st, err := Config{}.Setup(context.Background(), "web.x.andrho.com")
	if err != nil || !strings.HasPrefix(st, "pendiente") {
		t.Fatalf("got %q, %v", st, err)
	}
	st, _ = Config{Wildcard: true}.Setup(context.Background(), "x.web.andrho.com")
	if !strings.HasPrefix(st, "listo") {
		t.Fatalf("wildcard: %q", st)
	}
}

// Railway answers with the records it needs; each one is created in Porkbun.
func TestSetupCreatesRailwayDomainAndDNS(t *testing.T) {
	var created []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql":
			if r.Header.Get("Authorization") != "Bearer rt" {
				w.WriteHeader(401)
				return
			}
			var body struct {
				Variables struct {
					Input map[string]string `json:"input"`
				} `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Variables.Input["domain"] != "web.acme.andrho.com" || body.Variables.Input["serviceId"] != "svc" {
				t.Errorf("railway input: %v", body.Variables.Input)
			}
			_, _ = w.Write([]byte(`{"data":{"customDomainCreate":{"status":{"dnsRecords":[
				{"recordType":"DNS_RECORD_TYPE_CNAME","hostlabel":"web.acme","fqdn":"web.acme.andrho.com","requiredValue":"abc.up.railway.app"},
				{"recordType":"DNS_RECORD_TYPE_TXT","hostlabel":"_railway-verify.web.acme","fqdn":"_railway-verify.web.acme.andrho.com","requiredValue":"railway-verify=123"}]}}}}`))
		case strings.HasPrefix(r.URL.Path, "/dns/create/andrho.com"):
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			created = append(created, b)
			_, _ = w.Write([]byte(`{"status":"SUCCESS","id":1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := Config{DNSZone: "andrho.com", RailwayToken: "rt", RailwayService: "svc", RailwayEnv: "env",
		PorkbunKey: "pk", PorkbunSecret: "sk", HTTP: srv.Client(), RailwayURL: srv.URL + "/graphql", PorkbunURL: srv.URL}
	st, err := c.Setup(context.Background(), "web.acme.andrho.com")
	if err != nil || !strings.HasPrefix(st, "listo") {
		t.Fatalf("got %q, %v", st, err)
	}
	if len(created) != 2 || created[0]["name"] != "web.acme" || created[0]["type"] != "CNAME" ||
		created[0]["content"] != "abc.up.railway.app" || created[1]["name"] != "_railway-verify.web.acme" {
		t.Fatalf("porkbun records: %v", created)
	}
}
