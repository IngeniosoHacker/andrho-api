package whatsapp

// A store's own number, connected with Embedded Signup (docs/auditoria-
// whatsapp-por-cliente.md). AndRho is a Tech Provider: each store's
// WhatsApp Business Account (WABA) is the store's, with its own display
// name, quality and payment method; AndRho holds a business token for it,
// sealed with SECRETS_KEY, and never logs it.
//
//	browser  FB.login(config_id, extras.version "v5") -> code + session event
//	server   ExchangeCode -> DebugToken (which WABA the token can manage)
//	         -> SubscribeApp -> Register (new number) or SyncSMBApp
//	         (Coexistence) -> PhoneInfo -> CreateTemplate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ForAccount is the client for a store's number (its business token).
func ForAccount(token, phoneNumberID, wabaID, version string) *Client {
	return New(true, token, phoneNumberID, wabaID, version)
}

// App is AndRho's Meta app, for the calls that need its secret (the code
// exchange and reading what a token may do).
type App struct {
	ID, Secret, Version, Graph string
	HTTP                       *http.Client
}

// NewApp builds the app client.
func NewApp(id, secret, version string) *App {
	if version == "" {
		version = "v27.0"
	}
	return &App{ID: id, Secret: secret, Version: version, Graph: "https://graph.facebook.com", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Enabled reports whether the app is configured.
func (a *App) Enabled() bool { return a != nil && a.ID != "" && a.Secret != "" }

// client is an unauthenticated Client on the app's Graph address, for
// requests that carry their own credentials in the query.
func (a *App) client(token string) *Client {
	c := New(true, token, "", "", a.Version)
	c.Graph, c.HTTP = a.Graph, a.HTTP
	return c
}

// ExchangeCode trades Embedded Signup's code (it lives 5 minutes) for the
// store's business token. Server to server only.
func (a *App) ExchangeCode(ctx context.Context, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", errors.New("whatsapp: empty code")
	}
	q := url.Values{"client_id": {a.ID}, "client_secret": {a.Secret}, "code": {code}}
	var res struct {
		AccessToken string `json:"access_token"`
	}
	c := a.client("")
	if err := c.do(ctx, http.MethodGet, c.endpoint("oauth/access_token")+"?"+q.Encode(), nil, &res); err != nil {
		return "", err
	}
	if res.AccessToken == "" {
		return "", errors.New("whatsapp: no token in the code exchange")
	}
	return res.AccessToken, nil
}

// Grants is what a business token may manage: the WABAs it was given in
// the flow (whatsapp_business_management) and whether it may message.
type Grants struct {
	WABAs     []string
	Messaging bool
}

// DebugToken reads what token was granted. The WABA comes from here, not
// from the browser: the session event is only a hint.
func (a *App) DebugToken(ctx context.Context, token string) (Grants, error) {
	q := url.Values{"input_token": {token}}
	var res struct {
		Data struct {
			IsValid bool `json:"is_valid"`
			Scopes  []struct {
				Scope     string   `json:"scope"`
				TargetIDs []string `json:"target_ids"`
			} `json:"granular_scopes"`
		} `json:"data"`
	}
	c := a.client(a.ID + "|" + a.Secret)
	if err := c.do(ctx, http.MethodGet, c.endpoint("debug_token")+"?"+q.Encode(), nil, &res); err != nil {
		return Grants{}, err
	}
	if !res.Data.IsValid {
		return Grants{}, errors.New("whatsapp: the token is not valid")
	}
	g := Grants{}
	for _, s := range res.Data.Scopes {
		switch s.Scope {
		case "whatsapp_business_management":
			g.WABAs = append(g.WABAs, s.TargetIDs...)
		case "whatsapp_business_messaging":
			g.Messaging = true
		}
	}
	return g, nil
}

// PickWABA chooses the store's WABA: the browser's hint when the token
// really manages it, else the only one it manages.
func PickWABA(g Grants, hints ...string) (string, error) {
	for _, h := range hints {
		if h != "" && slices.Contains(g.WABAs, h) {
			return h, nil
		}
	}
	uniq := []string{}
	for _, w := range g.WABAs {
		if !slices.Contains(uniq, w) {
			uniq = append(uniq, w)
		}
	}
	switch len(uniq) {
	case 0:
		return "", errors.New("whatsapp: no WhatsApp account was shared")
	case 1:
		return uniq[0], nil
	}
	return "", errors.New("whatsapp: more than one WhatsApp account was shared")
}

// PhoneNumber is a number of a WABA as Meta describes it.
type PhoneNumber struct {
	ID                 string `json:"id"`
	DisplayPhoneNumber string `json:"display_phone_number"`
	VerifiedName       string `json:"verified_name"`
	NameStatus         string `json:"name_status"`
	QualityRating      string `json:"quality_rating"`
	PlatformType       string `json:"platform_type"`
	IsOnBizApp         bool   `json:"is_on_biz_app"`
	CodeVerification   string `json:"code_verification_status"`
}

const phoneFields = "id,display_phone_number,verified_name,name_status,quality_rating,platform_type,is_on_biz_app,code_verification_status"

// PhoneNumbers lists the WABA's numbers.
func (c *Client) PhoneNumbers(ctx context.Context) ([]PhoneNumber, error) {
	var res struct {
		Data []PhoneNumber `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, c.endpoint(url.PathEscape(c.WABAID)+"/phone_numbers")+"?fields="+phoneFields, nil, &res)
	return res.Data, err
}

// PickPhone chooses the number: the browser's hint when it's in the WABA,
// else the only one.
func PickPhone(nums []PhoneNumber, hint string) (PhoneNumber, error) {
	for _, n := range nums {
		if hint != "" && n.ID == hint {
			return n, nil
		}
	}
	if len(nums) == 1 {
		return nums[0], nil
	}
	if len(nums) == 0 {
		return PhoneNumber{}, errors.New("whatsapp: the WhatsApp account has no number")
	}
	return PhoneNumber{}, errors.New("whatsapp: which number? more than one in the account")
}

// PhoneInfo reads the number's name, its approval and quality.
func (c *Client) PhoneInfo(ctx context.Context) (PhoneNumber, error) {
	var p PhoneNumber
	err := c.do(ctx, http.MethodGet, c.endpoint(url.PathEscape(c.PhoneNumberID))+"?fields="+phoneFields, nil, &p)
	return p, err
}

// SubscribeApp makes the store's WABA send its webhooks to AndRho's app
// (the same /meta/webhook, the same signature).
func (c *Client) SubscribeApp(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, c.endpoint(url.PathEscape(c.WABAID)+"/subscribed_apps"), nil, nil)
}

// UnsubscribeApp stops them (disconnecting; the number stays the store's).
func (c *Client) UnsubscribeApp(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, c.endpoint(url.PathEscape(c.WABAID)+"/subscribed_apps"), nil, nil)
}

// NewPIN is a random 6-digit two-step verification PIN.
func NewPIN() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// Register puts a new number on the Cloud API with its two-step PIN. Not
// for Coexistence: that number is already registered on the app.
func (c *Client) Register(ctx context.Context, pin string) error {
	return c.do(ctx, http.MethodPost, c.endpoint(url.PathEscape(c.PhoneNumberID)+"/register"),
		map[string]string{"messaging_product": "whatsapp", "pin": pin}, nil)
}

// SyncSMBApp starts one of Coexistence's syncs ("smb_app_state_sync" for
// contacts, "history" for chats). Meta allows each once, within 24 h of
// the signup; AndRho asks for both and keeps neither (decision
// 2026-11-27: the chats are discarded).
func (c *Client) SyncSMBApp(ctx context.Context, syncType string) error {
	return c.do(ctx, http.MethodPost, c.endpoint(url.PathEscape(c.PhoneNumberID)+"/smb_app_data"),
		map[string]string{"messaging_product": "whatsapp", "sync_type": syncType}, nil)
}

// Funded reports whether the WABA has a payment method (Meta's
// primary_funding_id). ok=false when Meta didn't say: the caller keeps
// what it knew (a send failing with 131042 also says it).
func (c *Client) Funded(ctx context.Context) (funded, ok bool) {
	var res struct {
		Funding *string `json:"primary_funding_id"`
	}
	if err := c.do(ctx, http.MethodGet, c.endpoint(url.PathEscape(c.WABAID))+"?fields=primary_funding_id", nil, &res); err != nil {
		return false, false
	}
	return res.Funding != nil && *res.Funding != "", true
}

// --- templates ---------------------------------------------------------------------

// Template is a message template AndRho creates in a store's WABA. Body
// has {{1}}, {{2}}…; Example fills them for Meta's review.
type Template struct {
	Name, Language, Category, Body string
	Example                        []string
	// Buttons are quick reply buttons (their payload is set when sending).
	Buttons []string
}

// TemplateStatus is a template as Meta has it.
type TemplateStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
	Status   string `json:"status"` // APPROVED, PENDING, REJECTED, PAUSED, DISABLED…
	Category string `json:"category"`
	Reason   string `json:"rejected_reason"`
}

// CreateTemplate submits t for review. Meta may give it another category
// than the one asked (utility -> marketing): the answer says which.
func (c *Client) CreateTemplate(ctx context.Context, t Template) (TemplateStatus, error) {
	body := map[string]any{"type": "BODY", "text": t.Body}
	if len(t.Example) > 0 {
		body["example"] = map[string]any{"body_text": [][]string{t.Example}}
	}
	comps := []any{body}
	if len(t.Buttons) > 0 {
		bs := []map[string]string{}
		for _, b := range t.Buttons {
			bs = append(bs, map[string]string{"type": "QUICK_REPLY", "text": clip(b, 25)})
		}
		comps = append(comps, map[string]any{"type": "BUTTONS", "buttons": bs})
	}
	var res TemplateStatus
	err := c.do(ctx, http.MethodPost, c.endpoint(url.PathEscape(c.WABAID)+"/message_templates"), map[string]any{
		"name": t.Name, "language": t.Language, "category": t.Category, "components": comps,
	}, &res)
	if res.Name == "" {
		res.Name, res.Language = t.Name, t.Language
	}
	return res, err
}

// Templates lists the WABA's templates (name, status, category).
func (c *Client) Templates(ctx context.Context) ([]TemplateStatus, error) {
	var res struct {
		Data []TemplateStatus `json:"data"`
	}
	err := c.do(ctx, http.MethodGet, c.endpoint(url.PathEscape(c.WABAID)+"/message_templates")+"?fields=id,name,language,status,category,rejected_reason&limit=200", nil, &res)
	return res.Data, err
}

// ErrPayment is code 131042: the WABA has no valid payment method.
var ErrPayment = errors.New("whatsapp: the account has no payment method")

// PaymentLink is where a store adds its payment method (WhatsApp Manager).
const PaymentLink = "https://business.facebook.com/wa/manage/home/"
