package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/whatsapp"
)

// AndRho's WhatsApp (internal/whatsapp, /root/README.md §2.2): one number
// for every account. A message arrives at POST /meta/webhook
// (object=whatsapp_business_account) and goes through waRoutes in order;
// the first route that takes it answers. People are known by the number
// they linked from the dashboard (POST /account/me/whatsapp/link) and act
// with their role's permissions, never more.
//
// Meta's AI policy (since 2026-01-15): no general-purpose chat. Anything
// outside what AndRho does gets a short answer pointing to what it does.

// waRoute handles a message if it's for it.
type waRoute func(ctx context.Context, m whatsapp.Message) bool

// waRoutes, in order. Features add theirs before waKnownUser.
func (h *Handler) waRoutes() []waRoute {
	return []waRoute{h.waLinkUser, h.waWorkforce, h.waKnownUser}
}

// handleWhatsApp handles a WhatsApp webhook (after the signature check).
func (h *Handler) handleWhatsApp(ctx context.Context, wh whatsapp.Webhook) {
	if !h.WA.Enabled() {
		return
	}
	for _, m := range wh.Messages(h.WA.PhoneNumberID) {
		if m.From == "" {
			continue
		}
		if err := db.TouchWAInbound(ctx, h.Accounts, m.From, m.At); err != nil {
			log.Printf("whatsapp: inbound %s: %v", maskPhone(m.From), err)
		}
		_ = h.WA.MarkRead(ctx, m.ID)
		taken := false
		for _, route := range h.waRoutes() {
			if route(ctx, m) {
				taken = true
				break
			}
		}
		if !taken {
			h.waSay(ctx, m.From, waStranger)
		}
	}
}

const (
	waStranger = "Hola, este es el WhatsApp de AndRho. Para usarlo, vincula tu número desde tu panel: Configuración → Tu WhatsApp."
	waLinked   = "Listo, %s: tu WhatsApp quedó vinculado a tu cuenta de AndRho."
	waOffTopic = "Por aquí te ayudo con tu negocio en AndRho. Lo demás lo ves en tu panel: %s"
)

// waSay sends text inside the window (the person just wrote).
func (h *Handler) waSay(ctx context.Context, to, text string) {
	if _, err := h.WA.SendText(ctx, to, text); err != nil {
		log.Printf("whatsapp: send to %s: %v", maskPhone(to), err)
	}
}

// waAsk sends text with reply buttons.
func (h *Handler) waAsk(ctx context.Context, to, text string, buttons []whatsapp.Button) {
	if _, err := h.WA.SendButtons(ctx, to, text, buttons); err != nil {
		log.Printf("whatsapp: send to %s: %v", maskPhone(to), err)
	}
}

// --- linking a user's number ------------------------------------------------

const waCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

var waLinkPattern = regexp.MustCompile(`(?i)vincular\s+andrho\s+([A-Z0-9]{6})\b`)

func newCode(n int) string {
	var b strings.Builder
	max := big.NewInt(int64(len(waCodeAlphabet)))
	for range n {
		i, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b.WriteByte(waCodeAlphabet[i.Int64()])
	}
	return b.String()
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(sum[:])
}

// codeGuesses limits wrong codes per number: a code links a phone to a
// user (or to an order), so nobody may try them by the thousand.
type codeGuesses struct {
	mu   sync.Mutex
	bad  map[string][]time.Time
	max  int
	span time.Duration
}

var waGuesses = &codeGuesses{bad: map[string][]time.Time{}, max: 5, span: time.Hour}

// blocked reports whether phone used up its wrong codes for now.
func (g *codeGuesses) blocked(phone string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	recent := g.bad[phone][:0]
	for _, t := range g.bad[phone] {
		if now.Sub(t) < g.span {
			recent = append(recent, t)
		}
	}
	g.bad[phone] = recent
	return len(recent) >= g.max
}

// miss records a wrong code from phone.
func (g *codeGuesses) miss(phone string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bad[phone] = append(g.bad[phone], now)
}

