//go:build integration

// End-to-end test of the live sales against a real Postgres and a real
// Odoo with andrho_live_sale, and a fake Graph API:
//
//	LIVE_IT_PG=postgres://... LIVE_IT_ODOO=http://127.0.0.1:18069 LIVE_IT_ODOO_DB=lstest \
//	  go test -tags integration -run TestLiveEndToEnd ./internal/handlers/
package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/config"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/live"
	"github.com/IngeniosoHacker/andrho-api/internal/meta"
	"github.com/IngeniosoHacker/andrho-api/internal/middleware"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
	"github.com/IngeniosoHacker/andrho-api/internal/secret"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// fakeGraph records what AndRho sends to Meta and serves a live's comments.
type fakeGraph struct {
	mu       sync.Mutex
	sent     []map[string]any // /messages bodies (Messenger)
	wa       []map[string]any // WhatsApp Cloud API bodies (no read receipts)
	reacts   []map[string]any
	comments []map[string]any
	posts    int
	deleted  []string
	prefix   string // per run, so post ids don't collide across runs
	captions []string
}

func (f *fakeGraph) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/messages"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["messaging_product"] == "whatsapp" {
			if body["status"] == nil {
				f.wa = append(f.wa, body)
			}
			w.Write([]byte(`{"messages":[{"id":"wamid.x"}]}`))
			return
		}
		f.sent = append(f.sent, body)
		w.Write([]byte(`{"message_id":"m"}`))
	case strings.HasSuffix(r.URL.Path, "/PN"):
		w.Write([]byte(`{"display_phone_number":"+1 555-000-1111"}`))
	case strings.HasSuffix(r.URL.Path, "/reactions"):
		b, _ := json.Marshal(map[string]any{"data": f.reacts})
		w.Write(b)
	case strings.HasSuffix(r.URL.Path, "/live_videos"):
		w.Write([]byte(`{"data":[{"id":"LIVE1","status":"LIVE","permalink_url":"https://facebook.com/p/videos/1"}]}`))
	case strings.HasSuffix(r.URL.Path, "/comments"):
		b, _ := json.Marshal(map[string]any{"data": f.comments})
		w.Write(b)
	case strings.HasSuffix(r.URL.Path, "/LIVE1"):
		w.Write([]byte(`{"id":"LIVE1","status":"LIVE"}`))
	case strings.HasSuffix(r.URL.Path, "/photos"):
		_ = r.ParseMultipartForm(1 << 20)
		f.captions = append(f.captions, r.FormValue("caption"))
		f.posts++
		w.Write([]byte(`{"id":"PH","post_id":"PAGE1_` + f.prefix + `POST` + string(rune('0'+f.posts)) + `"}`))
	case r.Method == http.MethodDelete:
		f.deleted = append(f.deleted, r.URL.Path)
		w.Write([]byte(`{"success":true}`))
	case strings.HasSuffix(r.URL.Path, "/subscribed_apps"):
		w.Write([]byte(`{"success":true}`))
	default:
		w.Write([]byte(`{}`))
	}
}

func (f *fakeGraph) lastText(t *testing.T) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("nothing sent to Messenger")
	}
	m := f.sent[len(f.sent)-1]["message"].(map[string]any)
	if txt, ok := m["text"].(string); ok {
		return txt
	}
	return m["attachment"].(map[string]any)["payload"].(map[string]any)["text"].(string)
}

// lastButtons are the buttons of the last Messenger message.
func (f *fakeGraph) lastButtons(t *testing.T) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.sent[len(f.sent)-1]["message"].(map[string]any)
	out := []map[string]any{}
	for _, b := range m["attachment"].(map[string]any)["payload"].(map[string]any)["buttons"].([]any) {
		out = append(out, b.(map[string]any))
	}
	return out
}

