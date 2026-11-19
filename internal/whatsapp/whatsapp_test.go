package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEnabled(t *testing.T) {
	if New(false, "tok", "123", "", "").Enabled() {
		t.Fatal("off by flag")
	}
	if New(true, "", "123", "", "").Enabled() {
		t.Fatal("off without token")
	}
	if !New(true, "tok", "123", "", "").Enabled() {
		t.Fatal("on")
	}
	var c *Client
	if c.Enabled() {
		t.Fatal("nil")
	}
}

func TestSendButtonsAndErrors(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["to"] == "50200000000" {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"code":131047,"message":"Re-engagement message"}}`))
			return
		}
		w.Write([]byte(`{"messages":[{"id":"wamid.1"}]}`))
	}))
	defer srv.Close()
	c := New(true, "tok", "PN", "", "v27.0")
	c.Graph = srv.URL
	id, err := c.SendButtons(context.Background(), "+502 5555-5555", "¿Confirmas?", []Button{
		{ID: "a", Title: "Sí, confirmo"}, {ID: "b", Title: "No"}, {ID: "c", Title: "Un título demasiado largo para Meta"}, {ID: "d", Title: "x"},
	})
	if err != nil || id != "wamid.1" {
		t.Fatalf("send: %v %q", err, id)
	}
	if got["to"] != "50255555555" {
		t.Fatalf("to %v", got["to"])
	}
	bs := got["interactive"].(map[string]any)["action"].(map[string]any)["buttons"].([]any)
	if len(bs) != 3 {
		t.Fatalf("buttons %d", len(bs))
	}
	if title := bs[2].(map[string]any)["reply"].(map[string]any)["title"].(string); len([]rune(title)) != 20 {
		t.Fatalf("title not clipped: %q", title)
	}
	_, err = c.SendText(context.Background(), "50200000000", "hola")
	if !errors.Is(err, ErrOutsideWindow) {
		t.Fatalf("want outside window, got %v", err)
	}
}

func TestMedia(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/MEDIA1"):
			w.Write([]byte(`{"url":"` + srv.URL + `/file","mime_type":"image/jpeg","file_size":3}`))
		case r.URL.Path == "/file":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte{1, 2, 3})
		}
	}))
	defer srv.Close()
	c := New(true, "tok", "PN", "", "")
	c.Graph = srv.URL
	b, mime, err := c.Media(context.Background(), "MEDIA1")
	if err != nil || len(b) != 3 || mime != "image/jpeg" {
		t.Fatalf("media: %v %d %q", err, len(b), mime)
	}
}

func TestWebhookMessages(t *testing.T) {
	raw := `{"object":"whatsapp_business_account","entry":[{"id":"WABA","changes":[{"field":"messages","value":{
	  "messaging_product":"whatsapp","metadata":{"display_phone_number":"15550000000","phone_number_id":"PN"},
	  "contacts":[{"wa_id":"50255555555","profile":{"name":"Ana"}}],
	  "messages":[
	    {"from":"50255555555","id":"m1","timestamp":"1763550000","type":"text","text":{"body":"Pedido AR-7F3K"}},
	    {"from":"50255555555","id":"m2","timestamp":"1763550001","type":"image","image":{"id":"IMG","mime_type":"image/jpeg","caption":"Juan Pérez 123"}},
	    {"from":"50255555555","id":"m3","timestamp":"1763550002","type":"interactive","interactive":{"type":"button_reply","button_reply":{"id":"yes","title":"Sí"}}}
	  ]}},{"field":"messages","value":{"metadata":{"phone_number_id":"OTHER"},"messages":[{"from":"1","id":"x","type":"text","text":{"body":"no"}}]}}]}]}`
	var w Webhook
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	ms := w.Messages("PN")
	if len(ms) != 3 {
		t.Fatalf("got %d", len(ms))
	}
	if ms[0].Text != "Pedido AR-7F3K" || ms[0].Name != "Ana" || ms[0].At.Unix() != 1763550000 {
		t.Fatalf("text: %+v", ms[0])
	}
	if ms[1].ImageID != "IMG" || ms[1].Text != "Juan Pérez 123" {
		t.Fatalf("image: %+v", ms[1])
	}
	if ms[2].ButtonID != "yes" {
		t.Fatalf("button: %+v", ms[2])
	}
	if len(w.Messages("")) != 4 {
		t.Fatal("any number")
	}
}

func TestHelpers(t *testing.T) {
	if Digits("+502 5555-5555") != "50255555555" {
		t.Fatal("digits")
	}
	if got := ChatLink("+1 555 000", "Pedido AR-7F3K"); got != "https://wa.me/1555000?text=Pedido+AR-7F3K" {
		t.Fatal(got)
	}
	now := time.Now()
	old := now.Add(-25 * time.Hour)
	recent := now.Add(-time.Hour)
	if InWindow(nil, now) || InWindow(&old, now) || !InWindow(&recent, now) {
		t.Fatal("window")
	}
}
