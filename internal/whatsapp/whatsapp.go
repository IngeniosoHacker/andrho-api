// Package whatsapp talks to the WhatsApp Cloud API with AndRho's own number
// (VPS/whatsapp/README.md, /root/README.md §2.2): the control channel where
// people of a client account write to AndRho, and, while AndRho is not a
// Meta Tech Provider, the test stand-in for a store's number in live sales.
//
// Meta's rules this package leaves to the caller (founder docs, audit
// docs/auditoria-crm-en-vivo-y-personal-whatsapp.md in the andrho repo):
//   - free-form messages only within 24 h of the person's last message
//     (InWindow); outside it, only approved templates (SendTemplate);
//   - a template needs the person's opt-in naming the business;
//   - with Meta's test number, messages reach only the 5 verified numbers.
//
// Everything is behind WHATSAPP_ENABLED: without it (or without the token
// and phone number id) Enabled() is false and nothing here is called.
package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Window is WhatsApp's customer service window.
const Window = 24 * time.Hour

// Client is AndRho's WhatsApp number.
type Client struct {
	On            bool
	Token         string // a System User's permanent token
	PhoneNumberID string
	WABAID        string
	Version       string // "v27.0"
	Graph         string // https://graph.facebook.com (tests point it elsewhere)
	HTTP          *http.Client

	mu      sync.Mutex
	display string // the number people write to, read once from Meta
}

