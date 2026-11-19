//go:build integration

// End-to-end test of Personal en obra against a real Postgres and a real
// Odoo with andrho_workforce, and a fake WhatsApp Cloud API:
//
//	LIVE_IT_PG=postgres://... LIVE_IT_ODOO=http://127.0.0.1:18069 WF_IT_ODOO_DB=wftest \
//	  go test -tags integration -run TestWorkforceEndToEnd ./internal/handlers/
package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

	"github.com/IngeniosoHacker/andrho-api/internal/ai"
	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/config"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/meta"
	"github.com/IngeniosoHacker/andrho-api/internal/middleware"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
	"github.com/IngeniosoHacker/andrho-api/internal/secret"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// fakeWA records what AndRho sends and serves one photo.
type fakeWA struct {
	mu   sync.Mutex
	sent []map[string]any
	url  string
}

func (f *fakeWA) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/messages"):
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["status"] == nil {
			f.sent = append(f.sent, body)
		}
		w.Write([]byte(`{"messages":[{"id":"wamid.x"}]}`))
	case strings.HasSuffix(r.URL.Path, "/MEDIA1"):
		w.Write([]byte(`{"url":"` + f.url + `/files/photo","mime_type":"image/png","file_size":70}`))
	case r.URL.Path == "/files/photo":
		png, _ := base64.StdEncoding.DecodeString(tinyPNG)
		w.Write(png)
	default:
		w.Write([]byte(`{}`))
	}
}

func (f *fakeWA) last(t *testing.T, to string) (string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		m := f.sent[i]
		if m["to"] != to {
			continue
		}
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
	t.Fatalf("nothing sent to %s", to)
	return "", nil
}

