package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/live"
	"github.com/IngeniosoHacker/andrho-api/internal/meta"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

// Venta en vivo, the dashboard's "En vivo" section (founder/live-sale-spec.md).
// Every /live/* route needs the plan's live_sale capability (Plataforma and
// up); connecting Facebook also needs AndRho's Meta app (META_APP_ID…).

// liveGate answers 403 unless the account's plan includes live sales.
func (h *Handler) liveGate(c *gin.Context) (accountID, userID string, ok bool) {
	claims, okc := currentClaims(c)
	if !okc {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return "", "", false
	}
	st, err := h.liveFeature(c.Request.Context(), claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return "", "", false
	}
	if !st.Available {
		c.JSON(http.StatusForbidden, gin.H{"error": "plan", "message": "La venta en vivo viene desde el plan Plataforma."})
		return "", "", false
	}
	if !st.Enabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "feature_off", "message": "La venta en vivo está apagada. Actívala en Configuración."})
		return "", "", false
	}
	return claims.AccountID, claims.Sub, true
}

// liveERP is the account's ERP client, answering the error itself.
func (h *Handler) liveERP(c *gin.Context, accountID string) (*odoo.Client, bool) {
	cl, err := h.odooFor(c.Request.Context(), accountID)
	if errors.Is(err, db.ErrNotFound) {
		respondError(c, http.StatusNotFound, "erp_not_connected")
		return nil, false
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	return cl, true
}

func (h *Handler) liveFail(c *gin.Context, where string, err error) {
	log.Printf("live: %s: %v", where, err)
	if errors.Is(err, odoo.ErrAuth) {
		respondError(c, http.StatusBadGateway, "tu ERP rechazó el acceso de AndRho; vuelve a conectarlo")
		return
	}
	if errors.Is(err, meta.ErrTokenInvalid) {
		c.JSON(http.StatusBadGateway, gin.H{"error": "meta_reconnect", "message": "Facebook pide conectar tu Página otra vez."})
		return
	}
	respondError(c, http.StatusBadGateway, "no pudimos completar eso ahora; inténtalo en un momento")
}

type pageView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Picture    string `json:"picture"`
	Instagram  bool   `json:"instagram"`
	Selected   bool   `json:"selected"`
	Subscribed bool   `json:"subscribed"`
}

// LiveStatus handles GET /live/status: what the "En vivo" section needs to
// know before anything else.
func (h *Handler) LiveStatus(c *gin.Context) {
	claims, _ := currentClaims(c)
	ctx := c.Request.Context()
	st, err := h.liveFeature(ctx, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	out := gin.H{"plan_allows": st.Available, "enabled": st.Enabled, "meta_ready": h.Meta.Enabled(), "pages": []pageView{}, "erp": "not_connected",
		"confirm_minutes": h.Cfg.LiveConfirmMinutes}
	if pages, err := db.ListMetaPages(ctx, h.Accounts, claims.AccountID); err == nil {
		views := []pageView{}
		for _, p := range pages {
			views = append(views, pageView{ID: p.PageID, Name: p.Name, Picture: p.Picture, Instagram: p.IGUserID != "", Selected: p.Selected, Subscribed: p.Subscribed})
		}
		out["pages"] = views
	}
	if cl, err := h.odooFor(ctx, claims.AccountID); err == nil {
		out["erp"] = "connected"
		if cl.HasModel(ctx, liveModel) {
			out["erp"] = "ready"
		}
	}
	c.JSON(http.StatusOK, out)
}

// LiveSetup handles POST /live/setup (admin+): installs the ERP's live
// sales module the first time.
func (h *Handler) LiveSetup(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if cl.HasModel(ctx, liveModel) {
		c.JSON(http.StatusOK, gin.H{"installed": true})
		return
	}
	if err := cl.UpdateModuleList(ctx); err != nil {
		h.liveFail(c, "update module list", err)
		return
	}
	if _, err := cl.InstallModules(ctx, []string{liveModule}); err != nil {
		log.Printf("live: install %s for %s: %v", liveModule, accountID, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "erp_update", "message": "Estamos actualizando tu ERP para la venta en vivo. Vuelve a intentarlo en unos minutos."})
		return
	}
	installedModels.Delete(accountID) // the search bar now offers it
	c.JSON(http.StatusOK, gin.H{"installed": true})
}

