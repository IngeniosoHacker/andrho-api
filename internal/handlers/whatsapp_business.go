package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// Each store's own WhatsApp (docs/auditoria-whatsapp-por-cliente.md):
//
//   - everything that goes to a buyer leaves from the store's number, with
//     its name. Without a connected number there's no WhatsApp with buyers:
//     live sales and the CRM use Messenger only, and the dashboard says
//     "Conecta tu WhatsApp". AndRho's number is never the fallback, except
//     with LIVE_WHATSAPP_TEST outside production (APP_ENV);
//   - AndRho's number (h.WA) is only the control channel: owners and
//     their team talking to AndRho;
//   - a webhook goes by metadata.phone_number_id: AndRho's number to the
//     control routes, a store's number to its buyers' routes, an unknown
//     number is logged and ignored.
//
// The store connects with Embedded Signup (v5) from the dashboard; AndRho
// exchanges the code, subscribes to the store's WABA, registers the number
// (or, with Coexistence, asks for the syncs Meta requires and keeps none
// of it), reads its name and quality, and creates the templates it needs.
// Behind WHATSAPP_EMBEDDED_SIGNUP_ENABLED.

// waControl is AndRho's number in the per-number tables.
const waControl = ""

// waLine is a business number buyers talk to: a store's own, or AndRho's
// while testing (Biz "").
type waLine struct {
	wa  *whatsapp.Client
	biz string             // phone_number_id; "" = AndRho's (test)
	acc db.WhatsAppAccount // zero for AndRho's
}

func (l waLine) own() bool { return l.acc.ID != "" }

// number is the number buyers write to (digits).
func (l waLine) number(ctx context.Context) string {
	if l.own() {
		return whatsapp.Digits(l.acc.DisplayPhoneNumber)
	}
	return l.wa.DisplayNumber(ctx)
}

// signupOn reports whether stores can connect their own number.
func (h *Handler) signupOn() bool {
	return h.Cfg.WhatsAppEmbeddedSignup && h.WAApp.Enabled() && h.Cfg.MetaWAESConfigID != ""
}

// lineFor builds the client of a stored number (nil when its token can't
// be opened or it isn't usable).
func (h *Handler) lineFor(w db.WhatsAppAccount) (waLine, bool) {
	if !w.Usable() {
		return waLine{}, false
	}
	token, err := h.Secrets.Open(w.Token)
	if err != nil {
		log.Printf("whatsapp: open token of %s: %v", w.PhoneNumberID, err)
		return waLine{}, false
	}
	c := whatsapp.ForAccount(token, w.PhoneNumberID, w.WABAID, h.Cfg.MetaGraphVersion)
	if h.WA != nil && h.WA.Graph != "" {
		c.Graph = h.WA.Graph // tests point both to the same fake Graph
	}
	return waLine{wa: c, biz: w.PhoneNumberID, acc: w}, true
}

// buyerLine is the number an account's buyers get: its own, or AndRho's
// in the test mode. ok=false: no WhatsApp with buyers.
func (h *Handler) buyerLine(ctx context.Context, accountID string) (waLine, bool) {
	if h.Cfg.WhatsAppEmbeddedSignup {
		if w, err := db.WhatsAppAccountFor(ctx, h.Accounts, accountID); err == nil {
			if l, ok := h.lineFor(w); ok {
				return l, true
			}
		}
	}
	if h.Cfg.LiveWhatsAppTest && h.WA.Enabled() {
		return waLine{wa: h.WA, biz: waControl}, true
	}
	return waLine{}, false
}

// sendFailed records what a failed send says about the number.
func (h *Handler) sendFailed(l waLine, to string, err error) {
	log.Printf("whatsapp: send from %s to %s: %v", l.biz, maskPhone(to), err)
	if l.own() && errors.Is(err, whatsapp.ErrPayment) {
		_ = db.SetWhatsAppStatus(context.Background(), h.Accounts, l.acc.ID, db.WANoPayment, "Falta el método de pago en WhatsApp Manager.")
	}
}

