package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
	"github.com/IngeniosoHacker/andrho-api/internal/secret"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// esGraph is a fake Graph API for Embedded Signup and the stores' numbers.
type esGraph struct {
	mu    sync.Mutex
	calls []string                    // "METHOD /path"
	sent  map[string][]map[string]any // phone number id -> /messages bodies
	pins  []string
	coex  bool
}

func (g *esGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, r.Method+" "+path)
	switch {
	case path == "/oauth/access_token":
		w.Write([]byte(`{"access_token":"BIZTOKEN-SECRET","token_type":"bearer"}`))
	case path == "/debug_token":
		w.Write([]byte(`{"data":{"is_valid":true,"granular_scopes":[{"scope":"whatsapp_business_management","target_ids":["WABA1"]},{"scope":"whatsapp_business_messaging","target_ids":["WABA1"]}]}}`))
	case path == "/WABA1/phone_numbers":
		w.Write([]byte(`{"data":[{"id":"PN1","display_phone_number":"+502 5555 0000","verified_name":"Modas Cristi","is_on_biz_app":` +
			map[bool]string{true: "true", false: "false"}[g.coex] + `}]}`))
	case path == "/PN1" && r.Method == http.MethodGet:
		w.Write([]byte(`{"id":"PN1","display_phone_number":"+502 5555 0000","verified_name":"Modas Cristi","name_status":"APPROVED","quality_rating":"GREEN"}`))
	case path == "/WABA1" && r.Method == http.MethodGet:
		w.Write([]byte(`{"id":"WABA1","primary_funding_id":"F1"}`))
	case path == "/PN1/register":
		g.pins = append(g.pins, body["pin"].(string))
		w.Write([]byte(`{"success":true}`))
	case path == "/WABA1/message_templates" && r.Method == http.MethodGet:
		w.Write([]byte(`{"data":[]}`))
	case path == "/WABA1/message_templates":
		w.Write([]byte(`{"id":"T","status":"PENDING","category":"UTILITY"}`))
	case strings.HasSuffix(path, "/messages"):
		pn := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/messages")
		if body["status"] == nil {
			g.sent[pn] = append(g.sent[pn], body)
		}
		w.Write([]byte(`{"messages":[{"id":"wamid.x"}]}`))
	default:
		w.Write([]byte(`{"success":true}`))
	}
}

