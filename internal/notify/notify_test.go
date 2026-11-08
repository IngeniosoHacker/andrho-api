package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

func TestEmailRender(t *testing.T) {
	e := Email{Subject: "Tu ERP está listo", Title: "Tu ERP ya está listo", Paragraph: []string{"Hola Ana:", "Pan <de> Ana"},
		Button: "Ir a mi panel", URL: "https://andrho.com/dashboard/", Footer: "pie"}
	text, page := e.Render()
	if !strings.Contains(text, "Ir a mi panel: https://andrho.com/dashboard/") || !strings.Contains(text, "Pan <de> Ana") {
		t.Fatalf("text:\n%s", text)
	}
	for _, want := range []string{`href="https://andrho.com/dashboard/"`, "Pan &lt;de&gt; Ana", "#00b4ff", "Tu ERP ya está listo", "Ir a mi panel"} {
		if !strings.Contains(page, want) {
			t.Errorf("html misses %q", want)
		}
	}
}

func TestMailerDisabledWithoutCredentials(t *testing.T) {
	t.Setenv("SMTP_USER", "")
	if MailerFromEnv().Enabled() {
		t.Fatal("enabled without a mailbox")
	}
	t.Setenv("SMTP_USER", "info@andrho.com")
	t.Setenv("SMTP_PASSWORD", "x")
	m := MailerFromEnv()
	if !m.Enabled() || m.Host != "mail.andrho.com" || m.Port != "587" || m.From != "AndRho <info@andrho.com>" {
		t.Fatalf("%+v", m)
	}
}

func subscription(t *testing.T, endpoint string) db.PushSubscription {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return db.PushSubscription{Endpoint: endpoint, P256dh: base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth)}
}

func TestPushSendAndGone(t *testing.T) {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	var gotAuth, gotEncoding string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotEncoding = r.Header.Get("Authorization"), r.Header.Get("Content-Encoding")
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	p := &Pusher{Public: pub, Private: priv, Subject: "mailto:info@andrho.com"}
	alive, err := p.Send(context.Background(), subscription(t, srv.URL+"/ok"), Message{Title: "Tu ERP está listo"})
	if err != nil || !alive || !strings.HasPrefix(gotAuth, "vapid ") || gotEncoding != "aes128gcm" {
		t.Fatalf("send: %v %v auth=%q enc=%q", alive, err, gotAuth, gotEncoding)
	}
	alive, err = p.Send(context.Background(), subscription(t, srv.URL+"/gone"), Message{Title: "x"})
	if err != nil || alive {
		t.Fatalf("gone: %v %v", alive, err)
	}
}

func TestNilNotifierIsQuiet(t *testing.T) {
	var n *Notifier
	n.ERPReady(context.Background(), "a", "b", "c", "d") // no panic
}