func (l waLine) say(ctx context.Context, h *Handler, to, text string) {
	if _, err := l.wa.SendText(ctx, to, text); err != nil {
		h.sendFailed(l, to, err)
	}
}

func (l waLine) ask(ctx context.Context, h *Handler, to, text string, buttons []whatsapp.Button) {
	if _, err := l.wa.SendButtons(ctx, to, text, buttons); err != nil {
		h.sendFailed(l, to, err)
	}
}

// --- templates ---------------------------------------------------------------------

// The templates AndRho creates in each store's account (utility: about
// an order the buyer made). They go out only once approved.
const (
	tplLiveConfirm = "andrho_confirmar_apartado"
	tplOrderNotice = "andrho_aviso_pedido"
	tplLang        = "es"
)

var storeTemplates = []whatsapp.Template{
	{Name: tplLiveConfirm, Language: tplLang, Category: "UTILITY",
		Body:    "Hola, apartaste {{1}} por {{2}} en nuestro live. ¿Lo confirmas?",
		Example: []string{"Blusa floreada talla M", "Q150.00"}, Buttons: []string{"Sí, lo quiero", "No"}},
	{Name: tplOrderNotice, Language: tplLang, Category: "UTILITY",
		Body:    "Hola {{1}}, tu pedido {{2}} cambió de estado: {{3}}. Si tienes dudas, responde a este mensaje.",
		Example: []string{"Ana", "AR-7F3K", "va en camino"}},
}

// ensureTemplates creates the missing templates and refreshes the status of
// all of them.
func (h *Handler) ensureTemplates(ctx context.Context, l waLine) {
	have := map[string]whatsapp.TemplateStatus{}
	if list, err := l.wa.Templates(ctx); err == nil {
		for _, t := range list {
			have[t.Name+"|"+t.Language] = t
		}
	} else {
		log.Printf("whatsapp: templates of %s: %v", l.acc.WABAID, err)
	}
	for _, t := range storeTemplates {
		st, ok := have[t.Name+"|"+t.Language]
		if !ok {
			var err error
			st, err = l.wa.CreateTemplate(ctx, t)
			if err != nil {
				log.Printf("whatsapp: create template %s in %s: %v", t.Name, l.acc.WABAID, err)
				st = whatsapp.TemplateStatus{Name: t.Name, Language: t.Language, Status: "ERROR", Reason: "No se pudo crear; se reintenta."}
			}
		}
		if st.Status == "" {
			st.Status = "PENDING"
		}
		_ = db.SaveWhatsAppTemplate(ctx, h.Accounts, l.acc.ID, db.WhatsAppTemplate{Name: t.Name, Language: t.Language,
			MetaID: st.ID, Category: st.Category, Status: strings.ToUpper(st.Status), Reason: st.Reason})
	}
}

// --- reading the number from Meta ---------------------------------------------------

// refreshWhatsApp reads name, quality, payment and templates of a number.
func (h *Handler) refreshWhatsApp(ctx context.Context, w db.WhatsAppAccount) {
	l, ok := h.lineFor(w)
	if !ok {
		return
	}
	info, err := l.wa.PhoneInfo(ctx)
	if err != nil {
		var me *whatsapp.Error
		if errors.As(err, &me) && (me.Code == 190 || me.Status == http.StatusUnauthorized) {
			// The token no longer works: the store removed AndRho.
			_ = db.DisconnectWhatsApp(ctx, h.Accounts, w.ID, "Meta ya no acepta el acceso de AndRho. Vuelve a conectar tu WhatsApp.")
			return
		}
		log.Printf("whatsapp: phone info %s: %v", w.PhoneNumberID, err)
		return
	}
	_ = db.UpdateWhatsAppInfo(ctx, h.Accounts, w.ID, info.DisplayPhoneNumber, info.VerifiedName, info.NameStatus, info.QualityRating)
	if funded, known := l.wa.Funded(ctx); known {
		switch {
		case !funded && w.Status == db.WAActive:
			_ = db.SetWhatsAppStatus(ctx, h.Accounts, w.ID, db.WANoPayment, "Falta el método de pago en WhatsApp Manager.")
		case funded && w.Status == db.WANoPayment:
			_ = db.SetWhatsAppStatus(ctx, h.Accounts, w.ID, db.WAActive, "")
		}
	}
	h.ensureTemplates(ctx, l)
}

