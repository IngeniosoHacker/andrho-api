package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// graphCall is one request the fake Graph got.
type graphCall struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

type fakeMeta struct {
	mu    sync.Mutex
	calls []graphCall
	// replies by "METHOD /path" (without the version)
	replies map[string]string
	status  map[string]int
}

func (f *fakeMeta) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:] // drop /vXX.0
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, graphCall{r.Method, path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
	key := r.Method + " " + path
	reply, ok := f.replies[key]
	st := f.status[key]
	f.mu.Unlock()
	if st != 0 {
		w.WriteHeader(st)
	}
	if !ok {
		reply = `{"success":true}`
	}
	w.Write([]byte(reply))
}

func (f *fakeMeta) call(t *testing.T, method, path string) graphCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			return c
		}
	}
	t.Fatalf("no %s %s in %+v", method, path, f.calls)
	return graphCall{}
}

func newFake(t *testing.T, replies map[string]string) (*fakeMeta, *App) {
	f := &fakeMeta{replies: replies, status: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	app := NewApp("APP", "SECRET", "v27.0")
	app.Graph = srv.URL
	return f, app
}

func TestExchangeAndDebugToken(t *testing.T) {
	f, app := newFake(t, map[string]string{
		"GET /oauth/access_token": `{"access_token":"BIZTOKEN","token_type":"bearer"}`,
		"GET /debug_token": `{"data":{"is_valid":true,"granular_scopes":[
			{"scope":"whatsapp_business_management","target_ids":["WABA1"]},
			{"scope":"whatsapp_business_messaging","target_ids":["WABA1"]}]}}`,
	})
	ctx := context.Background()
	tok, err := app.ExchangeCode(ctx, "CODE")
	if err != nil || tok != "BIZTOKEN" {
		t.Fatalf("%q %v", tok, err)
	}
	c := f.call(t, "GET", "/oauth/access_token")
	if !strings.Contains(c.Query, "code=CODE") || !strings.Contains(c.Query, "client_id=APP") || strings.Contains(c.Query, "redirect_uri") {
		t.Fatalf("exchange query: %s", c.Query)
	}
	if c.Auth != "" {
		t.Fatalf("the exchange carries no bearer: %q", c.Auth)
	}
	g, err := app.DebugToken(ctx, tok)
	if err != nil || !g.Messaging || len(g.WABAs) != 1 {
		t.Fatalf("%+v %v", g, err)
	}
	if a := f.call(t, "GET", "/debug_token").Auth; a != "Bearer APP|SECRET" {
		t.Fatalf("debug_token uses the app token: %q", a)
	}
	if _, err := app.ExchangeCode(ctx, " "); err == nil {
		t.Fatal("empty code")
	}
}

func TestPickWABAAndPhone(t *testing.T) {
	g := Grants{WABAs: []string{"W1", "W2"}}
	if w, err := PickWABA(g, "", "W2"); err != nil || w != "W2" {
		t.Fatalf("%q %v", w, err)
	}
	if _, err := PickWABA(g, "EVIL"); err == nil {
		t.Fatal("a hint the token doesn't manage must not be trusted, and two WABAs are ambiguous")
	}
	if w, err := PickWABA(Grants{WABAs: []string{"W1", "W1"}}, "EVIL"); err != nil || w != "W1" {
		t.Fatalf("%q %v", w, err)
	}
	if _, err := PickWABA(Grants{}); err == nil {
		t.Fatal("no WABA")
	}
	nums := []PhoneNumber{{ID: "P1"}, {ID: "P2"}}
	if p, err := PickPhone(nums, "P2"); err != nil || p.ID != "P2" {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := PickPhone(nums, "P9"); err == nil {
		t.Fatal("ambiguous")
	}
	if p, err := PickPhone(nums[:1], ""); err != nil || p.ID != "P1" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestAccountCalls(t *testing.T) {
	f, app := newFake(t, map[string]string{
		"GET /PN1":                      `{"id":"PN1","display_phone_number":"+502 5555 0000","verified_name":"Modas Cristi","name_status":"APPROVED","quality_rating":"GREEN","is_on_biz_app":true}`,
		"GET /WABA1":                    `{"id":"WABA1"}`,
		"POST /WABA1/message_templates": `{"id":"T1","status":"PENDING","category":"MARKETING"}`,
	})
	c := ForAccount("BIZTOKEN", "PN1", "WABA1", "v27.0")
	c.Graph = app.Graph
	ctx := context.Background()
	if err := c.SubscribeApp(ctx); err != nil {
		t.Fatal(err)
	}
	if a := f.call(t, "POST", "/WABA1/subscribed_apps").Auth; a != "Bearer BIZTOKEN" {
		t.Fatalf("auth %q", a)
	}
	if err := c.Register(ctx, "123456"); err != nil {
		t.Fatal(err)
	}
	if b := f.call(t, "POST", "/PN1/register").Body; b["pin"] != "123456" || b["messaging_product"] != "whatsapp" {
		t.Fatalf("%v", b)
	}
	if err := c.SyncSMBApp(ctx, "history"); err != nil {
		t.Fatal(err)
	}
	if b := f.call(t, "POST", "/PN1/smb_app_data").Body; b["sync_type"] != "history" {
		t.Fatalf("%v", b)
	}
	info, err := c.PhoneInfo(ctx)
	if err != nil || info.VerifiedName != "Modas Cristi" || !info.IsOnBizApp || info.QualityRating != "GREEN" {
		t.Fatalf("%+v %v", info, err)
	}
	if funded, known := c.Funded(ctx); !known || funded {
		t.Fatalf("no primary_funding_id = no payment method: %v %v", funded, known)
	}
	st, err := c.CreateTemplate(ctx, Template{Name: "andrho_x", Language: "es", Category: "UTILITY", Body: "Hola {{1}}",
		Example: []string{"Ana"}, Buttons: []string{"Sí, lo quiero", "No"}})
	if err != nil || st.Category != "MARKETING" || st.Name != "andrho_x" {
		t.Fatalf("%+v %v", st, err)
	}
	b := f.call(t, "POST", "/WABA1/message_templates").Body
	comps := b["components"].([]any)
	if len(comps) != 2 || comps[1].(map[string]any)["type"] != "BUTTONS" {
		t.Fatalf("components %v", comps)
	}
	if err := c.UnsubscribeApp(ctx); err != nil {
		t.Fatal(err)
	}
	f.call(t, "DELETE", "/WABA1/subscribed_apps")
	if p := NewPIN(); len(p) != 6 {
		t.Fatalf("pin %q", p)
	}
}

func TestTemplateButtonsAndPaymentError(t *testing.T) {
	f, app := newFake(t, map[string]string{
		"POST /PN1/messages": `{"error":{"code":131042,"message":"Business eligibility payment issue"}}`,
	})
	f.status["POST /PN1/messages"] = 400
	c := ForAccount("T", "PN1", "WABA1", "v27.0")
	c.Graph = app.Graph
	_, err := c.SendTemplateButtons(context.Background(), "+502 5555-1111", "andrho_confirmar_apartado", "es",
		[]string{"Blusa", "Q150.00"}, []string{"LS_YES:7", "LS_NO:7"})
	if !errors.Is(err, ErrPayment) {
		t.Fatalf("131042 is ErrPayment: %v", err)
	}
	b := f.call(t, "POST", "/PN1/messages").Body
	if b["to"] != "50255551111" {
		t.Fatalf("to %v", b["to"])
	}
	comps := b["template"].(map[string]any)["components"].([]any)
	if len(comps) != 3 {
		t.Fatalf("body + 2 buttons: %v", comps)
	}
	btn := comps[2].(map[string]any)
	if btn["index"] != "1" || btn["parameters"].([]any)[0].(map[string]any)["payload"] != "LS_NO:7" {
		t.Fatalf("%v", btn)
	}
}

func TestWebhookChanges(t *testing.T) {
	raw := `{"object":"whatsapp_business_account","entry":[
	 {"id":"WABA1","changes":[
	  {"field":"messages","value":{"metadata":{"phone_number_id":"PN1"},"contacts":[{"wa_id":"50255551111","profile":{"name":"Ana"}}],
	    "messages":[{"from":"50255551111","id":"m1","timestamp":"1700000000","type":"button","button":{"payload":"LS_YES:7","text":"Sí, lo quiero"}}]}},
	  {"field":"messages","value":{"metadata":{"phone_number_id":"PN1"},"statuses":[{"status":"failed","errors":[{"code":131042}]}]}},
	  {"field":"message_template_status_update","value":{"event":"APPROVED","message_template_id":123,"message_template_name":"andrho_x","message_template_language":"es"}}]},
	 {"id":"WABA2","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"PN2"},"messages":[{"from":"1","id":"m2","type":"text","text":{"body":"hola"}}]}}]}]}`
	var w Webhook
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	chs := w.Changes()
	if len(chs) != 4 || chs[0].WABAID != "WABA1" || chs[3].WABAID != "WABA2" {
		t.Fatalf("%+v", chs)
	}
	in := chs[0].Value.Incoming()
	if len(in) != 1 || in[0].ButtonID != "LS_YES:7" || in[0].Name != "Ana" {
		t.Fatalf("%+v", in)
	}
	if chs[0].Value.PaymentFailed() || !chs[1].Value.PaymentFailed() {
		t.Fatal("PaymentFailed")
	}
	if chs[2].Value.Event != "APPROVED" || chs[2].Value.MessageTemplateName != "andrho_x" {
		t.Fatalf("%+v", chs[2].Value)
	}
	if got := w.Messages("PN2"); len(got) != 1 || got[0].Text != "hola" {
		t.Fatalf("%+v", got)
	}
	if got := w.Messages(""); len(got) != 2 {
		t.Fatalf("%+v", got)
	}
}
