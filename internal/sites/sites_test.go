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
	c := Config{HostPattern: "{id}.web.andrho.com"}
	if h, err := c.Host("panaderia-ana"); err != nil || h != "panaderia-ana.web.andrho.com" {
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
	if st != "wildcard" {
		t.Fatalf("wildcard: %q", st)
	}
}

// SITE_WILDCARD=false (a client's own domain): Railway answers with the
// records it needs on sites-router; each one is created in Porkbun.
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
	c := Config{DNSZone: "andrho.com", RailwayToken: "rt", RouterService: "svc", ClientsEnv: "env",
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

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Panadería Doña Ána":    "panaderia-dona-ana",
		"  ¡Café & Té!  ":       "cafe-te",
		"www":                   "sitio-www",
		"API":                   "sitio-api",
		"":                      "sitio",
		"---":                   "sitio",
		"Ñandú_Rápido 2000":     "nandu-rapido-2000",
		strings.Repeat("a", 50): strings.Repeat("a", 40),
		"una empresa con un nombre muy largo de verdad": "una-empresa-con-un-nombre-muy-largo-de-v",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
		if got := Normalize(in); len(got) > MaxSubdomain || !idRe.MatchString(got) {
			t.Errorf("Normalize(%q) = %q is not a valid subdomain", in, got)
		}
	}
	c := Candidates(strings.Repeat("b", 40), 3)
	if c[0] != strings.Repeat("b", 40) || c[1] != strings.Repeat("b", 38)+"-2" || c[2] != strings.Repeat("b", 38)+"-3" {
		t.Fatalf("candidates %v", c)
	}
	if ServiceName("abc") != "site-abc" {
		t.Fatal("service name")
	}
}

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("SITE_HOST_PATTERN", "")
	t.Setenv("SITE_WILDCARD", "")
	t.Setenv("RAILWAY_ROUTER_SERVICE_ID", "")
	t.Setenv("RAILWAY_SITES_SERVICE_ID", "old")
	c := FromEnv()
	if c.HostPattern != "{id}.web.andrho.com" || !c.Wildcard || c.RouterService != "old" || c.TemplateRoot != "sites" || c.Port != 8080 {
		t.Fatalf("%+v", c)
	}
	t.Setenv("SITE_WILDCARD", "false")
	if FromEnv().Wildcard {
		t.Fatal("SITE_WILDCARD=false")
	}
}

// The client's service: found or created by name, configured, connected to
// the template, its public domain removed, and its deployment read.
func TestClientService(t *testing.T) {
	var calls []string
	var vars map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		q := body.Query
		switch {
		case strings.Contains(q, "project(id"):
			calls = append(calls, "find")
			_, _ = w.Write([]byte(`{"data":{"project":{"services":{"edges":[{"node":{"id":"router","name":"sites-router"}}]}}}}`))
		case strings.Contains(q, "serviceCreate"):
			calls = append(calls, "create")
			in := body.Variables["input"].(map[string]any)
			if in["projectId"] != "proj" || in["name"] != "site-acc1" {
				t.Errorf("create input %v", in)
			}
			_, _ = w.Write([]byte(`{"data":{"serviceCreate":{"id":"svc1","name":"site-acc1"}}}`))
		case strings.Contains(q, "variableCollectionUpsert"):
			calls = append(calls, "vars")
			vars = body.Variables["input"].(map[string]any)
			_, _ = w.Write([]byte(`{"data":{"variableCollectionUpsert":true}}`))
		case strings.Contains(q, "serviceInstanceUpdate"):
			calls = append(calls, "root")
			if body.Variables["input"].(map[string]any)["rootDirectory"] != "sites" {
				t.Errorf("root %v", body.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"serviceInstanceUpdate":true}}`))
		case strings.Contains(q, "serviceConnect"):
			calls = append(calls, "connect")
			_, _ = w.Write([]byte(`{"data":{"serviceConnect":{"id":"svc1"}}}`))
		case strings.Contains(q, "domains("):
			calls = append(calls, "domains")
			_, _ = w.Write([]byte(`{"data":{"domains":{"serviceDomains":[{"id":"d1","domain":"site-acc1-production.up.railway.app"}]}}}`))
		case strings.Contains(q, "serviceDomainDelete"):
			calls = append(calls, "delete:"+body.Variables["id"].(string))
			_, _ = w.Write([]byte(`{"data":{"serviceDomainDelete":true}}`))
		case strings.Contains(q, "serviceInstance("):
			calls = append(calls, "status")
			_, _ = w.Write([]byte(`{"data":{"serviceInstance":{"latestDeployment":{"id":"x","status":"SUCCESS"},"source":{"repo":"IngeniosoHacker/andrho-tracker-dashboard"}}}}`))
		default:
			t.Errorf("unexpected query %s", q)
			_, _ = w.Write([]byte(`{"errors":[{"message":"unknown"}]}`))
		}
	}))
	defer srv.Close()
	c := Config{RailwayToken: "rt", ClientsProject: "proj", ClientsEnv: "env", TemplateRepo: "IngeniosoHacker/andrho-tracker-dashboard",
		TemplateBranch: "main", TemplateRoot: "sites", APIURL: "https://api.andrho.com", Port: 8080, HTTP: srv.Client(), RailwayURL: srv.URL}
	ctx := context.Background()
	id, name, err := c.EnsureService(ctx, "acc1")
	if err != nil || id != "svc1" || name != "site-acc1" {
		t.Fatalf("ensure: %q %q %v", id, name, err)
	}
	if err := c.ConfigureService(ctx, id, "panaderia"); err != nil {
		t.Fatal(err)
	}
	v := vars["variables"].(map[string]any)
	if v["SITE_MODE"] != "site" || v["SITE_ID"] != "panaderia" || v["PORT"] != "8080" || vars["skipDeploys"] != true {
		t.Fatalf("vars %v", vars)
	}
	if err := c.Deploy(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if n, err := c.RemovePublicDomains(ctx, id); err != nil || n != 1 {
		t.Fatalf("remove: %d %v", n, err)
	}
	d, err := c.LatestDeployment(ctx, id)
	if err != nil || !d.Done() || !d.Connected {
		t.Fatalf("deployment %+v %v", d, err)
	}
	want := "find create vars root connect domains delete:d1 status"
	if got := strings.Join(calls, " "); got != want {
		t.Fatalf("calls %q, want %q", got, want)
	}
	// Without the Clients project nothing is called.
	if _, _, err := (Config{RailwayToken: "rt"}).EnsureService(ctx, "acc1"); err == nil || !strings.Contains(err.Error(), "RAILWAY_CLIENTS_PROJECT_ID") {
		t.Fatalf("missing config: %v", err)
	}
}