// WhatsAppCheckLoop refreshes the connected numbers every few hours
// (quality and payment change without a webhook we can rely on).
func (h *Handler) WhatsAppCheckLoop(ctx context.Context) {
	if !h.Cfg.WhatsAppEmbeddedSignup {
		return
	}
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		list, err := db.WhatsAppAccountsToCheck(ctx, h.Accounts, 6*time.Hour)
		if err == nil {
			for _, w := range list {
				h.refreshWhatsApp(ctx, w)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- webhooks: the stores' accounts ---------------------------------------------------

// waAccountEvent handles a change about a store's account (not a message).
func (h *Handler) waAccountEvent(ctx context.Context, ch whatsapp.Change) {
	v := ch.Value
	switch ch.Field {
	case "message_template_status_update":
		if v.MessageTemplateName != "" && v.Event != "" {
			_ = db.SetTemplateStatusByWABA(ctx, h.Accounts, ch.WABAID, v.MessageTemplateName, v.MessageTemplateLanguage,
				strings.ToUpper(v.Event), v.Reason)
		}
	case "account_update":
		switch strings.ToUpper(v.Event) {
		case "PARTNER_REMOVED", "ACCOUNT_OFFBOARDED", "ACCOUNT_DELETED":
			list, _ := db.WhatsAppAccountsByWABA(ctx, h.Accounts, ch.WABAID)
			for _, w := range list {
				_ = db.DisconnectWhatsApp(ctx, h.Accounts, w.ID, "Quitaste a AndRho de tu cuenta de WhatsApp.")
				log.Printf("whatsapp: %s disconnected by Meta (%s)", w.PhoneNumberID, v.Event)
			}
		}
	case "phone_number_quality_update", "phone_number_name_update":
		list, _ := db.WhatsAppAccountsByWABA(ctx, h.Accounts, ch.WABAID)
		for _, w := range list {
			h.refreshWhatsApp(ctx, w)
		}
	}
}

// --- the dashboard ------------------------------------------------------------------

// waGate answers unless the plan includes the store's own number and
// Embedded Signup is on.
func (h *Handler) waGate(c *gin.Context) (accountID, userID string, ok bool) {
	claims, okc := currentClaims(c)
	if !okc {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return "", "", false
	}
	if !h.Cfg.WhatsAppEmbeddedSignup {
		respondError(c, http.StatusNotFound, "not found")
		return "", "", false
	}
	caps, err := h.accountCaps(c.Request.Context(), claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return "", "", false
	}
	if !caps.OwnNumber {
		c.JSON(http.StatusForbidden, gin.H{"error": "plan", "message": "Tu propio WhatsApp viene desde el plan Plataforma."})
		return "", "", false
	}
	return claims.AccountID, claims.Sub, true
}

// waStatusView is what the dashboard shows of the store's number.
func (h *Handler) waStatusView(ctx context.Context, accountID string) gin.H {
	out := gin.H{"enabled": h.signupOn(), "connected": false, "payment_link": whatsapp.PaymentLink}
	if j, err := db.WhatsAppJob(ctx, h.Accounts, accountID); err == nil {
		out["job"] = gin.H{"status": j.Status, "detail": j.Detail, "progress": j.Progress}
	}
	w, err := db.WhatsAppAccountFor(ctx, h.Accounts, accountID)
	if err != nil {
		return out
	}
	tpls, _ := db.WhatsAppTemplates(ctx, h.Accounts, w.ID)
	out["connected"] = true
	out["number"] = gin.H{
		"display_phone_number": w.DisplayPhoneNumber, "verified_name": w.VerifiedName, "name_status": w.NameStatus,
		"name_approved":  w.NameStatus == "APPROVED" || w.NameStatus == "AVAILABLE_WITHOUT_REVIEW",
		"quality_rating": w.QualityRating, "coexistence": w.Coexistence, "status": w.Status, "status_detail": w.StatusDetail,
		"needs_payment": w.Status == db.WANoPayment, "connected_at": w.ConnectedAt, "checked_at": w.CheckedAt,
	}
	out["templates"] = tpls
	return out
}

// WhatsAppBusiness handles GET /whatsapp/business: the store's number.
func (h *Handler) WhatsAppBusiness(c *gin.Context) {
	accountID, _, ok := h.waGate(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.waStatusView(c.Request.Context(), accountID))
}

// WhatsAppSignupConfig handles GET /whatsapp/business/signup-config: what
// the dashboard needs to open Embedded Signup (no secrets).
func (h *Handler) WhatsAppSignupConfig(c *gin.Context) {
	if _, _, ok := h.waGate(c); !ok {
		return
	}
	if !h.signupOn() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "signup_off", "message": "La conexión de WhatsApp todavía no está activa."})
		return
	}
	c.JSON(http.StatusOK, gin.H{"app_id": h.Cfg.MetaAppID, "config_id": h.Cfg.MetaWAESConfigID,
		"graph_version": h.WAApp.Version, "es_version": "v5"})
}