// lastWA is the last WhatsApp message: its text and its button ids.
func (f *fakeGraph) lastWA(t *testing.T) (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.wa) == 0 {
		t.Fatal("nothing sent to WhatsApp")
	}
	m := f.wa[len(f.wa)-1]
	if tx, ok := m["text"].(map[string]any); ok {
		return tx["body"].(string), nil
	}
	in := m["interactive"].(map[string]any)
	ids := []string{}
	for _, b := range in["action"].(map[string]any)["buttons"].([]any) {
		ids = append(ids, b.(map[string]any)["reply"].(map[string]any)["id"].(string))
	}
	return in["body"].(map[string]any)["text"].(string), ids
}

func TestLiveEndToEnd(t *testing.T) {
	pgURL, odooURL, odooDB := os.Getenv("LIVE_IT_PG"), os.Getenv("LIVE_IT_ODOO"), os.Getenv("LIVE_IT_ODOO_DB")
	if pgURL == "" || odooURL == "" {
		t.Skip("LIVE_IT_PG / LIVE_IT_ODOO not set")
	}
	ctx := context.Background()
	pool, err := db.New(ctx, pgURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	graph := &fakeGraph{}
	gs := httptest.NewServer(http.HandlerFunc(graph.handler))
	defer gs.Close()
	box, _ := secret.New("integration-secret-key-0123456789")
	cfg := &config.Config{JWTSecret: "jwt-secret", LiveConfirmMinutes: 15, MetaAppSecret: "appsecret", MetaVerifyToken: "vt", LiveWhatsAppTest: true}
	h := &Handler{Accounts: pool, Cfg: cfg, Secrets: box, Meta: meta.New("app", "appsecret", "v27.0", ""),
		WA: whatsapp.New(true, "watoken", "PN", "WABA", "v27.0")}
	h.Meta.Graph = gs.URL
	h.WA.Graph = gs.URL
	livePoll = 200 * time.Millisecond
	liveStatusEvery = 400 * time.Millisecond

	accountID := uuid.NewString()
	run := accountID[:8] // comment ids are unique across runs on the same database
	cid := func(s string) string { return run + "-" + s }
	graph.prefix = run
	post1 := "PAGE1_" + run + "POST1"
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, email, password_hash, company_name, site_id, plan) VALUES ($1, $2, 'x', 'Tienda Ana', $3, 'plataforma')`,
		accountID, accountID+"@t.dev", "s"+accountID[:8]); err != nil {
		t.Fatal(err)
	}
	sealedPw, _ := box.Seal("admin")
	if err := db.SaveOdooConnection(ctx, pool, db.OdooConnection{AccountID: accountID, URL: odooURL, DBName: odooDB, Login: "admin", Secret: sealedPw}); err != nil {
		t.Fatal(err)
	}
	sealedTok, _ := box.Seal("PAGETOKEN")
	if err := db.SaveMetaPage(ctx, pool, db.MetaPage{PageID: "PAGE1", AccountID: accountID, Name: "Tienda Ana", Token: sealedTok}, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SelectMetaPage(ctx, pool, accountID, "PAGE1", true); err != nil {
		t.Fatal(err)
	}

	// Two products with stock in the ERP.
	cl := odoo.New(odooURL, odooDB, "admin", "admin")
	var stockLoc []map[string]any
	if err := cl.SearchRead(ctx, "stock.warehouse", nil, []string{"lot_stock_id"}, "", 1, &stockLoc); err != nil {
		t.Fatal(err)
	}
	locID := int(stockLoc[0]["lot_stock_id"].([]any)[0].(float64))
	pids := []int{}
	for _, p := range []struct {
		name  string
		price float64
	}{{"Blusa roja de seda", 85}, {"Jeans azul", 120}, {"Vestido negro", 200}} {
		var id int
		if err := cl.Exec(ctx, "product.product", "create", []any{map[string]any{"name": p.name + " " + accountID[:4], "is_storable": true, "list_price": p.price, "image_1920": tinyPNG}}, nil, &id); err != nil {
			t.Fatal(err)
		}
		var qid int
		if err := cl.Exec(ctx, "stock.quant", "create", []any{map[string]any{"product_id": id, "location_id": locID, "inventory_quantity": 2}}, nil, &qid); err != nil {
			t.Fatal(err)
		}
		if err := cl.Exec(ctx, "stock.quant", "action_apply_inventory", []any{[]int{qid}}, nil, nil); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, id)
	}

	token, _ := auth.IssueAccessToken(cfg.JWTSecret, uuid.NewString(), accountID, "ana@t.dev", "s", "Tienda Ana", "owner", time.Hour)
	router := gin_test_router(h)
	status := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	call := func(method, path string, body any) map[string]any {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		return out
	}

	// Plataforma includes live sales, but this business isn't en_vivo and
	// never turned it on: it's hidden and every /live/* route says so.
	st := call("GET", "/live/status", nil)
	if st["plan_allows"] != true || st["enabled"] != false {
		t.Fatalf("status before enabling %v", st)
	}
	if code := status("GET", "/live/sessions"); code != http.StatusForbidden {
		t.Fatalf("sessions while off: %d", code)
	}
	if err := db.SetFeature(ctx, pool, accountID, "live_sale", true); err != nil {
		t.Fatal(err)
	}
	st = call("GET", "/live/status", nil)
	if st["enabled"] != true || st["erp"] != "ready" {
		t.Fatalf("status %v", st)
	}
	// CRM: on by default; the WhatsApp button is turned on by hand, and
	// reactions count as prospects only because the seller asks.
	feats := call("PUT", "/account/features/live_whatsapp", map[string]any{"enabled": true})["features"].(map[string]any)
	if feats["live_crm"].(map[string]any)["enabled"] != true || feats["live_whatsapp"].(map[string]any)["enabled"] != true {
		t.Fatalf("features %v", feats)
	}
	call("PUT", "/live/settings", map[string]string{"capture_reactions": "1", "privacy_url": "https://tienda.example/privacidad"})
	graph.mu.Lock()
	graph.reacts = []map[string]any{{"id": cid("U9"), "name": "Marta Reacciona", "type": "LOVE"}}
	graph.mu.Unlock()
	// The seller's word for the posts: "Apartado" instead of "mío".
	if set := call("PUT", "/live/settings", map[string]string{"claim_word": "Apartado"}); set["claim_word"] != "Apartado" {
		t.Fatalf("claim word %v", set)
	}
	created := call("POST", "/live/sessions", map[string]any{"name": "Live de prueba", "items": []map[string]any{
		{"product_id": pids[0]}, {"product_id": pids[1]}, {"product_id": pids[2], "price": 180}}})
	sessID := created["session"].(map[string]any)["id"].(string)
	lv := created["live"].(map[string]any)
	lines := lv["lines"].([]any)
	if lv["state"] != "ready" || len(lines) != 3 {
		t.Fatalf("live %v", lv)
	}
	// Order: the second priciest opens (jeans), then the blusa, the
	// priciest closes (vestido); names follow that order.
	first := lines[1].(map[string]any)
	if lines[0].(map[string]any)["live_name"] != "jeans azul 1" || first["live_name"] != "blusa roja 2" {
		t.Fatalf("names %v", lines)
	}
	blusaLine := int(first["id"].(float64))

	// The live starts; two people comment the blusa, one someone else's item.
	graph.mu.Lock()
	graph.comments = []map[string]any{
		{"id": cid("c1"), "message": "Blusa roja 2!!", "created_time": time.Now().UTC().Format("2006-01-02T15:04:05-0700"), "from": map[string]any{"id": cid("U1"), "name": "ana maría lópez"}},
		{"id": cid("c2"), "message": "2", "created_time": time.Now().UTC().Add(time.Second).Format("2006-01-02T15:04:05-0700"), "from": map[string]any{"id": cid("U2"), "name": "Luis Pérez"}},
		{"id": cid("c3"), "message": "hola que bonito", "created_time": time.Now().UTC().Add(2 * time.Second).Format("2006-01-02T15:04:05-0700"), "from": map[string]any{"id": cid("U3"), "name": "X"}},
	}
	graph.mu.Unlock()
	call("POST", "/live/sessions/"+sessID+"/start", map[string]any{"live_url": ""})
	deadline := time.Now().Add(15 * time.Second)
	var sess map[string]any
	for time.Now().Before(deadline) {
		sess = call("GET", "/live/sessions/"+sessID, nil)
		if len(sess["activity"].([]any)) >= 2 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if got := graph.lastText(t); got != live.ConfirmWithConsent("blusa roja 2", 85) {
		t.Fatalf("private reply %q", got)
	}
	if bs := graph.lastButtons(t); len(bs) != 3 || bs[2]["type"] != "web_url" || !strings.HasPrefix(bs[2]["url"].(string), "https://wa.me/15550001111?text=Pedido+AR-") {
		t.Fatalf("buttons %v", bs)
	}
	claims, _ := db.ListSessionClaims(ctx, pool, sessID, 10)
	var asked, queued db.LiveClaim
	for _, c := range claims {
		switch c.State {
		case "asked":
			asked = c
		case "queued":
			queued = c
		}
	}
	if asked.CommentID != cid("c1") || queued.CommentID != cid("c2") {
		t.Fatalf("claims %+v", claims)
	}

	// Ana says no: the blusa goes to Luis.
	page, _ := h.pageFor(ctx, "PAGE1")
	h.livePostback(ctx, page, "PSID-ANA", "LS_NO:"+itoa(asked.ID))
	if got := graph.lastText(t); got != live.ConfirmWithConsent("blusa roja 2", 85) {
		t.Fatalf("second ask %q", got)
	}
	luis, _ := db.GetLiveClaim(ctx, pool, queued.ID)
	if luis.State != "asked" {
		t.Fatalf("luis %s", luis.State)
	}
	// Luis says yes, then sends phone and address.
	h.livePostback(ctx, page, "PSID-LUIS", "LS_YES:"+itoa(luis.ID))
	if got := graph.lastText(t); got != "¡Listo, Luis! Tu pedido quedó apartado. Para tu envío, escríbenos tu número de teléfono." {
		t.Fatalf("after yes %q", got)
	}
	h.liveText(ctx, page, "PSID-LUIS", "mi numero es 5555 1234")
	h.liveText(ctx, page, "PSID-LUIS", "5a calle 1-23, zona 1")
	// Right before closing: which channel does he prefer?
	if got := graph.lastText(t); got != live.AskChannel {
		t.Fatalf("channel question %q", got)
	}
	h.livePostback(ctx, page, "PSID-LUIS", live.PayloadChannelMS+itoa(luis.ID))
	if got := graph.lastText(t); got != live.ChannelSavedMessenger+" "+live.DoneWith("https://tienda.example/privacidad") {
		t.Fatalf("done %q", got)
	}
	cards := call("GET", "/live/cards?state=ready", nil)["cards"].([]any)
	found := false
	for _, c := range cards {
		m := c.(map[string]any)
		if m["buyer"] == "Luis Pérez" && m["phone"] == "5555-1234" && m["zone"] == "zona 1" && m["total"] == 85.0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("card %v", cards)
	}
	_ = blusaLine

	// The live ends; the unsold items are posted and one sells with "Mío".
	call("POST", "/live/sessions/"+sessID+"/end", nil)
	pub := call("POST", "/live/sessions/"+sessID+"/publish", map[string]any{})
	if pub["posted"].(float64) != 2 {
		t.Fatalf("publish %v (products have no photo?)", pub)
	}
	if !strings.Contains(graph.captions[0], "Comenta «Apartado»") {
		t.Fatalf("caption %q", graph.captions[0])
	}
	wh := map[string]any{"object": "page", "entry": []any{map[string]any{"id": "PAGE1", "changes": []any{
		map[string]any{"field": "feed", "value": map[string]any{"item": "comment", "verb": "add", "post_id": post1, "comment_id": cid("pc1"), "message": "Mío", "from": map[string]any{"id": cid("U5"), "name": "Eva"}}},
		map[string]any{"field": "feed", "value": map[string]any{"item": "comment", "verb": "add", "post_id": post1, "comment_id": cid("pc2"), "message": "¡APARTADO! 🙋", "from": map[string]any{"id": cid("U4"), "name": "Rosa"}}},
	}}}}
	raw, _ := json.Marshal(wh)
	mac := hmac.New(sha256.New, []byte("appsecret"))
	mac.Write(raw)
	req := httptest.NewRequest("POST", "/meta/webhook", bytes.NewReader(raw))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("webhook %d", rec.Code)
	}
	var rosa db.LiveClaim
	for i := 0; i < 50; i++ {
		if c, err := claimByComment(ctx, pool, cid("pc2")); err == nil && c.State == "asked" {
			rosa = c
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if rosa.ID == 0 {
		t.Fatal("rosa's mío wasn't asked")
	}
	if _, err := claimByComment(ctx, pool, cid("pc1")); err == nil {
		t.Fatal("'Mío' must be ignored once the word is 'Apartado'")
	}
	// Rosa answers through WhatsApp: the button types "Pedido AR-XXXX".
	waURL := graph.lastButtons(t)[2]["url"].(string)
	code := live.OrderCode(strings.ReplaceAll(waURL, "+", " "))
	if code == "" {
		t.Fatalf("no code in %q", waURL)
	}
	sendWA := func(msg map[string]any) {
		msg["from"], msg["id"], msg["timestamp"] = "50255559999", uuid.NewString(), strconv.FormatInt(time.Now().Unix(), 10)
		body := map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "WABA", "changes": []any{
			map[string]any{"field": "messages", "value": map[string]any{"messaging_product": "whatsapp",
				"metadata": map[string]any{"phone_number_id": "PN", "display_phone_number": "15550001111"},
				"contacts": []any{map[string]any{"wa_id": "50255559999", "profile": map[string]any{"name": "Rosa"}}},
				"messages": []any{msg}}}}}}}
		raw, _ := json.Marshal(body)
		mac := hmac.New(sha256.New, []byte("appsecret"))
		mac.Write(raw)
		req := httptest.NewRequest("POST", "/meta/webhook", bytes.NewReader(raw))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("wa webhook %d", rec.Code)
		}
		time.Sleep(700 * time.Millisecond) // handled in the background
	}
	sendWA(map[string]any{"type": "text", "text": map[string]any{"body": "Pedido " + code}})
	txt, ids := graph.lastWA(t)
	if !strings.HasPrefix(txt, live.WhatsAppLinked) || len(ids) != 2 || ids[0] != live.PayloadYes+itoa(rosa.ID) {
		t.Fatalf("wa confirm %q %v", txt, ids)
	}
	sendWA(map[string]any{"type": "interactive", "interactive": map[string]any{"type": "button_reply", "button_reply": map[string]any{"id": ids[0], "title": "Sí"}}})
	if txt, _ = graph.lastWA(t); !strings.HasPrefix(txt, "¡Listo, Rosa!") {
		t.Fatalf("wa after yes %q", txt)
	}
	sendWA(map[string]any{"type": "text", "text": map[string]any{"body": "4444 1234"}})
	sendWA(map[string]any{"type": "text", "text": map[string]any{"body": "Avenida Reforma 10-10, zona 10"}})
	txt, ids = graph.lastWA(t)
	if txt != live.AskChannel || len(ids) != 2 {
		t.Fatalf("wa channel question %q %v", txt, ids)
	}
	sendWA(map[string]any{"type": "interactive", "interactive": map[string]any{"type": "button_reply", "button_reply": map[string]any{"id": ids[0], "title": "WhatsApp"}}})
	if txt, _ = graph.lastWA(t); !strings.HasPrefix(txt, live.ChannelSavedWhatsApp) {
		t.Fatalf("wa channel saved %q", txt)
	}
	// Someone else sending Rosa's code gets nothing of hers.
	graph.mu.Lock()
	before := len(graph.wa)
	graph.mu.Unlock()
	rosa, _ = db.GetLiveClaim(ctx, pool, rosa.ID)
	var rb []map[string]any
	_ = cl.SearchRead(ctx, "andrho.live.buyer", []any{[]any{"id", "=", rosa.BuyerID}}, []string{"kind", "preferred_channel", "wa_phone", "consent_channel", "psid"}, "", 1, &rb)
	if len(rb) != 1 || rb[0]["kind"] != "customer" || rb[0]["preferred_channel"] != "whatsapp" || rb[0]["wa_phone"] != "50255559999" || rb[0]["consent_channel"] != "whatsapp" {
		t.Fatalf("rosa in the erp %v", rb)
	}
	_ = before

	// Someone writes to the Page: a prospect.
	pm := map[string]any{"object": "page", "entry": []any{map[string]any{"id": "PAGE1", "messaging": []any{
		map[string]any{"sender": map[string]any{"id": cid("PS-NEW")}, "recipient": map[string]any{"id": "PAGE1"}, "message": map[string]any{"mid": "m1", "text": "hola, ¿precio?"}}}}}}
	raw, _ = json.Marshal(pm)
	mac = hmac.New(sha256.New, []byte("appsecret"))
	mac.Write(raw)
	req = httptest.NewRequest("POST", "/meta/webhook", bytes.NewReader(raw))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	router.ServeHTTP(httptest.NewRecorder(), req)
	time.Sleep(700 * time.Millisecond)

	// The live's people: Ana and Luis are customers (they named the item),
	// X and Marta (reaction) prospects.
	people := call("GET", "/live/sessions/"+sessID+"/people", nil)
	kinds := map[string]string{}
	for _, p := range people["people"].([]any) {
		m := p.(map[string]any)
		kinds[m["name"].(string)] = m["kind"].(string)
	}
	if kinds["ana maría lópez"] != "customer" || kinds["Luis Pérez"] != "customer" || kinds["X"] != "prospect" || kinds["Marta Reacciona"] != "prospect" {
		t.Fatalf("people %v", kinds)
	}
	contacts := call("GET", "/live/contacts", nil)
	if contacts["prospects"].(float64) < 3 || contacts["channels"].(map[string]any)["whatsapp"].(float64) < 1 {
		t.Fatalf("contacts %v", contacts)
	}
	graph.mu.Lock()
	deleted := append([]string(nil), graph.deleted...)
	graph.mu.Unlock()
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/"+post1) {
		t.Fatalf("deleted %v", deleted)
	}
	m := call("GET", "/live/metrics", nil)
	if m["lives"].(float64) < 1 || m["sold"].(float64) < 2 {
		t.Fatalf("metrics %v", m)
	}
	set := call("PUT", "/live/settings", map[string]string{"card_message": "¡Gracias {nombre}, vuelve pronto!", "loyalty_enabled": "1", "claim_word": "mío"})
	if set["loyalty_enabled"] != "1" {
		t.Fatalf("settings %v", set)
	}
	loc := call("GET", "/erp/locate?q=productos", nil)["results"].([]any)
	if loc[0].(map[string]any)["key"] != "products" {
		t.Fatalf("locate %v", loc)
	}
}

func claimByComment(ctx context.Context, pool *pgxpool.Pool, commentID string) (db.LiveClaim, error) {
	return db.GetLiveClaimByComment(ctx, pool, commentID)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// gin_test_router mounts the routes this test uses (the real router
// package imports handlers, so it can't be used here).
func gin_test_router(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	auth := middleware.RequireAuth(h.Cfg.JWTSecret)
	g := r.Group("/live", auth)
	g.GET("/status", h.LiveStatus)
	g.GET("/sessions", h.LiveList)
	g.POST("/sessions", h.LiveCreate)
	g.GET("/sessions/:id", h.LiveGet)
	g.POST("/sessions/:id/start", h.LiveStart)
	g.POST("/sessions/:id/end", h.LiveEnd)
	g.POST("/sessions/:id/publish", h.LivePublish)
	g.GET("/cards", h.LiveCards)
	g.GET("/metrics", h.LiveMetrics)
	g.PUT("/settings", h.LiveSaveSettings)
	g.GET("/sessions/:id/people", h.LivePeople)
	g.GET("/contacts", h.LiveContacts)
	r.PUT("/account/features/:feature", auth, h.SetAccountFeature)
	r.GET("/erp/locate", auth, h.ERPLocate)
	r.POST("/meta/webhook", h.MetaWebhook)
	return r
}

// A 1x1 PNG, so the unsold items have a photo to post.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