// --- Facebook Login ------------------------------------------------------------

func (h *Handler) metaRedirectURI() string { return h.Cfg.PublicURL + "/meta/callback" }

// signState makes Facebook Login's state: account, user and expiry, signed.
func (h *Handler) signState(accountID, userID string) string {
	body := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%s|%d", accountID, userID, time.Now().Add(15*time.Minute).Unix())))
	mac := hmac.New(sha256.New, []byte(h.Cfg.JWTSecret))
	mac.Write([]byte("meta-state:" + body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h *Handler) readState(state string) (accountID, userID string, ok bool) {
	body, sig, found := strings.Cut(state, ".")
	if !found {
		return "", "", false
	}
	mac := hmac.New(sha256.New, []byte(h.Cfg.JWTSecret))
	mac.Write([]byte("meta-state:" + body))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// LiveMetaConnect handles GET /live/meta/connect (admin+): the Facebook
// Login address for the seller's Page.
func (h *Handler) LiveMetaConnect(c *gin.Context) {
	accountID, userID, ok := h.liveGate(c)
	if !ok {
		return
	}
	if !h.Meta.Enabled() || h.Cfg.PublicURL == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "meta_not_ready", "message": "La conexión con Facebook se activa cuando Meta apruebe a AndRho."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": h.Meta.LoginURL(h.metaRedirectURI(), h.signState(accountID, userID))})
}

// back is where the callback sends the seller: the dashboard's En vivo.
func (h *Handler) liveBack(result string) string {
	base := h.Cfg.DashboardURL
	if base == "" {
		base = "/dashboard/"
	}
	return strings.TrimRight(base, "/") + "/#/envivo?meta=" + url.QueryEscape(result)
}

// MetaCallback handles GET /meta/callback (public): Facebook Login comes
// back here. The seller's Pages are stored; with a single Page it's
// selected and subscribed right away.
func (h *Handler) MetaCallback(c *gin.Context) {
	accountID, userID, ok := h.readState(c.Query("state"))
	if !ok || c.Query("code") == "" || !h.Meta.Enabled() {
		c.Redirect(http.StatusFound, h.liveBack("error"))
		return
	}
	ctx := c.Request.Context()
	userToken, err := h.Meta.ExchangeCode(ctx, c.Query("code"), h.metaRedirectURI())
	if err != nil {
		log.Printf("meta: exchange code for %s: %v", accountID, err)
		c.Redirect(http.StatusFound, h.liveBack("error"))
		return
	}
	pages, err := h.Meta.Pages(ctx, userToken)
	if err != nil || len(pages) == 0 {
		log.Printf("meta: pages for %s: %v (%d)", accountID, err, len(pages))
		c.Redirect(http.StatusFound, h.liveBack("no_pages"))
		return
	}
	for _, p := range pages {
		sealed, err := h.Secrets.Seal(p.AccessToken)
		if err != nil {
			c.Redirect(http.StatusFound, h.liveBack("error"))
			return
		}
		if err := db.SaveMetaPage(ctx, h.Accounts, db.MetaPage{PageID: p.ID, AccountID: accountID, Name: p.Name, Picture: p.Picture,
			IGUserID: p.IGUserID, Token: sealed}, userID); err != nil {
			log.Printf("meta: save page %s: %v", p.ID, err)
		}
	}
	if len(pages) == 1 {
		if err := h.selectPage(ctx, accountID, pages[0].ID); err != nil {
			log.Printf("meta: select page %s: %v", pages[0].ID, err)
		}
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), accountID, &userID, "meta_connected", fmt.Sprintf("Facebook conectado: %d página(s)", len(pages)))
	c.Redirect(http.StatusFound, h.liveBack("ok"))
}

func (h *Handler) selectPage(ctx context.Context, accountID, pageID string) error {
	page, err := h.pageFor(ctx, pageID)
	if err != nil {
		return err
	}
	if page.AccountID != accountID {
		return db.ErrNotFound
	}
	subErr := h.Meta.Subscribe(ctx, page.PageID, page.token)
	if subErr != nil {
		log.Printf("meta: subscribe %s: %v", pageID, subErr)
	}
	return db.SelectMetaPage(ctx, h.Accounts, accountID, pageID, subErr == nil)
}

// LiveSelectPage handles PUT /live/meta/page (admin+): {page_id}.
func (h *Handler) LiveSelectPage(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	var req struct {
		PageID string `json:"page_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.PageID == "" {
		respondError(c, http.StatusBadRequest, "elige una página")
		return
	}
	if err := h.selectPage(c.Request.Context(), accountID, req.PageID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusNotFound, "esa página no está conectada")
			return
		}
		h.liveFail(c, "select page", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"selected": req.PageID})
}

// LiveDisconnect handles DELETE /live/meta (admin+).
func (h *Handler) LiveDisconnect(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if page, err := h.selectedPage(ctx, accountID); err == nil {
		_ = h.Meta.Unsubscribe(ctx, page.PageID, page.token)
	}
	if err := db.DeleteMetaPages(ctx, h.Accounts, accountID); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"disconnected": true})
}

// --- products and sessions -------------------------------------------------------

// LiveProducts handles GET /live/products: what the seller can pick for a
// live (products with stock), from the ERP's Inventario.
func (h *Handler) LiveProducts(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	var rows []map[string]any
	domain := []any{[]any{"sale_ok", "=", true}, []any{"qty_available", ">", 0}}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		domain = append(domain, []any{"name", "ilike", q})
	}
	err := cl.SearchRead(c.Request.Context(), "product.product", domain,
		[]string{"id", "display_name", "lst_price", "qty_available", "image_128", "description_sale"}, "write_date desc", 300, &rows)
	if err != nil {
		h.liveFail(c, "products", err)
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		img := odoo.Str(r["image_128"])
		out = append(out, gin.H{"id": int(odoo.Num(r["id"])), "name": odoo.Str(r["display_name"]), "price": odoo.Num(r["lst_price"]),
			"stock": odoo.Num(r["qty_available"]), "image": img, "description": odoo.Str(r["description_sale"])})
	}
	c.JSON(http.StatusOK, gin.H{"products": out})
}

type liveCreateRequest struct {
	Name  string `json:"name"`
	Items []struct {
		ProductID int      `json:"product_id"`
		Price     *float64 `json:"price"`
	} `json:"items"`
}

// LiveCreate handles POST /live/sessions (editor+), the "Organizar venta"
// button: AndRho orders the picked products, names them for the live and
// creates the live in the ERP.
func (h *Handler) LiveCreate(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	var req liveCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Items) == 0 || len(req.Items) > 200 || len(req.Name) > 120 {
		respondError(c, http.StatusBadRequest, "elige entre 1 y 200 artículos")
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	ids := []any{}
	for _, it := range req.Items {
		ids = append(ids, it.ProductID)
	}
	var rows []map[string]any
	if err := cl.SearchRead(ctx, "product.product", []any{[]any{"id", "in", ids}}, []string{"id", "name", "lst_price", "qty_available"}, "", 0, &rows); err != nil {
		h.liveFail(c, "read products", err)
		return
	}
	byID := map[int]map[string]any{}
	for _, r := range rows {
		byID[int(odoo.Num(r["id"]))] = r
	}
	items := []live.Item{}
	for _, it := range req.Items {
		r, found := byID[it.ProductID]
		if !found {
			continue
		}
		price := odoo.Num(r["lst_price"])
		if it.Price != nil && *it.Price >= 0 {
			price = *it.Price
		}
		items = append(items, live.Item{ID: it.ProductID, Name: odoo.Str(r["name"]), Price: price, Stock: odoo.Num(r["qty_available"])})
	}
	if len(items) == 0 {
		respondError(c, http.StatusBadRequest, "esos productos ya no están en tu inventario")
		return
	}
	ordered := live.Names(live.Order(items))
	payload := make([]map[string]any, 0, len(ordered))
	for _, it := range ordered {
		payload = append(payload, map[string]any{"product_id": it.ID, "price": it.Price, "live_name": it.LiveName})
	}
	liveID, err := erpLiveCreate(ctx, cl, req.Name, payload)
	if err != nil {
		h.liveFail(c, "create live", err)
		return
	}
	// Mark it organized (state ready): the order is already the ERP's.
	sum, err := erpLiveSummary(ctx, cl, liveID)
	if err == nil {
		seq := []map[string]any{}
		for i, l := range sum.Lines {
			seq = append(seq, map[string]any{"line_id": l.ID, "sequence": (i + 1) * 10})
		}
		err = erpLiveSetOrder(ctx, cl, liveID, seq)
	}
	if err != nil {
		h.liveFail(c, "order live", err)
		return
	}
	pageID := ""
	if p, err := db.SelectedMetaPage(ctx, h.Accounts, accountID); err == nil {
		pageID = p.PageID
	}
	sess := db.LiveSession{ID: uuid.NewString(), AccountID: accountID, OdooLiveID: liveID, PageID: pageID}
	if err := db.CreateLiveSession(ctx, h.Accounts, sess); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	h.liveSessionJSON(c, sess.ID, accountID)
}

// liveSessionJSON answers a session with the ERP's summary and activity.
func (h *Handler) liveSessionJSON(c *gin.Context, id, accountID string) {
	ctx := c.Request.Context()
	sess, err := db.GetLiveSession(ctx, h.Accounts, accountID, id)
	if errors.Is(err, db.ErrNotFound) {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	sum, err := erpLiveSummary(ctx, cl, sess.OdooLiveID)
	if err != nil {
		h.liveFail(c, "summary", err)
		return
	}
	claims, _ := db.ListSessionClaims(ctx, h.Accounts, sess.ID, 100)
	activity := []gin.H{}
	names := map[int]string{}
	for _, l := range sum.Lines {
		names[l.ID] = l.LiveName
	}
	for _, cl := range claims {
		activity = append(activity, gin.H{"id": cl.ID, "item": names[cl.OdooLineID], "line_id": cl.OdooLineID, "buyer": cl.AuthorName,
			"comment": cl.CommentText, "state": cl.State, "source": cl.Source, "at": cl.CommentedAt})
	}
	c.JSON(http.StatusOK, gin.H{"session": gin.H{"id": sess.ID, "status": sess.Status, "page_id": sess.PageID, "live_url": sess.LiveURL,
		"platform_live_id": sess.PlatformLiveID, "started_at": sess.StartedAt, "ended_at": sess.EndedAt, "created_at": sess.CreatedAt,
		"watching": isWatching(sess.ID)}, "live": sum, "activity": activity})
}

func isWatching(id string) bool { _, ok := liveWorkers.Load(id); return ok }

// LiveGet handles GET /live/sessions/:id.
func (h *Handler) LiveGet(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	h.liveSessionJSON(c, c.Param("id"), accountID)
}

// LiveList handles GET /live/sessions: the history, with each live's
// numbers from the ERP (including lives made there directly).
func (h *Handler) LiveList(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !cl.HasModel(ctx, liveModel) {
		c.JSON(http.StatusOK, gin.H{"lives": []any{}, "erp_ready": false})
		return
	}
	var rows []map[string]any
	if err := cl.SearchRead(ctx, liveModel, nil, []string{"id", "name", "date", "state", "item_count", "sold_count", "unsold_count",
		"reserved_count", "revenue", "buyer_count", "started_at", "ended_at"}, "date desc", 100, &rows); err != nil {
		h.liveFail(c, "history", err)
		return
	}
	sessions, _ := db.ListLiveSessions(ctx, h.Accounts, accountID, 200)
	byLive := map[int]db.LiveSession{}
	for _, s := range sessions {
		byLive[s.OdooLiveID] = s
	}
	out := []gin.H{}
	for _, r := range rows {
		id := int(odoo.Num(r["id"]))
		row := gin.H{"odoo_id": id, "name": odoo.Str(r["name"]), "date": odoo.Str(r["date"]), "state": odoo.Str(r["state"]),
			"items": int(odoo.Num(r["item_count"])), "sold": int(odoo.Num(r["sold_count"])), "unsold": int(odoo.Num(r["unsold_count"])),
			"reserved": int(odoo.Num(r["reserved_count"])), "revenue": odoo.Num(r["revenue"]), "buyers": int(odoo.Num(r["buyer_count"])),
			"started_at": odoo.Str(r["started_at"]), "ended_at": odoo.Str(r["ended_at"])}
		if s, found := byLive[id]; found {
			row["session_id"] = s.ID
			row["status"] = s.Status
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"lives": out, "erp_ready": true})
}

var liveIDInURL = regexp.MustCompile(`(?:/videos/(?:[^/]+/)?|[?&]v=|/live_videos/)(\d{6,})`)

// LiveStart handles POST /live/sessions/:id/start (editor+): {live_url}.
// AndRho finds the broadcast (from the link, or the Page's live on air)
// and starts reading its comments.
func (h *Handler) LiveStart(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	var req struct {
		LiveURL string `json:"live_url"`
	}
	_ = c.ShouldBindJSON(&req)
	req.LiveURL = strings.TrimSpace(req.LiveURL)
	ctx := c.Request.Context()
	sess, err := db.GetLiveSession(ctx, h.Accounts, accountID, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	if sess.Status == "done" {
		respondError(c, http.StatusConflict, "ese live ya terminó")
		return
	}
	page, err := h.selectedPage(ctx, accountID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "no_page", "message": "Conecta tu Página de Facebook para que AndRho lea los comentarios."})
		return
	}
	platformID := ""
	if m := liveIDInURL.FindStringSubmatch(req.LiveURL); m != nil {
		platformID = m[1]
	}
	// The Page's live on air is the reliable id (a video link may carry the
	// video's id, not the broadcast's).
	if v, found, err := h.Meta.CurrentLive(ctx, page.PageID, page.token); err == nil && found {
		platformID = v.ID
		if req.LiveURL == "" {
			req.LiveURL = v.Permalink
		}
	} else if err != nil && errors.Is(err, meta.ErrTokenInvalid) {
		h.liveFail(c, "current live", err)
		return
	}
	if platformID == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "not_live", "message": "No vemos tu live al aire todavía. Inícialo en Facebook y vuelve a tocar \"Empezar\"."})
		return
	}
	if err := h.beginLive(ctx, sess, page.PageID, platformID, req.LiveURL); err != nil {
		h.liveFail(c, "start", err)
		return
	}
	h.liveSessionJSON(c, sess.ID, accountID)
}

func (h *Handler) beginLive(ctx context.Context, sess db.LiveSession, pageID, platformID, liveURL string) error {
	cl, err := h.odooFor(ctx, sess.AccountID)
	if err != nil {
		return err
	}
	if err := erpLiveStart(ctx, cl, sess.OdooLiveID, liveURL, platformID); err != nil {
		return err
	}
	if err := db.StartLiveSession(ctx, h.Accounts, sess.ID, pageID, platformID, liveURL); err != nil {
		return err
	}
	h.startLiveWorker(sess.ID)
	return nil
}

// LiveEnd handles POST /live/sessions/:id/end (editor+).
func (h *Handler) LiveEnd(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	sess, err := db.GetLiveSession(ctx, h.Accounts, accountID, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	if sess.Status != "done" {
		h.endLive(ctx, sess)
	}
	h.liveSessionJSON(c, sess.ID, accountID)
}

// LivePublish handles POST /live/sessions/:id/publish (editor+): one post
// per unsold item (photo, price, "comenta Mío"), {line_ids} to choose.
func (h *Handler) LivePublish(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	var req struct {
		LineIDs []int `json:"line_ids"`
	}
	_ = c.ShouldBindJSON(&req)
	ctx := c.Request.Context()
	sess, err := db.GetLiveSession(ctx, h.Accounts, accountID, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "not found")
		return
	}
	page, err := h.selectedPage(ctx, accountID)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "no_page", "message": "Conecta tu Página de Facebook para publicar."})
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	sum, err := erpLiveSummary(ctx, cl, sess.OdooLiveID)
	if err != nil {
		h.liveFail(c, "summary", err)
		return
	}
	want := map[int]bool{}
	for _, id := range req.LineIDs {
		want[id] = true
	}
	loyalty, _ := erpGetParam(ctx, cl, liveParams["loyalty_enabled"])
	word, _ := erpGetParam(ctx, cl, liveParams["claim_word"])
	posted, failed := 0, 0
	for _, l := range sum.Lines {
		if l.State != "unsold" || (len(want) > 0 && !want[l.ID]) {
			continue
		}
		img, desc, err := erpPostData(ctx, cl, l.ID)
		if err != nil || len(img) == 0 {
			failed++
			continue
		}
		caption := live.PostCaption(l.Price, desc, loyalty == "1" || loyalty == "true", word)
		postID, err := h.Meta.PostPhoto(ctx, page.PageID, page.token, img, fmt.Sprintf("articulo-%d.png", l.ID), caption)
		if err != nil {
			log.Printf("live: post line %d: %v", l.ID, err)
			if errors.Is(err, meta.ErrTokenInvalid) {
				h.liveFail(c, "post", err)
				return
			}
			failed++
			continue
		}
		_ = erpMarkPosted(ctx, cl, l.ID, postID)
		_ = db.SaveLivePost(ctx, h.Accounts, db.LivePost{PostID: postID, AccountID: accountID, SessionID: sess.ID, PageID: page.PageID, OdooLineID: l.ID})
		posted++
	}
	c.JSON(http.StatusOK, gin.H{"posted": posted, "failed": failed})
}

// --- cards, settings, metrics -----------------------------------------------------

// LiveCards handles GET /live/cards?state=ready: shipping cards.
func (h *Handler) LiveCards(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	domain := []any{}
	if st := c.Query("state"); st != "" {
		domain = append(domain, []any{"state", "=", st})
	}
	var rows []map[string]any
	if err := cl.SearchRead(c.Request.Context(), cardModel, domain, []string{"id", "buyer_name", "first_name", "phone", "address", "zone",
		"item_count", "total", "discount_pct", "state", "live_id", "create_date"}, "create_date desc", 200, &rows); err != nil {
		h.liveFail(c, "cards", err)
		return
	}
	out := []gin.H{}
	for _, r := range rows {
		out = append(out, gin.H{"id": int(odoo.Num(r["id"])), "buyer": odoo.Str(r["buyer_name"]), "first_name": odoo.Str(r["first_name"]),
			"phone": odoo.Str(r["phone"]), "address": odoo.Str(r["address"]), "zone": odoo.Str(r["zone"]), "items": int(odoo.Num(r["item_count"])),
			"total": odoo.Num(r["total"]), "discount_pct": odoo.Num(r["discount_pct"]), "state": odoo.Str(r["state"]),
			"live": odoo.M2O(r["live_id"]), "created_at": odoo.Str(r["create_date"])})
	}
	c.JSON(http.StatusOK, gin.H{"cards": out})
}

// LiveCardsPrint handles POST /live/cards/print: {card_ids}. It answers
// the ERP address of the PDF (four 10 × 10 cm cards per page), opened
// with the dashboard's entry token.
func (h *Handler) LiveCardsPrint(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	var req struct {
		CardIDs []int `json:"card_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.CardIDs) == 0 || len(req.CardIDs) > 400 {
		respondError(c, http.StatusBadRequest, "elige las tarjetas a imprimir")
		return
	}
	ids := make([]string, 0, len(req.CardIDs))
	for _, id := range req.CardIDs {
		ids = append(ids, strconv.Itoa(id))
	}
	if cl, err := h.odooFor(c.Request.Context(), accountID); err == nil {
		_ = cl.Exec(c.Request.Context(), cardModel, "write", []any{req.CardIDs, map[string]any{"state": "printed"}}, nil, nil)
	}
	h.ssoTo(c, "/report/pdf/andrho_live_sale.report_shipping_cards/"+strings.Join(ids, ","))
}

// LiveSettings handles GET /live/settings: the card's message and logo
// and the loyalty program.
func (h *Handler) LiveSettings(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	out := gin.H{}
	for k, param := range liveParams {
		v, err := erpGetParam(c.Request.Context(), cl, param)
		if err != nil {
			h.liveFail(c, "settings", err)
			return
		}
		out[k] = v
	}
	c.JSON(http.StatusOK, out)
}

// LiveSaveSettings handles PUT /live/settings (admin+).
func (h *Handler) LiveSaveSettings(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	clean := map[string]string{}
	for k, v := range req {
		if _, known := liveParams[k]; !known {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "card_message":
			if v == "" || len([]rune(v)) > 120 {
				respondError(c, http.StatusBadRequest, "el mensaje de la tarjeta va de 1 a 120 letras")
				return
			}
		case "card_show_logo", "loyalty_enabled":
			if v != "1" && v != "0" {
				respondError(c, http.StatusBadRequest, k+" es 1 o 0")
				return
			}
		case "claim_word":
			if !live.ValidClaimWord(v) {
				respondError(c, http.StatusBadRequest, "la palabra para pedir va de 1 a 3 palabras, sin emojis, hasta 20 letras")
				return
			}
		case "loyalty_min":
			if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 100 {
				respondError(c, http.StatusBadRequest, "las compras para ser frecuente van de 1 a 100")
				return
			}
		case "loyalty_discount":
			if f, err := strconv.ParseFloat(v, 64); err != nil || f < 0 || f > 50 {
				respondError(c, http.StatusBadRequest, "el descuento va de 0 a 50%")
				return
			}
		}
		clean[k] = v
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	for k, v := range clean {
		if err := erpSetParam(c.Request.Context(), cl, liveParams[k], v); err != nil {
			h.liveFail(c, "save settings", err)
			return
		}
	}
	h.LiveSettings(c)
}

// LiveMetrics handles GET /live/metrics: how the lives go, for AndRho's
// analysis (sales per live, how many apartados become sales, the best
// hours, top buyers and products).
func (h *Handler) LiveMetrics(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if !cl.HasModel(ctx, liveModel) {
		c.JSON(http.StatusOK, gin.H{"lives": 0})
		return
	}
	var lives []map[string]any
	if err := cl.SearchRead(ctx, liveModel, []any{[]any{"state", "=", "done"}}, []string{"id", "name", "date", "started_at", "sold_count",
		"unsold_count", "revenue", "buyer_count", "item_count"}, "date desc", 60, &lives); err != nil {
		h.liveFail(c, "metrics", err)
		return
	}
	var sold, unsold, items int
	var revenue float64
	byHour := map[int][2]float64{} // hour -> lives, revenue
	series := []gin.H{}
	for i := len(lives) - 1; i >= 0; i-- {
		l := lives[i]
		sold += int(odoo.Num(l["sold_count"]))
		unsold += int(odoo.Num(l["unsold_count"]))
		items += int(odoo.Num(l["item_count"]))
		revenue += odoo.Num(l["revenue"])
		series = append(series, gin.H{"name": odoo.Str(l["name"]), "date": odoo.Str(l["date"]), "revenue": odoo.Num(l["revenue"]),
			"sold": int(odoo.Num(l["sold_count"])), "items": int(odoo.Num(l["item_count"]))})
		when := odoo.Str(l["started_at"])
		if when == "" {
			when = odoo.Str(l["date"])
		}
		if t, err := time.Parse("2006-01-02 15:04:05", when); err == nil {
			// The ERP stores UTC; Guatemala is UTC-6 all year.
			hour := t.Add(-6 * time.Hour).Hour()
			v := byHour[hour]
			byHour[hour] = [2]float64{v[0] + 1, v[1] + odoo.Num(l["revenue"])}
		}
	}
	hours := []gin.H{}
	for hr, v := range byHour {
		hours = append(hours, gin.H{"hour": hr, "lives": int(v[0]), "avg_revenue": v[1] / v[0]})
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i]["hour"].(int) < hours[j]["hour"].(int) })
	var buyers []map[string]any
	_ = cl.SearchRead(ctx, buyerModel, []any{[]any{"purchase_count", ">", 0}}, []string{"name", "purchase_count", "total_spent", "frequent"}, "purchase_count desc, total_spent desc", 10, &buyers)
	top := []gin.H{}
	for _, b := range buyers {
		top = append(top, gin.H{"name": odoo.Str(b["name"]), "purchases": int(odoo.Num(b["purchase_count"])), "total": odoo.Num(b["total_spent"]), "frequent": b["frequent"] == true})
	}
	var products []map[string]any
	_ = cl.Exec(ctx, lineModel, "read_group", []any{[]any{[]any{"state", "=", "confirmed"}}, []string{"price:sum"}, []string{"product_id"}},
		map[string]any{"orderby": "price desc", "limit": 10, "lazy": false}, &products)
	topProducts := []gin.H{}
	for _, p := range products {
		topProducts = append(topProducts, gin.H{"product": odoo.M2O(p["product_id"]), "count": int(odoo.Num(p["__count"])), "revenue": odoo.Num(p["price"])})
	}
	rate := 0.0
	if sold+unsold > 0 {
		rate = float64(sold) / float64(sold+unsold)
	}
	var claims struct{ Asked, Confirmed int }
	_ = h.Accounts.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state IN ('asked','confirmed','declined','expired')),
		count(*) FILTER (WHERE state = 'confirmed') FROM live_claims WHERE account_id = $1`, accountID).Scan(&claims.Asked, &claims.Confirmed)
	confirmRate := 0.0
	if claims.Asked > 0 {
		confirmRate = float64(claims.Confirmed) / float64(claims.Asked)
	}
	c.JSON(http.StatusOK, gin.H{"lives": len(lives), "sold": sold, "unsold": unsold, "items": items, "revenue": revenue,
		"sell_through": rate, "confirm_rate": confirmRate, "by_hour": hours, "series": series, "top_buyers": top, "top_products": topProducts})
}

// --- Meta webhook -------------------------------------------------------------------

// MetaWebhookVerify handles GET /meta/webhook: Meta's subscription check.
func (h *Handler) MetaWebhookVerify(c *gin.Context) {
	if c.Query("hub.mode") == "subscribe" && h.Cfg.MetaVerifyToken != "" &&
		hmac.Equal([]byte(c.Query("hub.verify_token")), []byte(h.Cfg.MetaVerifyToken)) {
		c.String(http.StatusOK, c.Query("hub.challenge"))
		return
	}
	c.Status(http.StatusForbidden)
}

// MetaWebhook handles POST /meta/webhook: Page events. The signature is
// checked, Meta gets its 200 right away and the events are handled after.
func (h *Handler) MetaWebhook(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 2<<20))
	if err != nil || !h.Meta.VerifySignature(body, c.GetHeader("X-Hub-Signature-256")) {
		c.Status(http.StatusForbidden)
		return
	}
	var wh meta.Webhook
	if err := jsonUnmarshal(body, &wh); err != nil || wh.Object != "page" {
		c.Status(http.StatusOK)
		return
	}
	c.Status(http.StatusOK)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		h.handleMetaEvents(ctx, wh)
	}()
}

func (h *Handler) handleMetaEvents(ctx context.Context, wh meta.Webhook) {
	for _, e := range wh.Entry {
		page, err := h.pageFor(ctx, e.ID)
		if err != nil {
			continue // a Page AndRho doesn't serve (anymore)
		}
		if st, err := h.liveFeature(ctx, page.AccountID); err != nil || !st.Enabled {
			continue // live sales are off for this account (plan or choice)
		}
		for _, ch := range e.Changes {
			v := ch.Value
			switch ch.Field {
			case "feed":
				if v.Item != "comment" || v.Verb != "add" || v.CommentID == "" {
					continue
				}
				if post, err := db.GetLivePost(ctx, h.Accounts, v.PostID); err == nil && post.AccountID == page.AccountID {
					h.postComment(ctx, page, post, v)
				}
			case "live_videos":
				h.liveVideoEvent(ctx, page, v)
			}
		}
		for _, m := range e.Messaging {
			psid := m.Sender.ID
			if psid == "" || psid == page.PageID {
				continue
			}
			switch {
			case m.Postback != nil:
				h.livePostback(ctx, page, psid, m.Postback.Payload)
			case m.Message != nil && m.Message.IsEcho:
			case m.Message != nil && m.Message.QuickReply != nil:
				h.livePostback(ctx, page, psid, m.Message.QuickReply.Payload)
			case m.Message != nil && m.Message.Text != "":
				h.liveText(ctx, page, psid, m.Message.Text)
			}
		}
	}
}

// liveVideoEvent: the Page went live (AndRho starts the latest organized
// live by itself) or stopped (the live ends).
func (h *Handler) liveVideoEvent(ctx context.Context, page pageCtx, v meta.ChangeValue) {
	status := strings.ToLower(v.Status)
	switch {
	case status == "live" && v.ID != "":
		if !page.Selected {
			return
		}
		sess, err := db.LatestReadySession(ctx, h.Accounts, page.AccountID)
		if err != nil {
			return
		}
		if err := h.beginLive(ctx, sess, page.PageID, v.ID, ""); err != nil {
			log.Printf("live: auto-start %s: %v", sess.ID, err)
		}
	case (status == "live_stopped" || status == "vod" || status == "processing") && v.ID != "":
		sessions, err := db.ActiveLiveSessions(ctx, h.Accounts)
		if err != nil {
			return
		}
		for _, s := range sessions {
			if s.PlatformLiveID == v.ID && s.AccountID == page.AccountID {
				h.endLive(ctx, s)
			}
		}
	}
}