// waConnectRequest is what the dashboard sends after Embedded Signup: the
// code and the session event's ids (only hints; the token decides).
type waConnectRequest struct {
	Code              string   `json:"code"`
	Event             string   `json:"event"`
	PhoneNumberID     string   `json:"phone_number_id"`
	WABAID            string   `json:"waba_id"`
	MessagingAccounts []string `json:"messaging_account_ids"`
	BusinessID        string   `json:"business_id"`
	IsOnBizApp        *bool    `json:"is_on_biz_app"`
}

// errStep is a connection step that failed, with what the store reads.
type errStep struct {
	msg string
	err error
}

func (e errStep) Error() string { return e.msg + ": " + e.err.Error() }

// WhatsAppConnect handles POST /whatsapp/business/connect (admin+).
func (h *Handler) WhatsAppConnect(c *gin.Context) {
	accountID, userID, ok := h.waGate(c)
	if !ok {
		return
	}
	if !h.signupOn() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "signup_off", "message": "La conexión de WhatsApp todavía no está activa."})
		return
	}
	var req waConnectRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		respondError(c, http.StatusBadRequest, "falta el código de Meta")
		return
	}
	// The code lives 5 minutes: the work runs here, not in the background.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Minute)
	defer cancel()
	job := h.waJob(ctx, accountID)
	progress := func(p int, detail string) {
		if job != "" {
			_ = db.SetJobProgress(ctx, h.Accounts, job, p, detail)
		}
	}
	finish := func(status, detail string) {
		if job != "" {
			_ = db.UpdateProvisioningJob(ctx, h.Accounts, job, status, detail, nil, true)
		}
	}
	if job != "" {
		_ = db.UpdateProvisioningJob(ctx, h.Accounts, job, provision.StatusRunning, "Conectando tu WhatsApp…", nil, false)
	}
	w, err := h.connectWhatsApp(ctx, accountID, userID, req, progress)
	if err != nil {
		var es errStep
		msg := "No pudimos conectar tu WhatsApp. Inténtalo otra vez."
		if errors.As(err, &es) {
			msg = es.msg
		}
		log.Printf("whatsapp: connect account %s: %v", accountID, err)
		finish(provision.StatusWaitingClient, msg+" "+provision.WhatsAppConnectDetail)
		c.JSON(http.StatusBadGateway, gin.H{"error": "connect_failed", "message": msg})
		return
	}
	finish(provision.StatusDone, "Tu WhatsApp quedó conectado: "+w.VerifiedName+" ("+w.DisplayPhoneNumber+").")
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), accountID, &userID, "whatsapp_business_connected", "WhatsApp del negocio conectado")
	c.JSON(http.StatusOK, h.waStatusView(ctx, accountID))
}