func (g *esGraph) called(call string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (g *esGraph) texts(pn string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []string{}
	for _, m := range g.sent[pn] {
		if t, ok := m["text"].(map[string]any); ok {
			out = append(out, t["body"].(string))
		} else {
			out = append(out, "<"+m["type"].(string)+">")
		}
	}
	return out
}

// waTestPool is a migrated database (WA_IT_PG), or a skip.
func waTestPool(t *testing.T) *pgxpool.Pool {
	pgURL := os.Getenv("WA_IT_PG")
	if pgURL == "" {
		t.Skip("WA_IT_PG not set")
	}
	ctx := context.Background()
	pool, err := db.New(ctx, pgURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func waTestHandler(t *testing.T, pool *pgxpool.Pool, g *esGraph) *Handler {
	gs := httptest.NewServer(g)
	t.Cleanup(gs.Close)
	box, _ := secret.New("integration-secret-key-0123456789")
	cfg := &config.Config{JWTSecret: "jwt-secret", MetaAppID: "APP", MetaAppSecret: "appsecret", MetaGraphVersion: "v27.0",
		WhatsAppEmbeddedSignup: true, MetaWAESConfigID: "CFG", LiveConfirmMinutes: 15}
	h := &Handler{Accounts: pool, Cfg: cfg, Secrets: box, Meta: meta.New("APP", "appsecret", "v27.0", ""),
		WA:    whatsapp.New(true, "ANDRHOTOKEN", "PNANDRHO", "WABAANDRHO", "v27.0"),
		WAApp: whatsapp.NewApp("APP", "appsecret", "v27.0")}
	h.Meta.Graph, h.WA.Graph, h.WAApp.Graph = gs.URL, gs.URL, gs.URL
	return h
}

func waTestAccount(t *testing.T, pool *pgxpool.Pool, plan string) string {
	id := uuid.NewString()
	if _, err := pool.Exec(context.Background(), `INSERT INTO accounts (id, email, password_hash, company_name, site_id, plan)
		VALUES ($1, $2, 'x', 'Modas Cristi', $3, $4)`, id, id+"@t.dev", "s"+id[:8], plan); err != nil {
		t.Fatal(err)
	}
	return id
}

func waRouter(h *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/whatsapp/business", middleware.RequireAuth(h.Cfg.JWTSecret))
	g.GET("", h.WhatsAppBusiness)
	g.GET("/signup-config", middleware.RequireRole("admin"), h.WhatsAppSignupConfig)
	g.POST("/connect", middleware.RequireRole("admin"), h.WhatsAppConnect)
	g.POST("/refresh", middleware.RequireRole("admin"), h.WhatsAppRefresh)
	g.DELETE("", middleware.RequireRole("admin"), h.WhatsAppDisconnect)
	return r
}

func waDo(t *testing.T, r *gin.Engine, accountID, role, method, path string, body any) (int, map[string]any) {
	token, _ := auth.IssueAccessToken("jwt-secret", uuid.NewString(), accountID, "o@t.dev", "s", "Modas Cristi", role, time.Hour)
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func waHook(t *testing.T, h *Handler, raw string) {
	var wh whatsapp.Webhook
	if err := json.Unmarshal([]byte(raw), &wh); err != nil {
		t.Fatal(err)
	}
	h.handleWhatsApp(context.Background(), wh)
}

func TestWhatsAppBusinessEndToEnd(t *testing.T) {
	pool := waTestPool(t)
	ctx := context.Background()
	g := &esGraph{sent: map[string][]map[string]any{}}
	h := waTestHandler(t, pool, g)
	r := waRouter(h)
	accountID := waTestAccount(t, pool, "plataforma")
	// Earlier signups left their whatsapp job waiting for Meta.
	if _, err := db.CreateProvisioningJob(ctx, pool, db.ProvisioningJob{ID: uuid.NewString(), AccountID: accountID, Kind: "whatsapp", Status: provision.StatusWaitingMeta}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MoveWhatsAppJobsToClient(ctx, pool, provision.StatusWaitingClient, provision.WhatsAppConnectDetail); err != nil {
		t.Fatal(err)
	}

	// Before connecting: no number for buyers, never AndRho's.
	if _, ok := h.buyerLine(ctx, accountID); ok {
		t.Fatal("without its own number an account has no WhatsApp with buyers")
	}
	code, st := waDo(t, r, accountID, "owner", "GET", "/whatsapp/business", nil)
	if code != 200 || st["connected"] != false || st["job"].(map[string]any)["status"] != provision.StatusWaitingClient {
		t.Fatalf("%d %v", code, st)
	}
	if code, cfgOut := waDo(t, r, accountID, "owner", "GET", "/whatsapp/business/signup-config", nil); code != 200 || cfgOut["config_id"] != "CFG" {
		t.Fatalf("%d %v", code, cfgOut)
	}

	// Connect: the browser's hints are wrong on purpose; the token decides.
	code, st = waDo(t, r, accountID, "owner", "POST", "/whatsapp/business/connect",
		map[string]any{"code": "CODE", "event": "FINISH", "phone_number_id": "PN1", "waba_id": "NOT-MINE"})
	if code != 200 || st["connected"] != true {
		t.Fatalf("connect: %d %v", code, st)
	}
	num := st["number"].(map[string]any)
	if num["verified_name"] != "Modas Cristi" || num["name_approved"] != true || num["quality_rating"] != "GREEN" || num["coexistence"] != false || num["status"] != db.WAActive {
		t.Fatalf("%v", num)
	}
	if !g.called("POST /WABA1/subscribed_apps") || len(g.pins) != 1 || len(g.pins[0]) != 6 {
		t.Fatalf("subscribe + register: %v %v", g.calls, g.pins)
	}
	if g.called("POST /PN1/smb_app_data") {
		t.Fatal("a new number gets no Coexistence sync")
	}
	if tpls := st["templates"].([]any); len(tpls) != 2 {
		t.Fatalf("templates %v", tpls)
	}
	if j := st["job"].(map[string]any); j["status"] != provision.StatusDone {
		t.Fatalf("job %v", j)
	}
	var tok, pin []byte
	if err := pool.QueryRow(ctx, `SELECT token, pin FROM whatsapp_accounts WHERE account_id = $1 AND status = 'activo'`, accountID).Scan(&tok, &pin); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(tok, []byte("BIZTOKEN")) || bytes.Contains(pin, []byte(g.pins[0])) {
		t.Fatal("token and PIN must be stored sealed")
	}
	if plain, _ := h.Secrets.Open(tok); plain != "BIZTOKEN-SECRET" {
		t.Fatal("token round trip")
	}

	// A buyer's order code on the store's number: answered from it.
	line, ok := h.buyerLine(ctx, accountID)
	if !ok || !line.own() || line.biz != "PN1" || line.number(ctx) != "50255550000" {
		t.Fatalf("%+v %v", line, ok)
	}
	claim, _, err := db.InsertLiveClaim(ctx, pool, db.LiveClaim{AccountID: accountID, PageID: "PAGE1", OdooLineID: 1,
		CommentID: "c-" + accountID, AuthorID: "psid1", AuthorName: "Ana"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetLiveClaimState(ctx, pool, claim.ID, []string{"queued"}, "confirmed"); err != nil {
		t.Fatal(err)
	}
	claim.State = "confirmed"
	l, err := h.orderCode(ctx, claim, line.biz)
	if err != nil || l.Biz != "PN1" {
		t.Fatalf("%+v %v", l, err)
	}
	buyer := "50255551111"
	msg := func(pn, from, text string) string {
		return `{"object":"whatsapp_business_account","entry":[{"id":"WABA1","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"` +
			pn + `"},"messages":[{"from":"` + from + `","id":"m-` + uuid.NewString() + `","type":"text","text":{"body":"` + text + `"}}]}}]}]}`
	}
	// The code sent to AndRho's number doesn't work there (and AndRho
	// answers as the control channel).
	waHook(t, h, msg("PNANDRHO", buyer, live.OrderText(l.Code)))
	if got := g.texts("PNANDRHO"); len(got) != 1 || got[0] != waStranger {
		t.Fatalf("AndRho's number: %v", got)
	}
	if got := g.texts("PN1"); len(got) != 0 {
		t.Fatalf("nothing from the store yet: %v", got)
	}
	waHook(t, h, msg("PN1", buyer, live.OrderText(l.Code)))
	if got := g.texts("PN1"); len(got) != 1 || got[0] != live.WhatsAppLinked {
		t.Fatalf("store's number: %v", got)
	}
	if last, _ := db.WALastInbound(ctx, pool, "PN1", buyer); last == nil {
		t.Fatal("the window is per store number")
	}
	if last, _ := db.WALastInbound(ctx, pool, "PN1", "50200000000"); last != nil {
		t.Fatal("another buyer has no window")
	}
	// Unknown number: ignored.
	waHook(t, h, msg("PNX", buyer, "hola"))
	if got := g.texts("PNX"); len(got) != 0 {
		t.Fatalf("unknown number answered: %v", got)
	}
	// A store's own chat (no live sale): AndRho stays quiet.
	waHook(t, h, msg("PN1", "50277778888", "¿a qué hora abren?"))
	if got := g.texts("PN1"); len(got) != 1 {
		t.Fatalf("AndRho must not answer the store's chats: %v", got)
	}

	// Template approved, payment failing, then the store removes AndRho.
	waHook(t, h, `{"object":"whatsapp_business_account","entry":[{"id":"WABA1","changes":[{"field":"message_template_status_update",
		"value":{"event":"APPROVED","message_template_id":1,"message_template_name":"andrho_confirmar_apartado","message_template_language":"es"}}]}]}`)
	if !db.WhatsAppTemplateApproved(ctx, pool, line.acc.ID, tplLiveConfirm, tplLang) {
		t.Fatal("template approved")
	}
	waHook(t, h, `{"object":"whatsapp_business_account","entry":[{"id":"WABA1","changes":[{"field":"messages",
		"value":{"metadata":{"phone_number_id":"PN1"},"statuses":[{"status":"failed","errors":[{"code":131042}]}]}}]}]}`)
	if _, st = waDo(t, r, accountID, "viewer", "GET", "/whatsapp/business", nil); st["number"].(map[string]any)["needs_payment"] != true {
		t.Fatalf("payment: %v", st["number"])
	}
	if _, ok := h.buyerLine(ctx, accountID); !ok {
		t.Fatal("without payment it still answers inside the window")
	}
	waHook(t, h, `{"object":"whatsapp_business_account","entry":[{"id":"WABA1","changes":[{"field":"account_update","value":{"event":"PARTNER_REMOVED"}}]}]}`)
	if _, ok := h.buyerLine(ctx, accountID); ok {
		t.Fatal("removed by the store: no number")
	}
	var left int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM whatsapp_accounts WHERE account_id = $1 AND token IS NOT NULL`, accountID).Scan(&left)
	if left != 0 {
		t.Fatal("tokens erased")
	}

	// Coexistence: no register, both syncs; then disconnect from the panel.
	g.coex = true
	g.pins = nil
	if code, st = waDo(t, r, accountID, "owner", "POST", "/whatsapp/business/connect",
		map[string]any{"code": "CODE2", "event": "FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING"}); code != 200 {
		t.Fatalf("%d %v", code, st)
	}
	if st["number"].(map[string]any)["coexistence"] != true || len(g.pins) != 0 || !g.called("POST /PN1/smb_app_data") {
		t.Fatalf("coexistence: %v %v", st["number"], g.pins)
	}
	if code, _ := waDo(t, r, accountID, "viewer", "DELETE", "/whatsapp/business", nil); code != http.StatusForbidden {
		t.Fatalf("only admins disconnect: %d", code)
	}
	if code, _ := waDo(t, r, accountID, "owner", "DELETE", "/whatsapp/business", nil); code != http.StatusNoContent {
		t.Fatalf("%d", code)
	}
	if !g.called("DELETE /WABA1/subscribed_apps") || g.called("POST /PN1/deregister") {
		t.Fatal("disconnect unsubscribes and never deregisters")
	}
	if _, st = waDo(t, r, accountID, "owner", "GET", "/whatsapp/business", nil); st["connected"] != false ||
		st["job"].(map[string]any)["status"] != provision.StatusWaitingClient {
		t.Fatalf("%v", st)
	}
}

func TestWhatsAppBusinessGates(t *testing.T) {
	pool := waTestPool(t)
	ctx := context.Background()
	g := &esGraph{sent: map[string][]map[string]any{}}
	h := waTestHandler(t, pool, g)
	r := waRouter(h)
	base := waTestAccount(t, pool, "base")
	if code, _ := waDo(t, r, base, "owner", "GET", "/whatsapp/business", nil); code != http.StatusForbidden {
		t.Fatalf("base has no own number: %d", code)
	}
	h.Cfg.WhatsAppEmbeddedSignup = false
	other := waTestAccount(t, pool, "plataforma")
	if code, _ := waDo(t, r, other, "owner", "GET", "/whatsapp/business", nil); code != http.StatusNotFound {
		t.Fatalf("flag off: %d", code)
	}
	// The test mode is the only way buyers reach AndRho's number.
	if _, ok := h.buyerLine(ctx, other); ok {
		t.Fatal("no test mode, no number")
	}
	h.Cfg.LiveWhatsAppTest = true
	if l, ok := h.buyerLine(ctx, other); !ok || l.own() || l.biz != waControl {
		t.Fatalf("test mode: %+v %v", l, ok)
	}
}

func TestLiveWhatsAppTestOffInProduction(t *testing.T) {
	t.Setenv("LIVE_WHATSAPP_TEST", "true")
	t.Setenv("APP_ENV", "production")
	if config.Load().LiveWhatsAppTest {
		t.Fatal("LIVE_WHATSAPP_TEST must be ignored in production")
	}
	t.Setenv("APP_ENV", "")
	if !config.Load().LiveWhatsAppTest {
		t.Fatal("outside production it works")
	}
}