func TestWorkforceEndToEnd(t *testing.T) {
	pgURL, odooURL, odooDB := os.Getenv("LIVE_IT_PG"), os.Getenv("LIVE_IT_ODOO"), os.Getenv("WF_IT_ODOO_DB")
	if pgURL == "" || odooURL == "" || odooDB == "" {
		t.Skip("LIVE_IT_PG / LIVE_IT_ODOO / WF_IT_ODOO_DB not set")
	}
	ctx := context.Background()
	pool, err := db.New(ctx, pgURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fw := &fakeWA{}
	srv := httptest.NewServer(http.HandlerFunc(fw.handler))
	defer srv.Close()
	fw.url = srv.URL
	box, _ := secret.New("integration-secret-key-0123456789")
	cfg := &config.Config{JWTSecret: "jwt-secret", MetaAppSecret: "appsecret"}
	h := &Handler{Accounts: pool, Cfg: cfg, Secrets: box, Meta: meta.New("app", "appsecret", "v27.0", ""),
		WA: whatsapp.New(true, "watoken", "PN", "WABA", "v27.0"), AI: ai.New("", "", nil)}
	h.WA.Graph = srv.URL

	accountID := uuid.NewString()
	// Letters only: a digit in a name would read as a code.
	run := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return 'a' + (r - '0')
		}
		return r
	}, accountID[:6])
	if _, err := pool.Exec(ctx, `INSERT INTO accounts (id, email, password_hash, company_name, site_id, plan) VALUES ($1, $2, 'x', 'Constructora Uno', $3, 'en_orbita')`,
		accountID, accountID+"@t.dev", "c"+accountID[:8]); err != nil {
		t.Fatal(err)
	}
	ownerID, supID := uuid.NewString(), uuid.NewString()
	stamp := strconv.FormatInt(time.Now().UnixNano()%10000000, 10)
	ownerPhone, supPhone := "5021"+stamp, "5022"+stamp
	for _, u := range []struct{ id, email, name, role, phone string }{
		{ownerID, run + "o@t.dev", "Olga Dueña", "owner", ownerPhone}, {supID, run + "s@t.dev", "Samuel Supervisor", "supervisor", supPhone}} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, account_id, email, password_hash, display_name, role, whatsapp_phone, whatsapp_verified_at)
			VALUES ($1, $2, $3, 'hash', $4, $5, $6, now())`, u.id, accountID, u.email, u.name, u.role, u.phone); err != nil {
			t.Fatal(err)
		}
	}
	sealedPw, _ := box.Seal("admin")
	if err := db.SaveOdooConnection(ctx, pool, db.OdooConnection{AccountID: accountID, URL: odooURL, DBName: odooDB, Login: "admin", Secret: sealedPw}); err != nil {
		t.Fatal(err)
	}
	cl := odoo.New(odooURL, odooDB, "admin", "admin")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authMW := middleware.RequireAuth(cfg.JWTSecret)
	g := r.Group("/workforce", authMW)
	g.GET("/status", h.WorkforceStatus)
	g.POST("/setup", middleware.RequireRole("admin"), h.WorkforceSetup)
	g.POST("/sites", middleware.RequireRole("admin"), h.WorkforceCreateSite)
	g.PUT("/sites/:id/supervisors", middleware.RequireRole("admin"), h.WorkforceSetSupervisors)
	g.GET("/staff", h.WorkforceStaff)
	g.POST("/employees/:id/terminate", h.WorkforceTerminate)
	r.PUT("/account/features/:feature", authMW, middleware.RequireRole("admin"), h.SetAccountFeature)
	r.GET("/erp/inventory", authMW, func(c *gin.Context) { c.Status(200) })
	r.POST("/meta/webhook", h.MetaWebhook)

	tokens := map[string]string{}
	tokens["owner"], _ = auth.IssueAccessToken(cfg.JWTSecret, ownerID, accountID, run+"o@t.dev", "s", "Constructora Uno", "owner", time.Hour)
	tokens["supervisor"], _ = auth.IssueAccessToken(cfg.JWTSecret, supID, accountID, run+"s@t.dev", "s", "Constructora Uno", "supervisor", time.Hour)
	do := func(who, method, path string, body any) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Authorization", "Bearer "+tokens[who])
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	must := func(who, method, path string, body any) map[string]any {
		code, out := do(who, method, path, body)
		if code >= 300 {
			t.Fatalf("%s %s %s: %d %v", who, method, path, code, out)
		}
		return out
	}
	sendWA := func(from string, msg map[string]any) {
		msg["from"], msg["id"], msg["timestamp"] = from, uuid.NewString(), strconv.FormatInt(time.Now().Unix(), 10)
		body := map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "WABA", "changes": []any{
			map[string]any{"field": "messages", "value": map[string]any{"messaging_product": "whatsapp",
				"metadata": map[string]any{"phone_number_id": "PN"}, "messages": []any{msg}}}}}}}
		raw, _ := json.Marshal(body)
		mac := hmac.New(sha256.New, []byte("appsecret"))
		mac.Write(raw)
		req := httptest.NewRequest("POST", "/meta/webhook", bytes.NewReader(raw))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("webhook %d", rec.Code)
		}
		time.Sleep(900 * time.Millisecond) // handled in the background
	}
	text := func(s string) map[string]any {
		return map[string]any{"type": "text", "text": map[string]any{"body": s}}
	}
	tap := func(id string) map[string]any {
		return map[string]any{"type": "interactive", "interactive": map[string]any{"type": "button_reply", "button_reply": map[string]any{"id": id, "title": "x"}}}
	}

	// En órbita includes it; it's turned on and the ERP gets the module.
	if code, _ := do("owner", "GET", "/workforce/staff", nil); code != http.StatusForbidden {
		t.Fatalf("staff before enabling: %d", code)
	}
	must("owner", "PUT", "/account/features/workforce", map[string]any{"enabled": true})
	must("owner", "POST", "/workforce/setup", nil)
	norte := must("owner", "POST", "/workforce/sites", map[string]any{"name": "Obra Norte " + run})["site"].(map[string]any)["id"].(string)
	sur := must("owner", "POST", "/workforce/sites", map[string]any{"name": "Obra Sur " + run})["site"].(map[string]any)["id"].(string)
	must("owner", "PUT", "/workforce/sites/"+norte+"/supervisors", map[string]any{"user_ids": []string{supID}})

	// The supervisor sees only Personal, and only their site.
	if code, _ := do("supervisor", "GET", "/erp/inventory", nil); code != http.StatusForbidden {
		t.Fatalf("supervisor reached the ERP: %d", code)
	}
	if code, _ := do("supervisor", "POST", "/workforce/sites", map[string]any{"name": "x"}); code != http.StatusForbidden {
		t.Fatalf("supervisor created a site: %d", code)
	}
	st := must("supervisor", "GET", "/workforce/status", nil)
	if len(st["sites"].([]any)) != 1 {
		t.Fatalf("supervisor sites %v", st["sites"])
	}

	// A photo with the name and code: one site, so straight to the confirmation.
	juan := "Juan Pérez " + run
	sendWA(supPhone, map[string]any{"type": "image", "image": map[string]any{"id": "MEDIA1", "mime_type": "image/png", "caption": juan + " 123" + run[:2]}})
	msg, ids := fw.last(t, supPhone)
	if !strings.HasPrefix(msg, "Doy de alta a "+juan) || !strings.Contains(msg, "con su foto en Obra Norte") || len(ids) != 2 {
		t.Fatalf("confirm %q %v", msg, ids)
	}
	sendWA(supPhone, tap(wfSave))
	if msg, _ = fw.last(t, supPhone); !strings.HasPrefix(msg, "Listo: "+juan) {
		t.Fatalf("saved %q", msg)
	}
	var emps []map[string]any
	if err := cl.SearchRead(ctx, "hr.employee", []any{[]any{"name", "=", juan}}, []string{"id", "andrho_photo", "andrho_reported_by", "work_location_id"}, "", 0, &emps); err != nil || len(emps) != 1 {
		t.Fatalf("juan in the erp: %v %v", err, emps)
	}
	if emps[0]["andrho_photo"] != true || emps[0]["andrho_reported_by"] != "Samuel Supervisor (WhatsApp)" {
		t.Fatalf("juan %v", emps[0])
	}
	juanID := int(emps[0]["id"].(float64))

	// The owner has two sites: AndRho asks which, then the missing name.
	sendWA(ownerPhone, map[string]any{"type": "image", "image": map[string]any{"id": "MEDIA1", "mime_type": "image/png", "caption": ""}})
	msg, ids = fw.last(t, ownerPhone)
	if msg != "¿A qué obra va?" || len(ids) != 2 {
		t.Fatalf("site question %q %v", msg, ids)
	}
	sendWA(ownerPhone, tap(wfSiteBtn+sur))
	if msg, _ = fw.last(t, ownerPhone); msg != "¿Cómo se llama el trabajador?" {
		t.Fatalf("name question %q", msg)
	}
	pedro := "Pedro Gómez " + run
	sendWA(ownerPhone, text(pedro))
	if msg, _ = fw.last(t, ownerPhone); !strings.Contains(msg, pedro) || !strings.Contains(msg, "Obra Sur") {
		t.Fatalf("owner confirm %q", msg)
	}
	sendWA(ownerPhone, text("sí"))

	// A crew: the supervisor can't add Pedro (another site); unknown names
	// are left out and said so.
	sendWA(supPhone, text("Cuadrilla Losa "+run+": "+juan+" y "+pedro+", en el nivel 3, armado de losa"))
	msg, _ = fw.last(t, supPhone)
	if !strings.Contains(msg, "Cuadrilla Losa "+run+" en Obra Norte") || !strings.Contains(msg, "No encontré a: "+pedro) || !strings.Contains(msg, "nivel 3") {
		t.Fatalf("crew confirm %q", msg)
	}
	sendWA(supPhone, tap(wfSave))
	staff := must("supervisor", "GET", "/workforce/staff", nil)["sites"].([]any)
	if len(staff) != 1 {
		t.Fatalf("supervisor staff %v", staff)
	}
	site := staff[0].(map[string]any)
	crews := site["crews"].([]any)
	if len(crews) != 1 || crews[0].(map[string]any)["zone"] != "nivel 3" || crews[0].(map[string]any)["reported_by"] != "Samuel Supervisor (WhatsApp)" {
		t.Fatalf("crews %v", crews)
	}

	// The supervisor can't let go someone from another site.
	var pedros []map[string]any
	_ = cl.SearchRead(ctx, "hr.employee", []any{[]any{"name", "=", pedro}}, []string{"id"}, "", 0, &pedros)
	if len(pedros) != 1 {
		t.Fatalf("pedro %v", pedros)
	}
	if code, _ := do("supervisor", "POST", "/workforce/employees/"+strconv.Itoa(int(pedros[0]["id"].(float64)))+"/terminate", nil); code != http.StatusForbidden {
		t.Fatalf("supervisor terminated another site's worker: %d", code)
	}

	// "Baja de …" by WhatsApp: archived and the photo erased.
	sendWA(supPhone, text("Baja de "+juan))
	if msg, ids = fw.last(t, supPhone); !strings.Contains(msg, "borro su foto") || len(ids) != 2 {
		t.Fatalf("terminate confirm %q", msg)
	}
	sendWA(supPhone, tap(wfSave))
	var gone []map[string]any
	_ = cl.Exec(ctx, "hr.employee", "search_read", []any{[]any{[]any{"id", "=", juanID}, []any{"active", "in", []bool{true, false}}}},
		map[string]any{"fields": []string{"active", "andrho_photo", "image_1920"}}, &gone)
	if len(gone) != 1 || gone[0]["active"] != false || gone[0]["andrho_photo"] != false || gone[0]["image_1920"] != false {
		t.Fatalf("after leaving %v", gone)
	}

	// Someone who isn't a supervisor gets nothing from Personal by WhatsApp.
	sendWA("50299990000", text("Cuadrilla X: Juan"))
	if msg, _ = fw.last(t, "50299990000"); msg != waStranger {
		t.Fatalf("stranger %q", msg)
	}
}