// waJob is the account's whatsapp job (created when it has none, so the
// progress shows in "Tu alta").
func (h *Handler) waJob(ctx context.Context, accountID string) string {
	if j, err := db.WhatsAppJob(ctx, h.Accounts, accountID); err == nil {
		return j.ID
	}
	j, err := db.CreateProvisioningJob(ctx, h.Accounts, db.ProvisioningJob{ID: uuid.NewString(), AccountID: accountID,
		Kind: "whatsapp", Status: provision.StatusWaitingClient, Detail: provision.WhatsAppConnectDetail})
	if err != nil {
		return ""
	}
	return j.ID
}

// connectWhatsApp runs the server side of Embedded Signup.
func (h *Handler) connectWhatsApp(ctx context.Context, accountID, userID string, req waConnectRequest, progress func(int, string)) (db.WhatsAppAccount, error) {
	progress(10, "Recibimos la autorización de Meta…")
	token, err := h.WAApp.ExchangeCode(ctx, req.Code)
	if err != nil {
		return db.WhatsAppAccount{}, errStep{"Meta no aceptó la autorización (vence en 5 minutos). Vuelve a conectar.", err}
	}
	grants, err := h.WAApp.DebugToken(ctx, token)
	if err != nil {
		return db.WhatsAppAccount{}, errStep{"No pudimos leer qué compartiste con AndRho.", err}
	}
	if !grants.Messaging {
		return db.WhatsAppAccount{}, errStep{"Falta el permiso para enviar mensajes. Vuelve a conectar y acepta todos los permisos.", errors.New("no whatsapp_business_messaging")}
	}
	hints := append([]string{req.WABAID}, req.MessagingAccounts...)
	waba, err := whatsapp.PickWABA(grants, hints...)
	if err != nil {
		return db.WhatsAppAccount{}, errStep{"Elige una sola cuenta de WhatsApp Business al conectar.", err}
	}
	cl := whatsapp.ForAccount(token, "", waba, h.Cfg.MetaGraphVersion)
	if h.WA != nil && h.WA.Graph != "" {
		cl.Graph = h.WA.Graph
	}
	nums, err := cl.PhoneNumbers(ctx)
	if err != nil {
		return db.WhatsAppAccount{}, errStep{"No pudimos leer el número de tu cuenta de WhatsApp.", err}
	}
	num, err := whatsapp.PickPhone(nums, req.PhoneNumberID)
	if err != nil {
		return db.WhatsAppAccount{}, errStep{"No encontramos el número que elegiste en tu cuenta de WhatsApp.", err}
	}
	cl.PhoneNumberID = num.ID
	if w, err := db.WhatsAppAccountByPhone(ctx, h.Accounts, num.ID); err == nil && w.AccountID != accountID {
		return db.WhatsAppAccount{}, errStep{"Ese número ya está conectado a otra cuenta de AndRho.", errors.New("number in use")}
	}

	progress(30, "Conectando tu cuenta de WhatsApp con AndRho…")
	if err := cl.SubscribeApp(ctx); err != nil {
		return db.WhatsAppAccount{}, errStep{"No pudimos conectar tu cuenta de WhatsApp con AndRho.", err}
	}
	coexist := num.IsOnBizApp || req.Event == "FINISH_WHATSAPP_BUSINESS_APP_ONBOARDING" || (req.IsOnBizApp != nil && *req.IsOnBizApp)
	var pinSealed []byte
	var synced *time.Time
	if coexist {
		// The number stays on the store's phone. Meta wants both syncs
		// asked within 24 h; AndRho discards what they bring.
		progress(50, "Tu número sigue en la app de WhatsApp Business de tu celular…")
		for _, kind := range []string{"smb_app_state_sync", "history"} {
			if err := cl.SyncSMBApp(ctx, kind); err != nil {
				log.Printf("whatsapp: sync %s of %s: %v", kind, num.ID, err)
			}
		}
		now := time.Now()
		synced = &now
	} else {
		progress(50, "Registrando tu número…")
		pin := whatsapp.NewPIN()
		if err := cl.Register(ctx, pin); err != nil {
			return db.WhatsAppAccount{}, errStep{"Meta no registró tu número. Revisa que ya no esté en otra app de WhatsApp.", err}
		}
		if pinSealed, err = h.Secrets.Seal(pin); err != nil {
			return db.WhatsAppAccount{}, err
		}
	}

	progress(70, "Leyendo el nombre y la calidad de tu número…")
	info, err := cl.PhoneInfo(ctx)
	if err != nil {
		info = num
	}
	sealed, err := h.Secrets.Seal(token)
	if err != nil {
		return db.WhatsAppAccount{}, err
	}
	w := db.WhatsAppAccount{ID: uuid.NewString(), AccountID: accountID, WABAID: waba, PhoneNumberID: num.ID, BusinessID: req.BusinessID,
		DisplayPhoneNumber: info.DisplayPhoneNumber, VerifiedName: info.VerifiedName, NameStatus: info.NameStatus,
		QualityRating: info.QualityRating, Coexistence: coexist, Status: db.WAActive, Token: sealed, PIN: pinSealed, SyncedAt: synced}
	if funded, known := cl.Funded(ctx); known && !funded {
		w.Status, w.StatusDetail = db.WANoPayment, "Falta el método de pago en WhatsApp Manager."
	}
	if err := db.SaveWhatsAppAccount(ctx, h.Accounts, w, userID); err != nil {
		return w, err
	}

	progress(90, "Creando tus mensajes de confirmación…")
	if l, ok := h.lineFor(w); ok {
		h.ensureTemplates(ctx, l)
	}
	return w, nil
}