// New builds the client; Enabled() says whether it may be used.
func New(enabled bool, token, phoneNumberID, wabaID, version string) *Client {
	if version == "" {
		version = "v27.0"
	}
	return &Client{On: enabled, Token: token, PhoneNumberID: phoneNumberID, WABAID: wabaID, Version: version,
		Graph: "https://graph.facebook.com", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Enabled reports whether AndRho's WhatsApp is configured and turned on.
func (c *Client) Enabled() bool {
	return c != nil && c.On && c.Token != "" && c.PhoneNumberID != ""
}

// Error is an error from the Cloud API.
type Error struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details struct {
		Details string `json:"details"`
	} `json:"error_data"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("whatsapp: %d %s (code %d)", e.Status, e.Message, e.Code)
}

// ErrOutsideWindow is code 131047: more than 24 h since the person's last
// message; only a template can go.
var ErrOutsideWindow = errors.New("whatsapp: outside the 24 h window")

// ErrNotAllowed is code 131030: with the test number, the recipient isn't
// one of the verified numbers.
var ErrNotAllowed = errors.New("whatsapp: recipient not in the allowed list")

func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.Graph, "/") + "/" + c.Version + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) do(ctx context.Context, method, rawURL string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		var env struct {
			Error *Error `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			env.Error.Status = res.StatusCode
			switch env.Error.Code {
			case 131047:
				return fmt.Errorf("%w: %s", ErrOutsideWindow, env.Error.Message)
			case 131030:
				return fmt.Errorf("%w: %s", ErrNotAllowed, env.Error.Message)
			}
			return env.Error
		}
		return &Error{Status: res.StatusCode, Message: strings.TrimSpace(string(raw))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) send(ctx context.Context, to string, msg map[string]any) (string, error) {
	msg["messaging_product"] = "whatsapp"
	msg["recipient_type"] = "individual"
	msg["to"] = Digits(to)
	var res struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := c.do(ctx, http.MethodPost, c.endpoint(c.PhoneNumberID+"/messages"), msg, &res); err != nil {
		return "", err
	}
	if len(res.Messages) > 0 {
		return res.Messages[0].ID, nil
	}
	return "", nil
}

// SendText sends a free-form message (inside the 24 h window).
func (c *Client) SendText(ctx context.Context, to, text string) (string, error) {
	return c.send(ctx, to, map[string]any{"type": "text",
		"text": map[string]any{"body": clip(text, 4096), "preview_url": strings.Contains(text, "https://")}})
}

// Button is a reply button: Title is what the person sees (Meta cuts it at
// 20 characters), ID what comes back.
type Button struct {
	ID, Title string
}

// SendButtons sends text with up to 3 reply buttons (inside the window).
func (c *Client) SendButtons(ctx context.Context, to, text string, buttons []Button) (string, error) {
	if len(buttons) == 0 {
		return c.SendText(ctx, to, text)
	}
	if len(buttons) > 3 {
		buttons = buttons[:3]
	}
	bs := make([]map[string]any, 0, len(buttons))
	for _, b := range buttons {
		bs = append(bs, map[string]any{"type": "reply", "reply": map[string]string{"id": clip(b.ID, 256), "title": clip(b.Title, 20)}})
	}
	return c.send(ctx, to, map[string]any{"type": "interactive", "interactive": map[string]any{
		"type": "button", "body": map[string]string{"text": clip(text, 1024)}, "action": map[string]any{"buttons": bs},
	}})
}

// SendTemplate sends an approved template (the only thing allowed outside
// the window); params fill the body's {{1}}, {{2}}…
func (c *Client) SendTemplate(ctx context.Context, to, name, lang string, params []string) (string, error) {
	if lang == "" {
		lang = "es"
	}
	tpl := map[string]any{"name": name, "language": map[string]string{"code": lang}}
	if len(params) > 0 {
		ps := make([]map[string]string, 0, len(params))
		for _, p := range params {
			ps = append(ps, map[string]string{"type": "text", "text": p})
		}
		tpl["components"] = []map[string]any{{"type": "body", "parameters": ps}}
	}
	return c.send(ctx, to, map[string]any{"type": "template", "template": tpl})
}

// MarkRead marks an incoming message read (the blue ticks).
func (c *Client) MarkRead(ctx context.Context, messageID string) error {
	return c.do(ctx, http.MethodPost, c.endpoint(c.PhoneNumberID+"/messages"),
		map[string]string{"messaging_product": "whatsapp", "status": "read", "message_id": messageID}, nil)
}

// Media downloads a photo or file someone sent: first its short-lived URL,
// then the bytes (with the same token). The caller keeps them in memory
// only; AndRho stores personal media nowhere but the client's ERP.
func (c *Client) Media(ctx context.Context, mediaID string) ([]byte, string, error) {
	var meta struct {
		URL      string `json:"url"`
		MimeType string `json:"mime_type"`
		FileSize int    `json:"file_size"`
	}
	if err := c.do(ctx, http.MethodGet, c.endpoint(url.PathEscape(mediaID)), nil, &meta); err != nil {
		return nil, "", err
	}
	if meta.URL == "" {
		return nil, "", errors.New("whatsapp: media without url")
	}
	if meta.FileSize > 16<<20 {
		return nil, "", errors.New("whatsapp: media too large")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("whatsapp: media: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, "", fmt.Errorf("whatsapp: media: status %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	return b, meta.MimeType, err
}

// DisplayNumber is the number people write to ("50255555555"), read once
// from Meta; "" when it can't be read.
func (c *Client) DisplayNumber(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.display != "" {
		return c.display
	}
	var res struct {
		Display string `json:"display_phone_number"`
	}
	if err := c.do(ctx, http.MethodGet, c.endpoint(c.PhoneNumberID)+"?fields=display_phone_number", nil, &res); err != nil {
		return ""
	}
	c.display = Digits(res.Display)
	return c.display
}

// ChatLink is a wa.me link to number with text already typed: the person
// must still press send, so the first message is theirs (that's the
// opt-in and what opens the 24 h window).
func ChatLink(number, text string) string {
	u := "https://wa.me/" + Digits(number)
	if text != "" {
		u += "?text=" + url.QueryEscape(text)
	}
	return u
}

// Digits keeps a phone number's digits ("+502 5555-5555" -> "50255555555").
func Digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// InWindow reports whether a free-form message may go to someone whose last
// message arrived at last.
func InWindow(last *time.Time, now time.Time) bool {
	return last != nil && now.Sub(*last) < Window
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// --- Webhooks ------------------------------------------------------------------

// Webhook is what Meta posts for a WhatsApp Business Account
// (object=whatsapp_business_account). Its signature is the app's, the same
// as the Page webhooks (meta.Client.VerifySignature).
type Webhook struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string `json:"field"`
			Value Value  `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// Value is one change: messages that arrived (and delivery statuses,
// which AndRho ignores).
type Value struct {
	MessagingProduct string `json:"messaging_product"`
	Metadata         struct {
		DisplayPhoneNumber string `json:"display_phone_number"`
		PhoneNumberID      string `json:"phone_number_id"`
	} `json:"metadata"`
	Contacts []struct {
		WaID    string `json:"wa_id"`
		Profile struct {
			Name string `json:"name"`
		} `json:"profile"`
	} `json:"contacts"`
	Messages []RawMessage `json:"messages"`
}

// RawMessage is a message as Meta sends it.
type RawMessage struct {
	From      string `json:"from"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"` // text, image, interactive, button…
	Text      *struct {
		Body string `json:"body"`
	} `json:"text"`
	Image *struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		Caption  string `json:"caption"`
	} `json:"image"`
	Interactive *struct {
		Type        string `json:"type"`
		ButtonReply *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"button_reply"`
		ListReply *struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"list_reply"`
	} `json:"interactive"`
	Button *struct {
		Payload string `json:"payload"`
		Text    string `json:"text"`
	} `json:"button"`
}

// Message is an incoming message, flattened.
type Message struct {
	ID       string
	From     string // the sender's number, digits only
	Name     string // their WhatsApp profile name
	At       time.Time
	Text     string // the text, the photo's caption or the button's title
	ButtonID string // the reply button tapped, if any
	ImageID  string // a photo's media id, if any
	Kind     string // Meta's type
}

// Messages flattens a webhook into the messages for phoneNumberID ("" =
// any number of the account).
func (w Webhook) Messages(phoneNumberID string) []Message {
	out := []Message{}
	for _, e := range w.Entry {
		for _, ch := range e.Changes {
			v := ch.Value
			if ch.Field != "messages" || (phoneNumberID != "" && v.Metadata.PhoneNumberID != phoneNumberID) {
				continue
			}
			names := map[string]string{}
			for _, c := range v.Contacts {
				names[Digits(c.WaID)] = c.Profile.Name
			}
			for _, m := range v.Messages {
				msg := Message{ID: m.ID, From: Digits(m.From), Kind: m.Type, At: time.Now()}
				msg.Name = names[msg.From]
				var ts int64
				if _, err := fmt.Sscan(m.Timestamp, &ts); err == nil && ts > 0 {
					msg.At = time.Unix(ts, 0)
				}
				switch {
				case m.Text != nil:
					msg.Text = m.Text.Body
				case m.Image != nil:
					msg.ImageID, msg.Text = m.Image.ID, m.Image.Caption
				case m.Interactive != nil && m.Interactive.ButtonReply != nil:
					msg.ButtonID, msg.Text = m.Interactive.ButtonReply.ID, m.Interactive.ButtonReply.Title
				case m.Interactive != nil && m.Interactive.ListReply != nil:
					msg.ButtonID, msg.Text = m.Interactive.ListReply.ID, m.Interactive.ListReply.Title
				case m.Button != nil:
					msg.ButtonID, msg.Text = m.Button.Payload, m.Button.Text
				}
				out = append(out, msg)
			}
		}
	}
	return out
}
