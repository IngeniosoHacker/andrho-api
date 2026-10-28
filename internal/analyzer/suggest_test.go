package analyzer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// fakeClaude answers every Messages API call with text and records the
// system prompt it got.
func fakeClaude(t *testing.T, text string, system *string) *ai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			System string `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if system != nil {
			*system = req.System
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": text}},
			"usage":   map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	return ai.New("test-key", "", nil).WithURL(srv.URL)
}

func TestBlend(t *testing.T) {
	rules := []string{"analitica", "erp", "inventario", "sitio_web", "finanzas", "rrhh"}
	claude := []string{"inventario", "analitica", "whatsapp", "vencimientos"}
	got := blend(rules, claude, map[string]bool{"rrhh": true})
	// By average score: in both lists first, then the rest. Everything
	// Claude listed stays, even its last ("vencimientos"); "sitio_web" and
	// "finanzas" (bottom half of the rules only) drop; "rrhh" is pinned.
	want := []string{"analitica", "inventario", "erp", "whatsapp", "vencimientos", "rrhh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blend = %v, want %v", got, want)
	}
}

func TestBlendPlan(t *testing.T) {
	cases := [][3]string{
		{"despegue", "galactico", "en_orbita"},
		{"despegue", "en_orbita", "en_orbita"},
		{"en_orbita", "galactico", "galactico"},
		{"galactico", "despegue", "en_orbita"},
		{"en_orbita", "luna", "en_orbita"},
	}
	for _, c := range cases {
		if got := blendPlan(c[0], c[1]); got != c[2] {
			t.Errorf("blendPlan(%s, %s) = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func restaurant() recommend.Answers {
	return recommend.Answers{
		Sectors: []string{"restaurantes"}, CompanySize: "1-10", Management: "excel", HasWebsite: "no",
		SocialNetworks: []string{"instagram"}, MessagingApps: []string{"whatsapp"},
		Modules: []string{"finanzas"}, Expectations: []string{"reducir_perdidas"},
	}
}

func TestSuggestWithoutAIIsTheRules(t *testing.T) {
	in := SuggestInput{Answers: restaurant()}
	got := Suggest(context.Background(), ai.New("", "", nil), in)
	want := Analyze(Sanitize(restaurant()))
	if got.Source != "reglas" || !reflect.DeepEqual(keysOf(got.Needs), keysOf(want.Needs)) {
		t.Errorf("without AI: source %q needs %v, want the rules' %v", got.Source, keysOf(got.Needs), keysOf(want.Needs))
	}
}

func TestSuggestBlendsClaude(t *testing.T) {
	var system string
	client := fakeClaude(t, `{"summary": "Un restaurante que pierde ingredientes.",
		"modules": [{"key": "vencimientos", "reason": "Dijiste que se te vencen los ingredientes."},
		            {"key": "inventario", "reason": "Para saber qué tienes."},
		            {"key": "creators", "reason": "x"}, {"key": "inventado", "reason": "x"}],
		"plan": "galactico", "plan_reason": "Necesitas control en todo."}`, &system)
	in := SuggestInput{Answers: restaurant(), Description: "Tengo un restaurante y se me vencen los ingredientes."}
	res := Suggest(context.Background(), client, in)

	if res.Source != "ia" || res.Summary == "" {
		t.Fatalf("source %q summary %q", res.Source, res.Summary)
	}
	keys := keysOf(res.Needs)
	if keys[0] != "vencimientos" && keys[0] != "inventario" && keys[0] != "analitica" {
		t.Errorf("Claude's top modules should lead, got %v", keys)
	}
	for _, k := range keys {
		if k == "creators" || k == "inventado" {
			t.Errorf("%s must not be a need: %v", k, keys)
		}
	}
	if !strings.Contains(strings.Join(keys, ","), "finanzas") {
		t.Errorf("an area the business asked for must stay: %v", keys)
	}
	if res.ModuleReasons["vencimientos"] == "" {
		t.Errorf("missing Claude's reason: %v", res.ModuleReasons)
	}
	if len(res.Reasons) != 1 || res.Reasons[0] != "Necesitas control en todo." {
		t.Errorf("reasons = %v", res.Reasons)
	}
	if !strings.Contains(system, "IA con criterio") || !strings.Contains(system, "Tu tarea") {
		t.Error("the system prompt must carry the project context")
	}
}

func TestSuggestFallsBackOnBadAnswer(t *testing.T) {
	res := Suggest(context.Background(), fakeClaude(t, "no sé", nil), SuggestInput{Answers: restaurant()})
	if res.Source != "reglas" || len(res.Needs) == 0 {
		t.Errorf("bad AI answer: source %q, %d needs", res.Source, len(res.Needs))
	}
}

func TestDescribeExtractsBoardingPass(t *testing.T) {
	client := fakeClaude(t, `{"answers": {"sectors": ["restaurantes"], "restaurantExpiry": "a_mano",
		"sectorAnswers": {"restaurantes.pedidos": "mesa, whatsapp, drones", "comercio.inventario": "telepatia"}},
		"contact": {"name": "Ana López", "company": "Pan de Ana", "email": "ana@pan", "phone": "+502 5555 5555"},
		"brand": {"essence": "Cálido y de barrio", "has_brand": true},
		"details": {"currentSoftware": "Excel"}, "summary": "Una panadería."}`, nil)
	d := Describe(context.Background(), client, "Me llamo Ana López, mi panadería Pan de Ana…")
	if d.Contact.Name != "Ana López" || d.Contact.Company != "Pan de Ana" || d.Contact.Phone != "+502 5555 5555" {
		t.Errorf("contact = %+v", d.Contact)
	}
	if d.Contact.Email != "" {
		t.Errorf("an invalid email must be dropped, got %q", d.Contact.Email)
	}
	if d.Brand.HasBrand == nil || !*d.Brand.HasBrand || d.Details.CurrentSoftware != "Excel" {
		t.Errorf("brand %+v details %+v", d.Brand, d.Details)
	}
	sa := d.Answers.SectorAnswers
	if sa["restaurantes.pedidos"] != "mesa,whatsapp" || sa["comercio.inventario"] != "" || d.Answers.RestaurantExp != "a_mano" {
		t.Errorf("sector answers %v, expiry %q", sa, d.Answers.RestaurantExp)
	}
}

func TestRuleContact(t *testing.T) {
	c := ruleContact("Hola, me llamo Ana López. Mi negocio se llama Pan de Ana. Escríbeme a ana@pan.gt o al +502 5555-5555.")
	want := Contact{Name: "Ana López", Company: "Pan de Ana", Email: "ana@pan.gt", Phone: "+502 5555-5555"}
	if c != want {
		t.Errorf("ruleContact = %+v, want %+v", c, want)
	}
}