// WhatsAppRefresh handles POST /whatsapp/business/refresh (admin+).
func (h *Handler) WhatsAppRefresh(c *gin.Context) {
	accountID, _, ok := h.waGate(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if w, err := db.WhatsAppAccountFor(ctx, h.Accounts, accountID); err == nil {
		h.refreshWhatsApp(ctx, w)
	}
	c.JSON(http.StatusOK, h.waStatusView(ctx, accountID))
}

// WhatsAppDisconnect handles DELETE /whatsapp/business (admin+): AndRho
// stops getting the number's messages and forgets its token. The number
// isn't deregistered: it stays the store's (and on its phone with
// Coexistence).
func (h *Handler) WhatsAppDisconnect(c *gin.Context) {
	accountID, userID, ok := h.waGate(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	w, err := db.WhatsAppAccountFor(ctx, h.Accounts, accountID)
	if errors.Is(err, db.ErrNotFound) {
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if l, ok := h.lineFor(w); ok {
		if err := l.wa.UnsubscribeApp(ctx); err != nil {
			log.Printf("whatsapp: unsubscribe %s: %v", w.WABAID, err)
		}
	}
	if err := db.DisconnectWhatsApp(ctx, h.Accounts, w.ID, "Desconectado desde el panel."); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if j, err := db.WhatsAppJob(ctx, h.Accounts, accountID); err == nil {
		_ = db.UpdateProvisioningJob(ctx, h.Accounts, j.ID, provision.StatusWaitingClient, provision.WhatsAppConnectDetail, nil, false)
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), accountID, &userID, "whatsapp_business_disconnected",
		fmt.Sprintf("WhatsApp del negocio desconectado (%s)", maskPhone(whatsapp.Digits(w.DisplayPhoneNumber))))
	c.Status(http.StatusNoContent)
}