const waTooManyCodes = "Demasiados códigos que no sirven. Espera una hora e inténtalo de nuevo."

// waLinkUser takes "Vincular AndRho ABC123" sent from the number to link.
func (h *Handler) waLinkUser(ctx context.Context, m whatsapp.Message) bool {
	match := waLinkPattern.FindStringSubmatch(m.Text)
	if match == nil {
		return false
	}
	if waGuesses.blocked(m.From, time.Now()) {
		h.waSay(ctx, m.From, waTooManyCodes)
		return true
	}
	userID, err := db.ConsumeWALinkCode(ctx, h.Accounts, hashCode(match[1]))
	if err != nil {
		waGuesses.miss(m.From, time.Now())
		h.waSay(ctx, m.From, "Ese código ya no sirve. Pide uno nuevo en tu panel: Configuración → Tu WhatsApp.")
		return true
	}
	if err := db.SetUserWhatsApp(ctx, h.Accounts, userID, m.From); err != nil {
		log.Printf("whatsapp: link user %s: %v", userID, err)
		h.waSay(ctx, m.From, "No pudimos vincular tu número. Intenta de nuevo en un momento.")
		return true
	}
	u, err := db.GetUserByID(ctx, h.Accounts, userID)
	name := "ya"
	if err == nil {
		_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), u.AccountID, &u.ID, "whatsapp_linked", "WhatsApp vinculado")
		if first := strings.Fields(u.DisplayName); len(first) > 0 {
			name = first[0]
		}
	}
	h.waSay(ctx, m.From, strings.Replace(waLinked, "%s", name, 1))
	return true
}

// waKnownUser answers a linked user whose message no feature took.
func (h *Handler) waKnownUser(ctx context.Context, m whatsapp.Message) bool {
	u, err := db.UserByWhatsApp(ctx, h.Accounts, m.From)
	if err != nil {
		return false
	}
	_ = u
	h.waSay(ctx, m.From, strings.Replace(waOffTopic, "%s", h.Cfg.DashboardURL, 1))
	return true
}

// --- dashboard ------------------------------------------------------------------

// MyWhatsApp handles GET /account/me/whatsapp: the user's linked number.
func (h *Handler) MyWhatsApp(c *gin.Context) {
	claims, _ := currentClaims(c)
	phone, at, err := db.UserWhatsApp(c.Request.Context(), h.Accounts, claims.Sub)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": h.WA.Enabled(), "linked": phone != "", "phone": maskPhone(phone), "verified_at": at})
}

// LinkMyWhatsApp handles POST /account/me/whatsapp/link: a code the user
// sends from their WhatsApp to AndRho's number (15 minutes).
func (h *Handler) LinkMyWhatsApp(c *gin.Context) {
	claims, _ := currentClaims(c)
	if !h.WA.Enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "whatsapp_off", "message": "El WhatsApp de AndRho todavía no está activo."})
		return
	}
	ctx := c.Request.Context()
	code := newCode(6)
	expires := time.Now().Add(15 * time.Minute)
	if err := db.SaveWALinkCode(ctx, h.Accounts, hashCode(code), claims.Sub, expires); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	text := "Vincular AndRho " + code
	number := h.WA.DisplayNumber(ctx)
	link := ""
	if number != "" {
		link = whatsapp.ChatLink(number, text)
	}
	c.JSON(http.StatusOK, gin.H{"code": code, "text": text, "number": number, "link": link, "expires_at": expires})
}

// UnlinkMyWhatsApp handles DELETE /account/me/whatsapp.
func (h *Handler) UnlinkMyWhatsApp(c *gin.Context) {
	claims, _ := currentClaims(c)
	if err := db.ClearUserWhatsApp(c.Request.Context(), h.Accounts, claims.Sub); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.Status(http.StatusNoContent)
}

// maskPhone shows only the last 4 digits ("•••• 5555"), for the dashboard
// and the logs.
func maskPhone(p string) string {
	if p == "" {
		return ""
	}
	if len(p) <= 4 {
		return "••••"
	}
	return "•••• " + p[len(p)-4:]
}
