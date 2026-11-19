package handlers

import (
	"context"
	"errors"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/live"
	"github.com/IngeniosoHacker/andrho-api/internal/meta"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

// The live-sale engine (founder/live-sale-spec.md §3):
//
//	comment matches an item ─▶ claim (queued)
//	   item free?  ─▶ reserve in the ERP ─▶ private reply "¿Confirmas…?" [Sí][No] (asked)
//	   "Sí"        ─▶ confirm in the ERP (card + stock) ─▶ ask phone/address ─▶ card ready
//	   "No" / no answer in LIVE_CONFIRM_MINUTES ─▶ release ─▶ next queued commenter
//
// Comments come from polling the live while it's on air (runLive) and from
// Meta's feed webhook for the unsold-item posts; both are idempotent by
// comment id (live_claims.comment_id).

var errNoPage = errors.New("live: the account has no Facebook Page connected")

// pageCtx is a Page with its usable token.
type pageCtx struct {
	db.MetaPage
	token string
}

func (h *Handler) pageFor(ctx context.Context, pageID string) (pageCtx, error) {
	p, err := db.GetMetaPage(ctx, h.Accounts, pageID)
	if err != nil {
		return pageCtx{}, err
	}
	tok, err := h.Secrets.Open(p.Token)
	if err != nil {
		return pageCtx{}, err
	}
	return pageCtx{MetaPage: p, token: tok}, nil
}

func (h *Handler) selectedPage(ctx context.Context, accountID string) (pageCtx, error) {
	p, err := db.SelectedMetaPage(ctx, h.Accounts, accountID)
	if errors.Is(err, db.ErrNotFound) {
		return pageCtx{}, errNoPage
	}
	if err != nil {
		return pageCtx{}, err
	}
	return h.pageFor(ctx, p.PageID)
}

func confirmButtons(claimID int64) []meta.Button {
	id := strconv.FormatInt(claimID, 10)
	return []meta.Button{{Title: "Sí, lo quiero", Payload: live.PayloadYes + id}, {Title: "No", Payload: live.PayloadNo + id}}
}

// itemsFor are the lines a comment can still take (on sale, or reserved
// by someone who may not confirm: then the comment waits in line).
func itemsFor(lines []liveLine) []live.Item {
	out := []live.Item{}
	for _, l := range lines {
		if l.State == "available" || l.State == "reserved" {
			out = append(out, live.Item{ID: l.ID, Name: l.Product, LiveName: l.LiveName, Price: l.Price})
		}
	}
	return out
}

func lineByID(lines []liveLine, id int) (liveLine, bool) {
	for _, l := range lines {
		if l.ID == id {
			return l, true
		}
	}
	return liveLine{}, false
}

// liveComment handles one comment of a live on air. took: the comment took
// or queued for an item (the caller then re-reads lines); matched: it named
// an item (otherwise its author may be a prospect).
func (h *Handler) liveComment(ctx context.Context, cl *odoo.Client, sess db.LiveSession, page pageCtx, lines []liveLine, c meta.Comment) (took, matched bool) {
	if c.From.ID == page.PageID {
		return false, true // the seller's own comments
	}
	item, ok := live.Match(c.Message, itemsFor(lines))
	if !ok {
		return false, false
	}
	at := c.CreatedTime
	if at.IsZero() {
		at = time.Now()
	}
	claim, inserted, err := db.InsertLiveClaim(ctx, h.Accounts, db.LiveClaim{
		AccountID: sess.AccountID, SessionID: sess.ID, PageID: page.PageID, OdooLineID: item.ID, Source: "live",
		CommentID: c.ID, AuthorID: c.From.ID, AuthorName: c.From.Name, CommentText: truncate(c.Message, 300), CommentedAt: at,
	})
	if err != nil {
		log.Printf("live: claim %s: %v", c.ID, err)
		return false, true
	}
	if !inserted {
		return false, true
	}
	if l, ok := lineByID(lines, item.ID); ok && l.State == "available" {
		h.askClaim(ctx, cl, page, claim, l)
	}
	return true, true
}

// postComment handles a comment on an unsold item's post: only "mío".
func (h *Handler) postComment(ctx context.Context, page pageCtx, post db.LivePost, v meta.ChangeValue) {
	if post.State != "open" || v.From.ID == page.PageID {
		return
	}
	cl, err := h.odooFor(ctx, post.AccountID)
	if err != nil {
		log.Printf("live: post %s: erp: %v", post.PostID, err)
		return
	}
	// The seller's word ("mío" unless they picked another one).
	word, _ := erpGetParam(ctx, cl, liveParams["claim_word"])
	if !live.IsClaim(v.Message, word) {
		return
	}
	at := time.Now()
	if v.Created > 0 {
		at = time.Unix(v.Created, 0)
	}
	claim, inserted, err := db.InsertLiveClaim(ctx, h.Accounts, db.LiveClaim{
		AccountID: post.AccountID, SessionID: post.SessionID, PageID: page.PageID, OdooLineID: post.OdooLineID, Source: "post",
		PostID: post.PostID, CommentID: v.CommentID, AuthorID: v.From.ID, AuthorName: v.From.Name, CommentText: truncate(v.Message, 300), CommentedAt: at,
	})
	if err != nil || !inserted {
		return
	}
	line, err := erpLine(ctx, cl, post.OdooLineID)
	if err == nil && line.State == "posted" {
		h.askClaim(ctx, cl, page, claim, line)
	}
}

// askClaim reserves the item for the claim's author and sends the private
// reply with Sí/No. Failing that, the item goes back and to the next one.
func (h *Handler) askClaim(ctx context.Context, cl *odoo.Client, page pageCtx, claim db.LiveClaim, line liveLine) bool {
	name := claim.AuthorName
	if name == "" {
		name = "Comprador"
	}
	author := claim.AuthorID
	if author == "" {
		author = "comment:" + claim.CommentID
	}
	buyerID, err := erpReserve(ctx, cl, line.ID, author, name, page.PageID, claim.CommentID, claim.CommentText)
	if err != nil || buyerID == 0 {
		if err != nil {
			log.Printf("live: reserve line %d: %v", line.ID, err)
		}
		return false // stays queued: someone else has it
	}
	if buyerID > 0 {
		claim.BuyerID = buyerID
		_ = db.SetLiveClaimBuyerID(ctx, h.Accounts, claim.ID, buyerID)
	}
	// The only message Meta guarantees (one per comment): the question, the
	// notice about the data and, where it applies, the WhatsApp button.
	buttons := confirmButtons(claim.ID)
	if b := h.whatsappButton(ctx, claim); b != nil {
		buttons = append(buttons, *b)
	}
	err = h.Meta.PrivateReply(ctx, page.PageID, page.token, claim.CommentID, live.ConfirmWithConsent(line.LiveName, line.Price), buttons)
	if err != nil {
		log.Printf("live: private reply %s: %v", claim.CommentID, err)
		_, _ = db.SetLiveClaimState(ctx, h.Accounts, claim.ID, []string{"queued"}, "failed")
		_ = erpRelease(ctx, cl, line.ID)
		return false
	}
	_, _ = db.SetLiveClaimState(ctx, h.Accounts, claim.ID, []string{"queued"}, "asked")
	h.notifyPreferred(ctx, cl, claim, line)
	return true
}

// advance gives a released item to the next person who commented it.
func (h *Handler) advance(ctx context.Context, cl *odoo.Client, accountID string, lineID int) {
	for range 20 {
		next, err := db.NextQueuedClaim(ctx, h.Accounts, accountID, lineID)
		if err != nil {
			return
		}
		line, err := erpLine(ctx, cl, lineID)
		if err != nil || (line.State != "available" && line.State != "posted") {
			return
		}
		page, err := h.pageFor(ctx, next.PageID)
		if err != nil {
			return
		}
		if h.askClaim(ctx, cl, page, next, line) {
			return
		}
		// The reply failed (marked failed): try the one after.
		_, _ = db.SetLiveClaimState(ctx, h.Accounts, next.ID, []string{"queued"}, "failed")
	}
}

// livePostback handles a tap in Messenger: Sí/No or the channel question.
func (h *Handler) livePostback(ctx context.Context, page pageCtx, psid, payload string) {
	prefix, id, ok := payloadClaim(payload, live.PayloadYes, live.PayloadNo, live.PayloadChannelWA, live.PayloadChannelMS)
	if !ok {
		return
	}
	claim, err := db.GetLiveClaim(ctx, h.Accounts, id)
	if err != nil || claim.PageID != page.PageID {
		return
	}
	chat := messengerChat{h: h, page: page, id: psid}
	switch prefix {
	case live.PayloadChannelWA, live.PayloadChannelMS:
		h.answerChannel(ctx, chat, claim, prefix == live.PayloadChannelWA)
	default:
		h.answerClaim(ctx, chat, claim, prefix == live.PayloadYes)
	}
}

// answerClaim handles "Sí" or "No", from Messenger or WhatsApp.
func (h *Handler) answerClaim(ctx context.Context, chat buyerChat, claim db.LiveClaim, yes bool) {
	cl, err := h.odooFor(ctx, claim.AccountID)
	if err != nil {
		log.Printf("live: answer %d: erp: %v", claim.ID, err)
		return
	}
	id := claim.ID
	if !yes {
		if ok, _ := db.SetLiveClaimState(ctx, h.Accounts, id, []string{"asked"}, "declined"); !ok {
			return
		}
		_ = erpRelease(ctx, cl, claim.OdooLineID)
		chat.say(ctx, live.Declined)
		h.advance(ctx, cl, claim.AccountID, claim.OdooLineID)
		return
	}
	if ok, _ := db.SetLiveClaimState(ctx, h.Accounts, id, []string{"asked"}, "confirmed"); !ok {
		if claim.State == "expired" {
			chat.say(ctx, live.Expired)
		}
		return
	}
	res, err := erpConfirm(ctx, cl, claim.OdooLineID)
	if err != nil {
		log.Printf("live: confirm line %d: %v", claim.OdooLineID, err)
		chat.say(ctx, "Tuvimos un problema al apartar tu pedido. La tienda te escribirá en un momento.")
		return
	}
	psid := claim.PSID
	if chat.psid() != "" {
		psid = chat.psid()
	}
	_ = db.SetLiveClaimBuyer(ctx, h.Accounts, id, psid, res.CardID)
	if claim.BuyerID > 0 && h.featureOn(ctx, claim.AccountID, featureLiveCRM) {
		// They read the notice in the first message and went on.
		if chat.psid() != "" {
			_ = erpLinkPSID(ctx, cl, claim.BuyerID, chat.psid())
		}
		_ = erpSetReach(ctx, cl, claim.BuyerID, map[string]string{"consent_channel": chat.channel()})
	}
	first := live.FirstName(claim.AuthorName)
	var msg, step string
	switch {
	case res.Phone == "":
		step, msg = "phone", live.AfterYes(first, res.DiscountPct, "", "")
	case res.Address == "":
		step, msg = "address", live.AfterYes(first, res.DiscountPct, "", "")
		msg = strings.Replace(msg, " Para tu envío, escríbenos tu número de teléfono.", " "+live.AskAddress, 1)
	default:
		step, msg = "change", live.AfterYes(first, res.DiscountPct, res.Address, res.Zone)
	}
	t := db.LiveThread{AccountID: claim.AccountID, Step: step, CardID: res.CardID, ClaimID: claim.ID, PageID: claim.PageID}
	chat.saveThread(ctx, t)
	chat.say(ctx, msg)
	if step == "change" {
		// Nothing missing: the channel question (if it applies) comes now.
		if h.wantsChannelQuestion(ctx, cl, claim) {
			t.Step = "channel"
			chat.saveThread(ctx, t)
			chat.ask(ctx, live.AskChannel, channelChoices(claim.ID))
		}
	}
	if claim.Source == "post" && claim.PostID != "" {
		if page, err := h.pageFor(ctx, claim.PageID); err == nil {
			// Sold: the post comes down so nobody else comments "mío".
			state := "sold"
			if err := h.Meta.DeletePost(ctx, claim.PostID, page.token); err == nil {
				state = "deleted"
			}
			_ = db.SetLivePostState(ctx, h.Accounts, claim.PostID, state)
		}
	}
}

var addressHint = regexp.MustCompile(`(?i)\d|zona|calle|avenida|ave\.|col\.|colonia|aldea|km|lote|casa|apto|residencial|condominio`)

// liveText handles a buyer's Messenger message after "Sí"; false when
// it isn't part of a purchase conversation.
func (h *Handler) liveText(ctx context.Context, page pageCtx, psid, text string) bool {
	chat := messengerChat{h: h, page: page, id: psid}
	t, err := chat.thread(ctx)
	if err != nil || t.Step == "" || (t.CardID == 0 && t.Step != "channel") {
		return false // not a live conversation: the seller answers it
	}
	h.shippingText(ctx, chat, t, text)
	return true
}

// shippingText: the phone, the address, a new address, or the answer to
// the channel question typed instead of tapped.
func (h *Handler) shippingText(ctx context.Context, chat buyerChat, t db.LiveThread, text string) {
	cl, err := h.odooFor(ctx, t.AccountID)
	if err != nil {
		return
	}
	var claim db.LiveClaim
	if t.ClaimID != 0 {
		claim, _ = db.GetLiveClaim(ctx, h.Accounts, t.ClaimID)
	}
	if t.Step == "channel" {
		n := live.Normalize(text)
		switch {
		case strings.Contains(n, "whatsapp") || strings.Contains(n, "wasap") || n == "wa":
			h.answerChannel(ctx, chat, claim, true)
			return
		case strings.Contains(n, "messenger") || strings.Contains(n, "facebook") || strings.Contains(n, "mensajero"):
			h.answerChannel(ctx, chat, claim, false)
			return
		}
		if t.CardID == 0 || len(strings.TrimSpace(text)) < 6 || !addressHint.MatchString(text) {
			chat.ask(ctx, live.AskChannel, channelChoices(t.ClaimID))
			return
		}
		t.Step = "change" // a new address typed instead
	}
	reply := ""
	switch t.Step {
	case "phone":
		phone, ok := live.Phone(text)
		if !ok {
			chat.say(ctx, live.BadPhone)
			return
		}
		if err := erpSetShipping(ctx, cl, t.CardID, phone, "", ""); err != nil {
			log.Printf("live: shipping card %d: %v", t.CardID, err)
			return
		}
		t.Step = "address"
		chat.saveThread(ctx, t)
		chat.say(ctx, live.AskAddress)
		return
	case "address", "change":
		if len(strings.TrimSpace(text)) < 6 || !addressHint.MatchString(text) {
			if t.Step == "address" {
				chat.say(ctx, live.AskAddress)
			}
			return
		}
		addr, zone := live.Address(text)
		if err := erpSetShipping(ctx, cl, t.CardID, "", addr, zone); err != nil {
			log.Printf("live: shipping card %d: %v", t.CardID, err)
			return
		}
		if t.Step == "change" {
			reply = "Listo, lo enviamos a " + strings.Trim(addr+", "+zone, ", ") + "."
			t.Step = ""
			chat.saveThread(ctx, t)
			chat.say(ctx, reply)
			return
		}
		// The data is complete: close (with the channel question first).
		if claim.ID != 0 {
			h.closeConfirmation(ctx, chat, cl, claim, t)
			return
		}
		t.Step = ""
		chat.saveThread(ctx, t)
		chat.say(ctx, live.Done)
	}
}

// --- the live on air ---------------------------------------------------------

var liveWorkers sync.Map // session id -> struct{}

// startLiveWorker reads a session's comments while it's on air.
func (h *Handler) startLiveWorker(sessionID string) {
	if _, running := liveWorkers.LoadOrStore(sessionID, struct{}{}); running {
		return
	}
	go func() {
		defer liveWorkers.Delete(sessionID)
		h.runLive(context.Background(), sessionID)
	}()
}

// livePoll is how often a live's comments are read; liveStatusEvery how
// often its status is checked (to notice it ended).
var (
	livePoll        = 2 * time.Second
	liveStatusEvery = 20 * time.Second
)

func (h *Handler) runLive(ctx context.Context, sessionID string) {
	seen := map[string]bool{}
	lastStatus := time.Now()
	failures := 0
	for {
		time.Sleep(livePoll)
		sess, err := db.GetLiveSession(ctx, h.Accounts, "", sessionID)
		if err != nil || sess.Status != "live" || sess.PlatformLiveID == "" {
			return
		}
		page, err := h.pageFor(ctx, sess.PageID)
		if err != nil {
			log.Printf("live %s: page: %v", sessionID, err)
			return
		}
		since := time.Now().Add(-2 * time.Minute)
		if sess.StartedAt != nil {
			since = sess.StartedAt.Add(-time.Minute)
		}
		if sess.LastCommentAt != nil {
			since = sess.LastCommentAt.Add(-5 * time.Second)
		}
		comments, err := h.Meta.LiveComments(ctx, sess.PlatformLiveID, page.token, since)
		if err != nil {
			failures++
			if errors.Is(err, meta.ErrTokenInvalid) || failures > 30 {
				log.Printf("live %s: stopping, comments: %v", sessionID, err)
				return
			}
			continue
		}
		failures = 0
		sort.SliceStable(comments, func(i, j int) bool { return comments[i].CreatedTime.Before(comments[j].CreatedTime) })
		fresh := comments[:0]
		for _, c := range comments {
			if !seen[c.ID] {
				seen[c.ID] = true
				fresh = append(fresh, c)
			}
		}
		if len(fresh) > 0 {
			h.liveBatch(ctx, sess, page, fresh)
		}
		if time.Since(lastStatus) >= liveStatusEvery {
			lastStatus = time.Now()
			h.liveReactions(ctx, sess, page, seen)
			if v, err := h.Meta.Live(ctx, sess.PlatformLiveID, page.token); err == nil && !v.IsLive() && v.Status != "" &&
				v.Status != "UNPUBLISHED" && v.Status != "SCHEDULED_LIVE" && v.Status != "PREVIEW" {
				h.endLive(ctx, sess)
				return
			}
		}
	}
}

func (h *Handler) liveBatch(ctx context.Context, sess db.LiveSession, page pageCtx, comments []meta.Comment) {
	cl, err := h.odooFor(ctx, sess.AccountID)
	if err != nil {
		return
	}
	sum, err := erpLiveSummary(ctx, cl, sess.OdooLiveID)
	if err != nil {
		log.Printf("live %s: summary: %v", sess.ID, err)
		return
	}
	lines := sum.Lines
	newest := time.Time{}
	people, seen := []prospect{}, map[string]bool{}
	for _, c := range comments {
		took, matched := h.liveComment(ctx, cl, sess, page, lines, c)
		if took {
			if s, err := erpLiveSummary(ctx, cl, sess.OdooLiveID); err == nil {
				lines = s.Lines
			}
		}
		if matched {
			seen[c.From.ID] = true // a customer now, not a prospect
		}
		if !matched && c.From.ID != "" && !seen[c.From.ID] {
			seen[c.From.ID] = true
			people = append(people, prospect{CommenterID: c.From.ID, Name: c.From.Name, Source: "comment_other"})
		}
		if c.CreatedTime.After(newest) {
			newest = c.CreatedTime
		}
	}
	h.touchProspects(ctx, cl, sess.AccountID, page.PageID, sess.OdooLiveID, people)
	if !newest.IsZero() {
		_ = db.TouchLiveSession(ctx, h.Accounts, sess.ID, newest)
	}
}

// endLive closes a session: what nobody bought becomes "No vendido" in the
// ERP; pending confirmations keep their time to answer.
func (h *Handler) endLive(ctx context.Context, sess db.LiveSession) {
	ended, err := db.EndLiveSession(ctx, h.Accounts, sess.ID)
	if err != nil || !ended {
		return
	}
	if cl, err := h.odooFor(ctx, sess.AccountID); err == nil {
		if err := erpLiveFinish(ctx, cl, sess.OdooLiveID); err != nil {
			log.Printf("live %s: finish: %v", sess.ID, err)
		}
	}
}

// LiveLoop resumes the lives on air after a restart and expires the
// confirmations nobody answered. Run it once, in the background.
func (h *Handler) LiveLoop(ctx context.Context) {
	if active, err := db.ActiveLiveSessions(ctx, h.Accounts); err == nil {
		for _, s := range active {
			h.startLiveWorker(s.ID)
		}
	}
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.expireClaims(ctx)
		}
	}
}

func (h *Handler) expireClaims(ctx context.Context) {
	minutes := h.Cfg.LiveConfirmMinutes
	if minutes <= 0 {
		minutes = 15
	}
	claims, err := db.ExpiredClaims(ctx, h.Accounts, time.Now().Add(-time.Duration(minutes)*time.Minute))
	if err != nil {
		return
	}
	for _, c := range claims {
		if ok, _ := db.SetLiveClaimState(ctx, h.Accounts, c.ID, []string{"asked"}, "expired"); !ok {
			continue
		}
		cl, err := h.odooFor(ctx, c.AccountID)
		if err != nil {
			continue
		}
		_ = erpRelease(ctx, cl, c.OdooLineID)
		h.advance(ctx, cl, c.AccountID, c.OdooLineID)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
