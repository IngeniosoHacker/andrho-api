// Package meta talks to Meta's Graph API for AndRho's live sales
// (founder/live-sale-spec.md §2): connecting a seller's Facebook Page,
// reading its live comments, replying privately in Messenger with Sí/No
// buttons, posting what didn't sell, and checking webhook signatures.
//
// AndRho's app (META_APP_ID / META_APP_SECRET) asks each seller for a Page
// token through Facebook Login; every call about a Page uses that Page's
// token, so messages go out in the seller's name. Without the app
// configured, Enabled() is false and nothing here is called.
package meta

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Scopes are the permissions AndRho asks a seller for (spec §2.3).
var Scopes = []string{
	"pages_show_list", "pages_read_engagement", "pages_read_user_content",
	"pages_manage_engagement", "pages_manage_metadata", "pages_messaging",
	"pages_manage_posts", "business_management",
}

// WebhookFields are the Page events AndRho subscribes to.
var WebhookFields = []string{"feed", "messages", "messaging_postbacks", "live_videos"}

// Client is AndRho's Meta app.
type Client struct {
	AppID, AppSecret string
	Version          string // "v27.0"
	// LoginConfigID: a Facebook Login for Business configuration; when set
	// the dialog uses it instead of the scope list.
	LoginConfigID string
	Graph         string // https://graph.facebook.com (tests point it elsewhere)
	HTTP          *http.Client
}

