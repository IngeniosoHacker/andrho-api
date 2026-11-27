package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/live"
	"github.com/IngeniosoHacker/andrho-api/internal/meta"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// CRM from the lives (audit 2026-11-19, andrho/docs/auditoria-…md):
//
//   - a comment that matches an item makes its author a customer in the ERP
//     (tag "Cliente", linked to the line and the live: andrho_reserve);
//   - comments without a match, messages to the Page and (if the seller
//     turns them on) reactions make prospects, with only the name and ids
//     Meta gives. People who only watch aren't in Meta's API;
//   - the confirmation carries a "Chatear por WhatsApp" button (wa.me with
//     "Pedido AR-XXXX" typed) to the store's own number (whatsapp_business.go;
//     no button without one). When that message reaches it, the number
//     joins the same contact and order; it's kept only because the buyer
//     wrote it;
//   - right before closing, AndRho asks which channel they prefer and
//     writes there next time, inside Meta's windows or with an approved
//     template;
//   - the first message carries a short notice about their data; the date
//     and channel they went on are kept on the contact.
//
// The conversation after the private reply is the same on Messenger and on
// WhatsApp: buyerChat hides which one.

// --- the ERP ---------------------------------------------------------------------

type livePerson struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`
	Source           string  `json:"source"`
	PreferredChannel string  `json:"preferred_channel"`
	HasWhatsApp      bool    `json:"has_whatsapp"`
	HasMessenger     bool    `json:"has_messenger"`
	ConsentAt        string  `json:"consent_at"`
	ConsentChannel   string  `json:"consent_channel"`
	PurchaseCount    int     `json:"purchase_count"`
	TotalSpent       float64 `json:"total_spent"`
	FirstSeen        string  `json:"first_seen"`
	ConvertedAt      string  `json:"converted_at"`
	Lives            int     `json:"lives"`
	Items            []struct {
		LiveName string  `json:"live_name"`
		State    string  `json:"state"`
		Price    float64 `json:"price"`
	} `json:"items,omitempty"`
}

// prospect is someone who interacted without taking an item.
type prospect struct {
	CommenterID string `json:"commenter_id,omitempty"`
	PSID        string `json:"psid,omitempty"`
	Name        string `json:"name"`
	Source      string `json:"source"` // comment_other | message | reaction
}

func erpTouchProspects(ctx context.Context, cl *odoo.Client, pageID string, odooLiveID int, people []prospect) (int, error) {
	var live any = false
	if odooLiveID > 0 {
		live = odooLiveID
	}
	var n int
	err := cl.Exec(ctx, buyerModel, "andrho_touch_prospects", []any{pageID, live, people}, nil, &n)
	return n, err
}

// buyerReach is how to reach a contact, as the ERP has it.
type buyerReach struct {
	Preferred string
	WhatsApp  string
	PSID      string
}

func erpBuyerReach(ctx context.Context, cl *odoo.Client, buyerID int) (buyerReach, error) {
	var rows []map[string]any
	if err := cl.SearchRead(ctx, buyerModel, []any{[]any{"id", "=", buyerID}}, []string{"preferred_channel", "wa_phone", "psid"}, "", 1, &rows); err != nil {
		return buyerReach{}, err
	}
	if len(rows) == 0 {
		return buyerReach{}, db.ErrNotFound
	}
	r := rows[0]
	return buyerReach{Preferred: odoo.Str(r["preferred_channel"]), WhatsApp: odoo.Str(r["wa_phone"]), PSID: odoo.Str(r["psid"])}, nil
}

func erpSetReach(ctx context.Context, cl *odoo.Client, buyerID int, vals map[string]string) error {
	return cl.Exec(ctx, buyerModel, "andrho_set_reach", []any{[]int{buyerID}, vals}, nil, nil)
}

func erpLinkPSID(ctx context.Context, cl *odoo.Client, buyerID int, psid string) error {
	return cl.Exec(ctx, buyerModel, "andrho_link_psid", []any{[]int{buyerID}, psid}, nil, nil)
}

// liveModulesChecked: accounts whose ERP modules were brought up to date
// in this process (andrho_core and andrho_live_sale ship new fields).
var liveModulesChecked sync.Map

// upgradeLiveModules updates the ERP's AndRho modules once per account and
// process when the image carries a newer version. In the background: it
// takes minutes.
func (h *Handler) upgradeLiveModules(accountID string) {
	if _, done := liveModulesChecked.LoadOrStore(accountID, true); done {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cl, err := h.odooFor(ctx, accountID)
		if err != nil || !cl.HasModel(ctx, liveModel) {
			liveModulesChecked.Delete(accountID)
			return
		}
		up, err := cl.UpgradeStale(ctx, []string{"andrho_core", liveModule})
		if err != nil {
			log.Printf("live: upgrade erp modules of %s: %v", accountID, err)
			liveModulesChecked.Delete(accountID)
			return
		}
		if len(up) > 0 {
			log.Printf("live: upgraded %v in %s's ERP", up, accountID)
		}
	}()
}

// --- prospects -------------------------------------------------------------------

// touchProspects records people who interacted without taking an item.
// Best effort: a failure only loses prospects, never sales.
func (h *Handler) touchProspects(ctx context.Context, cl *odoo.Client, accountID, pageID string, odooLiveID int, people []prospect) {
	if len(people) == 0 || !h.featureOn(ctx, accountID, featureLiveCRM) {
		return
	}
	if _, err := erpTouchProspects(ctx, cl, pageID, odooLiveID, people); err != nil {
		log.Printf("live: prospects of %s: %v", accountID, err)
	}
}

// seenMessengers: people who wrote to a Page and are already prospects (in
// this process), so each message doesn't call the ERP.
var seenMessengers sync.Map

// pageMessageProspect makes a prospect of someone who writes to the Page
// outside a purchase conversation.
func (h *Handler) pageMessageProspect(ctx context.Context, page pageCtx, psid string) {
	key := page.PageID + "|" + psid
	if _, seen := seenMessengers.LoadOrStore(key, true); seen {
		return
	}
	if !h.featureOn(ctx, page.AccountID, featureLiveCRM) {
		seenMessengers.Delete(key)
		return
	}
	cl, err := h.odooFor(ctx, page.AccountID)
	if err != nil || !cl.HasModel(ctx, liveModel) {
		seenMessengers.Delete(key)
		return
	}
	odooLive := 0
	if active, err := db.ActiveLiveSessions(ctx, h.Accounts); err == nil {
		for _, s := range active {
			if s.AccountID == page.AccountID {
				odooLive = s.OdooLiveID
			}
		}
	}
	name := h.Meta.ProfileName(ctx, psid, page.token)
	h.touchProspects(ctx, cl, page.AccountID, page.PageID, odooLive, []prospect{{PSID: psid, Name: name, Source: "message"}})
}

// liveReactions turns a live's reactions into prospects when the seller
// asked for it (andrho.live.capture_reactions).
func (h *Handler) liveReactions(ctx context.Context, sess db.LiveSession, page pageCtx, seen map[string]bool) {
	if !h.featureOn(ctx, sess.AccountID, featureLiveCRM) {
		return
	}
	cl, err := h.odooFor(ctx, sess.AccountID)
	if err != nil {
		return
	}
	if v, _ := erpGetParam(ctx, cl, liveParams["capture_reactions"]); v != "1" {
		return
	}
	rs, err := h.Meta.LiveReactions(ctx, sess.PlatformLiveID, page.token)
	if err != nil {
		return
	}
	people := []prospect{}
	for _, r := range rs {
		if r.ID == "" || r.ID == page.PageID || seen["r:"+r.ID] {
			continue
		}
		seen["r:"+r.ID] = true
		people = append(people, prospect{CommenterID: r.ID, Name: r.Name, Source: "reaction"})
	}
	h.touchProspects(ctx, cl, sess.AccountID, page.PageID, sess.OdooLiveID, people)
}

// --- the order code and the WhatsApp button --------------------------------------

// linkCodeTTL: the code works as long as the private reply can be answered
// and a little more.
const linkCodeTTL = 8 * 24 * time.Hour

// orderCode gives a claim its "AR-XXXX" (once), valid on the store number
// biz.
func (h *Handler) orderCode(ctx context.Context, claim db.LiveClaim, biz string) (db.LiveLinkCode, error) {
	for range 5 {
		l, ok, err := db.CreateLiveLinkCode(ctx, h.Accounts, db.LiveLinkCode{Code: "AR-" + newCode(4), AccountID: claim.AccountID,
			ClaimID: claim.ID, PageID: claim.PageID, BuyerID: claim.BuyerID, ExpiresAt: time.Now().Add(linkCodeTTL), Biz: biz})
		if err != nil {
			return l, err
		}
		if ok {
			return l, nil
		}
	}
	return db.LiveLinkCode{}, errors.New("live: no free order code")
}

// whatsappButton is the "Chatear por WhatsApp" link to the store's
// number for a claim, or nil when the account doesn't use it or has no
// number of its own.
func (h *Handler) whatsappButton(ctx context.Context, claim db.LiveClaim) *meta.Button {
	if !h.featureOn(ctx, claim.AccountID, featureLiveWhatsApp) {
		return nil
	}
	line, ok := h.buyerLine(ctx, claim.AccountID)
	if !ok {
		return nil
	}
	number := line.number(ctx)
	if number == "" {
		return nil
	}
	l, err := h.orderCode(ctx, claim, line.biz)
	if err != nil {
		log.Printf("live: order code for claim %d: %v", claim.ID, err)
		return nil
	}
	if l.Biz != line.biz {
		return nil // the code was made for a number the store no longer uses
	}
	return &meta.Button{Title: "Chatear por WhatsApp", URL: whatsapp.ChatLink(number, live.OrderText(l.Code))}
}

// --- one conversation, two channels ---------------------------------------------------

type choice struct{ Title, Payload string }

// buyerChat is the conversation with a buyer after the private reply.
type buyerChat interface {
	channel() string // messenger | whatsapp
	say(ctx context.Context, text string)
	ask(ctx context.Context, text string, opts []choice)
	link(ctx context.Context, text, title, url string)
	thread(ctx context.Context) (db.LiveThread, error)
	saveThread(ctx context.Context, t db.LiveThread)
	psid() string
}

type messengerChat struct {
	h    *Handler
	page pageCtx
	id   string
}

func (m messengerChat) channel() string { return "messenger" }
func (m messengerChat) psid() string    { return m.id }
func (m messengerChat) say(ctx context.Context, text string) {
	if err := m.h.Meta.SendText(ctx, m.page.PageID, m.page.token, m.id, text); err != nil {
		log.Printf("live: message %s: %v", m.id, err)
	}
}
func (m messengerChat) ask(ctx context.Context, text string, opts []choice) {
	bs := make([]meta.Button, 0, len(opts))
	for _, o := range opts {
		bs = append(bs, meta.Button{Title: o.Title, Payload: o.Payload})
	}
	if err := m.h.Meta.SendButtons(ctx, m.page.PageID, m.page.token, m.id, text, bs); err != nil {
		log.Printf("live: message %s: %v", m.id, err)
	}
}
func (m messengerChat) link(ctx context.Context, text, title, url string) {
	if err := m.h.Meta.SendButtons(ctx, m.page.PageID, m.page.token, m.id, text, []meta.Button{{Title: title, URL: url}}); err != nil {
		log.Printf("live: message %s: %v", m.id, err)
	}
}
func (m messengerChat) thread(ctx context.Context) (db.LiveThread, error) {
	return db.GetLiveThread(ctx, m.h.Accounts, m.page.PageID, m.id)
}
func (m messengerChat) saveThread(ctx context.Context, t db.LiveThread) {
	t.PageID, t.PSID = m.page.PageID, m.id
	_ = db.SaveLiveThread(ctx, m.h.Accounts, t)
}

// waChat is a buyer's conversation on a store's number (line).
type waChat struct {
	h     *Handler
	line  waLine
	phone string
}

const waLiveKind = "live"

func (w waChat) channel() string                      { return "whatsapp" }
func (w waChat) psid() string                         { return "" }
func (w waChat) say(ctx context.Context, text string) { w.line.say(ctx, w.h, w.phone, text) }
func (w waChat) ask(ctx context.Context, text string, opts []choice) {
	bs := make([]whatsapp.Button, 0, len(opts))
	for _, o := range opts {
		bs = append(bs, whatsapp.Button{ID: o.Payload, Title: o.Title})
	}
	w.line.ask(ctx, w.h, w.phone, text, bs)
}
func (w waChat) link(ctx context.Context, text, title, url string) { w.say(ctx, text+" "+url) }
func (w waChat) thread(ctx context.Context) (db.LiveThread, error) {
	s, err := db.GetWASession(ctx, w.h.Accounts, w.line.biz, w.phone)
	if err != nil || s.Kind != waLiveKind {
		return db.LiveThread{}, db.ErrNotFound
	}
	var t db.LiveThread
	err = json.Unmarshal(s.Data, &t)
	return t, err
}
func (w waChat) saveThread(ctx context.Context, t db.LiveThread) {
	if t.Step == "" {
		_ = db.DeleteWASession(ctx, w.h.Accounts, w.line.biz, w.phone)
		return
	}
	b, _ := json.Marshal(t)
	_ = db.SaveWASession(ctx, w.h.Accounts, db.WASession{Biz: w.line.biz, Phone: w.phone, AccountID: t.AccountID, Kind: waLiveKind, Data: b,
		ExpiresAt: time.Now().Add(whatsapp.Window)})
}

func channelChoices(claimID int64) []choice {
	id := strconv.FormatInt(claimID, 10)
	return []choice{{Title: "WhatsApp", Payload: live.PayloadChannelWA + id}, {Title: "Messenger", Payload: live.PayloadChannelMS + id}}
}

func claimChoices(claimID int64) []choice {
	id := strconv.FormatInt(claimID, 10)
	return []choice{{Title: "Sí, lo quiero", Payload: live.PayloadYes + id}, {Title: "No", Payload: live.PayloadNo + id}}
}

// payloadClaim reads "<prefix><claim id>".
func payloadClaim(payload string, prefixes ...string) (string, int64, bool) {
	for _, p := range prefixes {
		if rest, ok := strings.CutPrefix(payload, p); ok {
			id, err := strconv.ParseInt(rest, 10, 64)
			return p, id, err == nil
		}
	}
	return "", 0, false
}

// wantsChannelQuestion: ask only where the WhatsApp option exists and the
// contact hasn't chosen yet.
func (h *Handler) wantsChannelQuestion(ctx context.Context, cl *odoo.Client, claim db.LiveClaim) bool {
	if claim.BuyerID == 0 || !h.featureOn(ctx, claim.AccountID, featureLiveWhatsApp) {
		return false
	}
	r, err := erpBuyerReach(ctx, cl, claim.BuyerID)
	return err == nil && r.Preferred == ""
}

// closeConfirmation is the end of the conversation: the channel question
// first (if it applies), then the thanks with the privacy notice.
func (h *Handler) closeConfirmation(ctx context.Context, chat buyerChat, cl *odoo.Client, claim db.LiveClaim, t db.LiveThread) {
	if h.wantsChannelQuestion(ctx, cl, claim) {
		t.Step, t.ClaimID = "channel", claim.ID
		chat.saveThread(ctx, t)
		chat.ask(ctx, live.AskChannel, channelChoices(claim.ID))
		return
	}
	t.Step = ""
	chat.saveThread(ctx, t)
	privacy, _ := erpGetParam(ctx, cl, liveParams["privacy_url"])
	chat.say(ctx, live.DoneWith(privacy))
}

// answerChannel saves the buyer's preferred channel.
func (h *Handler) answerChannel(ctx context.Context, chat buyerChat, claim db.LiveClaim, wantWhatsApp bool) {
	cl, err := h.odooFor(ctx, claim.AccountID)
	if err != nil || claim.BuyerID == 0 {
		return
	}
	pref := map[bool]string{true: "whatsapp", false: "messenger"}[wantWhatsApp]
	if err := erpSetReach(ctx, cl, claim.BuyerID, map[string]string{"preferred_channel": pref}); err != nil {
		log.Printf("live: preferred channel of buyer %d: %v", claim.BuyerID, err)
	}
	t, _ := chat.thread(ctx)
	t.Step = ""
	t.AccountID = claim.AccountID
	chat.saveThread(ctx, t)
	privacy, _ := erpGetParam(ctx, cl, liveParams["privacy_url"])
	switch {
	case !wantWhatsApp:
		chat.say(ctx, live.ChannelSavedMessenger+" "+live.DoneWith(privacy))
	case chat.channel() == "whatsapp":
		chat.say(ctx, live.ChannelSavedWhatsApp+" "+live.DoneWith(privacy))
	default:
		r, _ := erpBuyerReach(ctx, cl, claim.BuyerID)
		if r.WhatsApp != "" {
			chat.say(ctx, live.ChannelSavedWhatsApp+" "+live.DoneWith(privacy))
			return
		}
		if b := h.whatsappButton(ctx, claim); b != nil {
			chat.link(ctx, live.ChannelAskWhatsApp, b.Title, b.URL)
		}
		chat.say(ctx, live.DoneWith(privacy))
	}
}

// --- the store's WhatsApp: buyers ------------------------------------------------------

// lineServes reports whether line may speak for accountID's orders: the
// store's own number only for its account; AndRho's (test) for any.
func (l waLine) serves(accountID string) bool {
	return !l.own() || l.acc.AccountID == accountID
}

// waLive takes a buyer's WhatsApp on a store's number (line): the order
// code, their buttons and the shipping details they type.
func (h *Handler) waLive(ctx context.Context, line waLine, m whatsapp.Message) bool {
	if prefix, id, ok := payloadClaim(m.ButtonID, live.PayloadYes, live.PayloadNo, live.PayloadChannelWA, live.PayloadChannelMS); ok {
		claim, err := db.GetLiveClaim(ctx, h.Accounts, id)
		if err != nil || !line.serves(claim.AccountID) {
			return true
		}
		// Only the number that linked this order, on the store's number,
		// can answer for it.
		if l, err := db.LiveLinkCodeForClaim(ctx, h.Accounts, id); err != nil || l.WAPhone != m.From || l.Biz != line.biz {
			return true
		}
		chat := waChat{h: h, line: line, phone: m.From}
		switch prefix {
		case live.PayloadChannelWA, live.PayloadChannelMS:
			h.answerChannel(ctx, chat, claim, prefix == live.PayloadChannelWA)
		default:
			h.answerClaim(ctx, chat, claim, prefix == live.PayloadYes)
		}
		return true
	}
	if code := live.OrderCode(m.Text); code != "" {
		h.waOrderCode(ctx, line, m, code)
		return true
	}
	chat := waChat{h: h, line: line, phone: m.From}
	if t, err := chat.thread(ctx); err == nil && t.Step != "" {
		h.shippingText(ctx, chat, t, m.Text)
		return true
	}
	return false
}

// waOrderCode joins the WhatsApp number to the order's contact. The code
// only works on the store number it was made for.
func (h *Handler) waOrderCode(ctx context.Context, line waLine, m whatsapp.Message, code string) {
	say := func(text string) { line.say(ctx, h, m.From, text) }
	guessKey := line.biz + "|" + m.From
	if waGuesses.blocked(guessKey, time.Now()) {
		say(waTooManyCodes)
		return
	}
	l, err := db.GetLiveLinkCode(ctx, h.Accounts, code)
	if err != nil || l.Biz != line.biz || !line.serves(l.AccountID) {
		waGuesses.miss(guessKey, time.Now())
		say(live.WhatsAppLinkExpired)
		return
	}
	claim, err := db.GetLiveClaim(ctx, h.Accounts, l.ClaimID)
	if err != nil {
		say(live.WhatsAppLinkExpired)
		return
	}
	if l.WAPhone != "" && l.WAPhone != m.From {
		// Someone else already linked this order: a forwarded code.
		waGuesses.miss(guessKey, time.Now())
		say(live.WhatsAppLinkExpired)
		return
	}
	if err := db.UseLiveLinkCode(ctx, h.Accounts, code, line.biz, m.From); err != nil {
		return
	}
	cl, err := h.odooFor(ctx, claim.AccountID)
	if err == nil && claim.BuyerID != 0 {
		if err := erpSetReach(ctx, cl, claim.BuyerID, map[string]string{"whatsapp": m.From, "consent_channel": "whatsapp"}); err != nil {
			log.Printf("live: whatsapp of buyer %d: %v", claim.BuyerID, err)
		}
	}
	chat := waChat{h: h, line: line, phone: m.From}
	switch claim.State {
	case "asked":
		item, err := erpLine(ctx, cl, claim.OdooLineID)
		if err != nil {
			say(live.WhatsAppLinked)
			return
		}
		chat.ask(ctx, live.WhatsAppLinked+" "+live.ConfirmWithConsent(item.LiveName, item.Price), claimChoices(claim.ID))
	case "confirmed":
		chat.say(ctx, live.WhatsAppLinked)
		if cl != nil && h.wantsChannelQuestion(ctx, cl, claim) {
			chat.saveThread(ctx, db.LiveThread{AccountID: claim.AccountID, Step: "channel", ClaimID: claim.ID})
			chat.ask(ctx, live.AskChannel, channelChoices(claim.ID))
		}
	default:
		say(live.WhatsAppLinkExpired)
	}
}

// --- the preferred channel, next time ---------------------------------------------------

// notifyPreferred also asks a returning buyer who prefers WhatsApp there,
// from the store's number: inside the 24 h window as a message with
// buttons; outside, only with the store's approved template
// (andrho_confirmar_apartado; in the test mode on AndRho's number,
// WHATSAPP_TEMPLATE_LIVE_CONFIRM). Without the store's number nothing goes
// by WhatsApp. Messenger's private reply always goes too: it's what
// answers their comment.
func (h *Handler) notifyPreferred(ctx context.Context, cl *odoo.Client, claim db.LiveClaim, item liveLine) {
	if claim.BuyerID == 0 || !h.featureOn(ctx, claim.AccountID, featureLiveWhatsApp) {
		return
	}
	line, ok := h.buyerLine(ctx, claim.AccountID)
	if !ok {
		return
	}
	r, err := erpBuyerReach(ctx, cl, claim.BuyerID)
	if err != nil || r.Preferred != "whatsapp" || r.WhatsApp == "" {
		return
	}
	l, err := h.orderCode(ctx, claim, line.biz)
	if err != nil || l.Biz != line.biz {
		return // the code belongs to a number the store no longer uses
	}
	_ = db.UseLiveLinkCode(ctx, h.Accounts, l.Code, line.biz, r.WhatsApp)
	last, _ := db.WALastInbound(ctx, h.Accounts, line.biz, r.WhatsApp)
	if whatsapp.InWindow(last, time.Now()) {
		waChat{h: h, line: line, phone: r.WhatsApp}.ask(ctx, live.ConfirmWithConsent(item.LiveName, item.Price), claimChoices(claim.ID))
		return
	}
	params := []string{item.LiveName, live.Money(item.Price)}
	if line.own() {
		if !db.WhatsAppTemplateApproved(ctx, h.Accounts, line.acc.ID, tplLiveConfirm, tplLang) {
			return // not approved yet: Messenger's private reply is enough
		}
		id := strconv.FormatInt(claim.ID, 10)
		if _, err := line.wa.SendTemplateButtons(ctx, r.WhatsApp, tplLiveConfirm, tplLang, params,
			[]string{live.PayloadYes + id, live.PayloadNo + id}); err != nil {
			h.sendFailed(line, r.WhatsApp, err)
		}
		return
	}
	if h.Cfg.WhatsAppTemplateLiveConfirm == "" {
		return
	}
	if _, err := line.wa.SendTemplate(ctx, r.WhatsApp, h.Cfg.WhatsAppTemplateLiveConfirm, h.Cfg.WhatsAppTemplateLang, params); err != nil {
		log.Printf("live: template to buyer %d: %v", claim.BuyerID, err)
	}
}

// --- the dashboard ---------------------------------------------------------------------

// LivePeople handles GET /live/sessions/:id/people: the live's customers
// and prospects.
func (h *Handler) LivePeople(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	sess, err := db.GetLiveSession(ctx, h.Accounts, accountID, c.Param("id"))
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
	var people []livePerson
	if err := cl.Exec(ctx, buyerModel, "andrho_people", []any{sess.OdooLiveID}, nil, &people); err != nil {
		h.upgradeLiveModules(accountID)
		h.liveFail(c, "people", err)
		return
	}
	customers, prospects := 0, 0
	for _, p := range people {
		if p.Kind == "customer" {
			customers++
		} else {
			prospects++
		}
	}
	c.JSON(http.StatusOK, gin.H{"people": people, "customers": customers, "prospects": prospects})
}

// LiveContacts handles GET /live/contacts?kind=customer|prospect: every
// live contact with the totals (preferred channel, conversion).
func (h *Handler) LiveContacts(c *gin.Context) {
	accountID, _, ok := h.liveGate(c)
	if !ok {
		return
	}
	cl, ok := h.liveERP(c, accountID)
	if !ok {
		return
	}
	var kind any = false
	if k := c.Query("kind"); k == "customer" || k == "prospect" {
		kind = k
	}
	var out struct {
		Contacts   []livePerson   `json:"contacts"`
		Customers  int            `json:"customers"`
		Prospects  int            `json:"prospects"`
		Converted  int            `json:"converted"`
		Conversion float64        `json:"conversion"`
		Channels   map[string]int `json:"channels"`
	}
	if err := cl.Exec(c.Request.Context(), buyerModel, "andrho_contacts", []any{kind, 500}, nil, &out); err != nil {
		h.upgradeLiveModules(accountID)
		h.liveFail(c, "contacts", err)
		return
	}
	if out.Contacts == nil {
		out.Contacts = []livePerson{}
	}
	c.JSON(http.StatusOK, out)
}
