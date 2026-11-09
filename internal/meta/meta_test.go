package meta

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("app", "secret", "v27.0", "")
	c.Graph = srv.URL
	return c
}

func TestLoginURL(t *testing.T) {
	c := New("123", "s", "v27.0", "")
	u := c.LoginURL("https://api.andrho.com/meta/callback", "st")
	if !strings.Contains(u, "scope=pages_show_list") || !strings.Contains(u, "client_id=123") || !strings.Contains(u, "state=st") {
		t.Fatal(u)
	}
	c.LoginConfigID = "cfg"
	if u := c.LoginURL("x", "y"); !strings.Contains(u, "config_id=cfg") || strings.Contains(u, "scope=") {
		t.Fatal(u)
	}
	if New("", "", "", "").Enabled() || !c.Enabled() {
		t.Fatal("Enabled")
	}
}

func TestPrivateReplyPayload(t *testing.T) {
	var got map[string]any
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v27.0/PAGE/messages" || r.URL.Query().Get("access_token") != "tok" {
			t.Errorf("path %s %s", r.URL.Path, r.URL.RawQuery)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Write([]byte(`{"recipient_id":"1","message_id":"m"}`))
	})
	err := c.PrivateReply(context.Background(), "PAGE", "tok", "C1", "¿Confirmas?", []Button{{"Sí", "LS_YES:1"}, {"No", "LS_NO:1"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := got["recipient"].(map[string]any)
	tpl := got["message"].(map[string]any)["attachment"].(map[string]any)["payload"].(map[string]any)
	if rec["comment_id"] != "C1" || tpl["template_type"] != "button" || len(tpl["buttons"].([]any)) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestErrors(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		if strings.Contains(r.URL.Path, "expired") {
			w.Write([]byte(`{"error":{"message":"Session has expired","type":"OAuthException","code":190}}`))
			return
		}
		w.Write([]byte(`{"error":{"message":"(#10) nope","type":"OAuthException","code":10}}`))
	})
	err := c.DeletePost(context.Background(), "expired", "t")
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatal(err)
	}
	var me *Error
	if err := c.DeletePost(context.Background(), "x", "t"); !errors.As(err, &me) || me.Code != 10 {
		t.Fatal(err)
	}
}

func TestLiveCommentsAndPages(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/comments"):
			if r.URL.Query().Get("live_filter") != "no_filter" || r.URL.Query().Get("since") != "100" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"data":[{"id":"c1","message":"blusa 1","created_time":"2026-11-09T21:24:05+0000","from":{"id":"u1","name":"Ana"}}]}`))
		case strings.HasSuffix(r.URL.Path, "/me/accounts"):
			w.Write([]byte(`{"data":[{"id":"P1","name":"Tienda","access_token":"pt","tasks":["MESSAGING"],"picture":{"data":{"url":"http://p"}},"instagram_business_account":{"id":"IG1"}}]}`))
		}
	})
	cs, err := c.LiveComments(context.Background(), "L1", "t", time.Unix(100, 0))
	if err != nil || len(cs) != 1 || cs[0].From.Name != "Ana" || cs[0].CreatedTime.IsZero() {
		t.Fatalf("%v %+v", err, cs)
	}
	ps, err := c.Pages(context.Background(), "ut")
	if err != nil || len(ps) != 1 || ps[0].AccessToken != "pt" || ps[0].IGUserID != "IG1" || ps[0].Picture != "http://p" {
		t.Fatalf("%v %+v", err, ps)
	}
}

func TestPostPhoto(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("caption") != "Q85" {
			t.Errorf("form %v", err)
		}
		w.Write([]byte(`{"id":"PH1","post_id":"P1_99"}`))
	})
	id, err := c.PostPhoto(context.Background(), "P1", "t", []byte("img"), "a.jpg", "Q85")
	if err != nil || id != "P1_99" {
		t.Fatal(id, err)
	}
}

func TestVerifySignature(t *testing.T) {
	c := New("a", "secret", "", "")
	body := []byte(`{"object":"page"}`)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !c.VerifySignature(body, good) || c.VerifySignature(body, "sha256=00") || c.VerifySignature(body, "") {
		t.Fatal("signature")
	}
	var wh Webhook
	raw := `{"object":"page","entry":[{"id":"P1","changes":[{"field":"feed","value":{"item":"comment","verb":"add","post_id":"P1_9","comment_id":"9_1","message":"Mío","from":{"id":"u","name":"Ana"}}}],"messaging":[{"sender":{"id":"S"},"recipient":{"id":"P1"},"postback":{"payload":"LS_YES:5"}}]}]}`
	if err := json.Unmarshal([]byte(raw), &wh); err != nil || wh.Entry[0].Changes[0].Value.CommentID != "9_1" || wh.Entry[0].Messaging[0].Postback.Payload != "LS_YES:5" {
		t.Fatalf("%v %+v", err, wh)
	}
}