// New builds the client; Enabled() says whether the app is configured.
func New(appID, appSecret, version, loginConfigID string) *Client {
	if version == "" {
		version = "v27.0"
	}
	return &Client{AppID: appID, AppSecret: appSecret, Version: version, LoginConfigID: loginConfigID,
		Graph: "https://graph.facebook.com", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Enabled reports whether AndRho's Meta app is configured.
func (c *Client) Enabled() bool { return c != nil && c.AppID != "" && c.AppSecret != "" }

// Error is an error from the Graph API.
type Error struct {
	Status  int
	Code    int    `json:"code"`
	Subcode int    `json:"error_subcode"`
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("meta: %d %s (code %d)", e.Status, e.Message, e.Code)
}

// ErrTokenInvalid: the Page token no longer works (the seller removed the
// app, changed the password or lost the Page): they must connect again.
var ErrTokenInvalid = errors.New("meta: page token invalid")

func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.Graph, "/") + "/" + c.Version + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader, contentType string, out any) error {
	u := c.endpoint(path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("meta: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		var env struct {
			Error *Error `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			env.Error.Status = res.StatusCode
			if env.Error.Code == 190 {
				return fmt.Errorf("%w: %s", ErrTokenInvalid, env.Error.Message)
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

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, q, nil, "", out)
}

func (c *Client) postJSON(ctx context.Context, path, token string, payload any, out any) error {
	body, _ := json.Marshal(payload)
	return c.do(ctx, http.MethodPost, path, url.Values{"access_token": {token}}, bytes.NewReader(body), "application/json", out)
}

// --- Facebook Login ------------------------------------------------------

// LoginURL is where the seller connects their Page; Meta comes back to
// redirectURI with ?code=…&state=….
func (c *Client) LoginURL(redirectURI, state string) string {
	q := url.Values{"client_id": {c.AppID}, "redirect_uri": {redirectURI}, "state": {state}, "response_type": {"code"}}
	if c.LoginConfigID != "" {
		q.Set("config_id", c.LoginConfigID)
	} else {
		q.Set("scope", strings.Join(Scopes, ","))
	}
	return "https://www.facebook.com/" + c.Version + "/dialog/oauth?" + q.Encode()
}

// ExchangeCode turns the login's code into a long-lived user token (about
// 60 days); the Page tokens read with it don't expire.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	var short struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.get(ctx, "oauth/access_token", url.Values{"client_id": {c.AppID}, "client_secret": {c.AppSecret},
		"redirect_uri": {redirectURI}, "code": {code}}, &short); err != nil {
		return "", err
	}
	var long struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.get(ctx, "oauth/access_token", url.Values{"grant_type": {"fb_exchange_token"}, "client_id": {c.AppID},
		"client_secret": {c.AppSecret}, "fb_exchange_token": {short.AccessToken}}, &long); err != nil {
		return "", err
	}
	return long.AccessToken, nil
}

// Page is a Facebook Page the seller manages.
type Page struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	AccessToken string   `json:"access_token"`
	Picture     string   `json:"-"`
	IGUserID    string   `json:"-"`
	Tasks       []string `json:"tasks"`
}

// Pages lists the Pages the seller manages, with their tokens.
func (c *Client) Pages(ctx context.Context, userToken string) ([]Page, error) {
	var res struct {
		Data []struct {
			Page
			IG *struct {
				ID string `json:"id"`
			} `json:"instagram_business_account"`
			Picture struct {
				Data struct {
					URL string `json:"url"`
				} `json:"data"`
			} `json:"picture"`
		} `json:"data"`
	}
	if err := c.get(ctx, "me/accounts", url.Values{"access_token": {userToken},
		"fields": {"id,name,access_token,tasks,picture{url},instagram_business_account"}, "limit": {"100"}}, &res); err != nil {
		return nil, err
	}
	out := make([]Page, 0, len(res.Data))
	for _, d := range res.Data {
		p := d.Page
		p.Picture = d.Picture.Data.URL
		if d.IG != nil {
			p.IGUserID = d.IG.ID
		}
		out = append(out, p)
	}
	return out, nil
}

// Subscribe makes Meta send this Page's events to AndRho's webhook.
func (c *Client) Subscribe(ctx context.Context, pageID, pageToken string) error {
	return c.do(ctx, http.MethodPost, pageID+"/subscribed_apps", url.Values{"access_token": {pageToken},
		"subscribed_fields": {strings.Join(WebhookFields, ",")}}, nil, "", nil)
}

// Unsubscribe stops this Page's events (the seller disconnected).
func (c *Client) Unsubscribe(ctx context.Context, pageID, pageToken string) error {
	return c.do(ctx, http.MethodDelete, pageID+"/subscribed_apps", url.Values{"access_token": {pageToken}}, nil, "", nil)
}

// --- Lives -----------------------------------------------------------------

// LiveVideo is a Page's live broadcast.
type LiveVideo struct {
	ID        string `json:"id"`
	Status    string `json:"status"` // LIVE, LIVE_STOPPED, VOD, UNPUBLISHED…
	Title     string `json:"title"`
	Permalink string `json:"permalink_url"`
	Video     struct {
		ID string `json:"id"`
	} `json:"video"`
}

// IsLive says whether the broadcast is on air.
func (v LiveVideo) IsLive() bool { return v.Status == "LIVE" }

// CurrentLive is the Page's broadcast on air now, if any.
func (c *Client) CurrentLive(ctx context.Context, pageID, pageToken string) (LiveVideo, bool, error) {
	var res struct {
		Data []LiveVideo `json:"data"`
	}
	err := c.get(ctx, pageID+"/live_videos", url.Values{"access_token": {pageToken},
		"broadcast_status": {`["LIVE"]`}, "fields": {"id,status,title,permalink_url,video"}, "limit": {"5"}}, &res)
	if err != nil {
		return LiveVideo{}, false, err
	}
	for _, v := range res.Data {
		if v.IsLive() {
			return v, true, nil
		}
	}
	return LiveVideo{}, false, nil
}

// Live reads one broadcast (its status says when it ended).
func (c *Client) Live(ctx context.Context, liveID, pageToken string) (LiveVideo, error) {
	var v LiveVideo
	err := c.get(ctx, liveID, url.Values{"access_token": {pageToken}, "fields": {"id,status,title,permalink_url,video"}}, &v)
	return v, err
}

// Comment is a comment on a live or a post.
type Comment struct {
	ID          string    `json:"id"`
	Message     string    `json:"message"`
	CreatedTime time.Time `json:"-"`
	Created     string    `json:"created_time"`
	From        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"from"`
}

// metaTime parses Graph's "2026-11-09T21:24:05+0000".
func metaTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// LiveComments reads a live's comments, newest first (Meta's advice for
// lives), with the low-quality filter off: a short "mío" must not be
// dropped. since limits to comments after that time.
func (c *Client) LiveComments(ctx context.Context, liveID, pageToken string, since time.Time) ([]Comment, error) {
	q := url.Values{"access_token": {pageToken}, "order": {"reverse_chronological"}, "live_filter": {"no_filter"},
		"filter": {"stream"}, "fields": {"id,message,created_time,from{id,name}"}, "limit": {"100"}}
	if !since.IsZero() {
		q.Set("since", fmt.Sprint(since.Unix()))
	}
	var res struct {
		Data []Comment `json:"data"`
	}
	if err := c.get(ctx, liveID+"/comments", q, &res); err != nil {
		return nil, err
	}
	for i := range res.Data {
		res.Data[i].CreatedTime = metaTime(res.Data[i].Created)
	}
	return res.Data, nil
}

// --- Messenger ---------------------------------------------------------------

// Button is a postback button.
type Button struct {
	Title, Payload string
}

func buttonTemplate(text string, buttons []Button) map[string]any {
	bs := make([]map[string]string, 0, len(buttons))
	for _, b := range buttons {
		bs = append(bs, map[string]string{"type": "postback", "title": b.Title, "payload": b.Payload})
	}
	return map[string]any{"attachment": map[string]any{"type": "template",
		"payload": map[string]any{"template_type": "button", "text": text, "buttons": bs}}}
}

// PrivateReply answers a comment in Messenger, in the Page's name, with
// buttons. Meta allows one per comment (spec §2.5): it must carry the
// whole question.
func (c *Client) PrivateReply(ctx context.Context, pageID, pageToken, commentID, text string, buttons []Button) error {
	return c.postJSON(ctx, pageID+"/messages", pageToken, map[string]any{
		"recipient": map[string]string{"comment_id": commentID},
		"message":   buttonTemplate(text, buttons),
	}, nil)
}

// SendText messages a person who wrote to the Page (inside the 24 h window).
func (c *Client) SendText(ctx context.Context, pageID, pageToken, psid, text string) error {
	return c.postJSON(ctx, pageID+"/messages", pageToken, map[string]any{
		"recipient": map[string]string{"id": psid}, "messaging_type": "RESPONSE",
		"message": map[string]string{"text": text},
	}, nil)
}

// SendButtons is SendText with Sí/No-style buttons.
func (c *Client) SendButtons(ctx context.Context, pageID, pageToken, psid, text string, buttons []Button) error {
	return c.postJSON(ctx, pageID+"/messages", pageToken, map[string]any{
		"recipient": map[string]string{"id": psid}, "messaging_type": "RESPONSE",
		"message": buttonTemplate(text, buttons),
	}, nil)
}

// ProfileName is a Messenger user's name (for buyers whose comment didn't
// carry it).
func (c *Client) ProfileName(ctx context.Context, psid, pageToken string) string {
	var p struct {
		First string `json:"first_name"`
		Last  string `json:"last_name"`
	}
	if err := c.get(ctx, psid, url.Values{"access_token": {pageToken}, "fields": {"first_name,last_name"}}, &p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.First + " " + p.Last)
}

// --- Posts -----------------------------------------------------------------

// PostPhoto publishes one photo with its caption on the Page; it returns
// the post id ("<page>_<post>"), the one comments' webhooks carry.
func (c *Client) PostPhoto(ctx context.Context, pageID, pageToken string, image []byte, filename, caption string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("caption", caption)
	_ = w.WriteField("published", "true")
	fw, err := w.CreateFormFile("source", filename)
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(image); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	var res struct {
		ID     string `json:"id"`
		PostID string `json:"post_id"`
	}
	if err := c.do(ctx, http.MethodPost, pageID+"/photos", url.Values{"access_token": {pageToken}}, &buf, w.FormDataContentType(), &res); err != nil {
		return "", err
	}
	if res.PostID != "" {
		return res.PostID, nil
	}
	return pageID + "_" + res.ID, nil
}

// DeletePost removes a post (the item sold).
func (c *Client) DeletePost(ctx context.Context, postID, pageToken string) error {
	return c.do(ctx, http.MethodDelete, postID, url.Values{"access_token": {pageToken}}, nil, "", nil)
}

// --- Webhooks ----------------------------------------------------------------

// VerifySignature checks X-Hub-Signature-256 ("sha256=<hex>") against the
// raw body with the app secret.
func (c *Client) VerifySignature(body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || c.AppSecret == "" {
		return false
	}
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(c.AppSecret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// Webhook is the body Meta posts for Page events.
type Webhook struct {
	Object string  `json:"object"`
	Entry  []Entry `json:"entry"`
}

// Entry is one Page's events.
type Entry struct {
	ID        string      `json:"id"` // the Page id
	Time      int64       `json:"time"`
	Changes   []Change    `json:"changes"`
	Messaging []Messaging `json:"messaging"`
}

// Change is a feed or live_videos event.
type Change struct {
	Field string      `json:"field"`
	Value ChangeValue `json:"value"`
}

// ChangeValue carries the fields AndRho reads from feed and live_videos.
type ChangeValue struct {
	Item      string `json:"item"` // comment, status, photo, video…
	Verb      string `json:"verb"` // add, edited, remove
	PostID    string `json:"post_id"`
	CommentID string `json:"comment_id"`
	ParentID  string `json:"parent_id"`
	Message   string `json:"message"`
	Created   int64  `json:"created_time"`
	From      struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"from"`
	// live_videos
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Messaging is a Messenger event (a message or a button tap).
type Messaging struct {
	Sender struct {
		ID string `json:"id"`
	} `json:"sender"`
	Recipient struct {
		ID string `json:"id"`
	} `json:"recipient"`
	Timestamp int64 `json:"timestamp"`
	Message   *struct {
		MID        string `json:"mid"`
		Text       string `json:"text"`
		IsEcho     bool   `json:"is_echo"`
		QuickReply *struct {
			Payload string `json:"payload"`
		} `json:"quick_reply"`
	} `json:"message"`
	Postback *struct {
		Title   string `json:"title"`
		Payload string `json:"payload"`
	} `json:"postback"`
}
