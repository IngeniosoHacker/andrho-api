package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// Pusher sends Web Push notifications with the VAPID keys in
// VAPID_PUBLIC_KEY / VAPID_PRIVATE_KEY (VAPID_SUBJECT: who to contact,
// default mailto:info@andrho.com).
type Pusher struct {
	Public, Private, Subject string
}

// PusherFromEnv reads the VAPID keys.
func PusherFromEnv() *Pusher {
	p := &Pusher{Public: os.Getenv("VAPID_PUBLIC_KEY"), Private: os.Getenv("VAPID_PRIVATE_KEY"), Subject: os.Getenv("VAPID_SUBJECT")}
	if p.Subject == "" {
		p.Subject = "mailto:info@andrho.com"
	}
	return p
}

// Enabled reports whether the keys are set.
func (p *Pusher) Enabled() bool { return p != nil && p.Public != "" && p.Private != "" }

// Message is what the service worker (web/public/sw.js) shows.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}

// Send pushes msg to one subscription. ok=false (no error) means the
// subscription is gone (404/410) and should be deleted.
func (p *Pusher) Send(ctx context.Context, s db.PushSubscription, msg Message) (bool, error) {
	body, _ := json.Marshal(msg)
	res, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
		Endpoint: s.Endpoint, Keys: webpush.Keys{P256dh: s.P256dh, Auth: s.Auth},
	}, &webpush.Options{Subscriber: p.Subject, VAPIDPublicKey: p.Public, VAPIDPrivateKey: p.Private, TTL: 24 * 3600, Urgency: webpush.UrgencyHigh})
	if err != nil {
		return true, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return false, nil
	case res.StatusCode >= 300:
		return true, fmt.Errorf("notify: push %d", res.StatusCode)
	}
	return true, nil
}

// Notifier tells an account's people. Either channel may be off.
type Notifier struct {
	DB           *pgxpool.Pool
	Mail         *Mailer
	Push         *Pusher
	DashboardURL string
}

// FromEnv builds a Notifier from SMTP_* and VAPID_*.
func FromEnv(pool *pgxpool.Pool, dashboardURL string) *Notifier {
	return &Notifier{DB: pool, Mail: MailerFromEnv(), Push: PusherFromEnv(), DashboardURL: dashboardURL}
}

// pushAccount sends msg to every browser of the account's people.
func (n *Notifier) pushAccount(ctx context.Context, accountID string, msg Message) int {
	if n == nil || !n.Push.Enabled() {
		return 0
	}
	subs, err := db.ListPushSubscriptions(ctx, n.DB, accountID)
	if err != nil {
		return 0
	}
	sent := 0
	for _, s := range subs {
		alive, err := n.Push.Send(ctx, s, msg)
		if !alive {
			_ = db.DeletePushSubscription(ctx, n.DB, s.UserID, s.Endpoint)
			continue
		}
		if err != nil {
			log.Printf("notify: push %s: %v", accountID, err)
			continue
		}
		sent++
	}
	return sent
}

// ERPReady tells the account its ERP is ready: a push to every browser that
// asked, and an email to the owner.
func (n *Notifier) ERPReady(ctx context.Context, accountID, company, ownerEmail, ownerName string) {
	if n == nil {
		return
	}
	url := n.DashboardURL
	if url == "" {
		url = "https://andrho.com/dashboard/"
	}
	n.pushAccount(ctx, accountID, Message{Title: "Tu ERP está listo", Body: "AndRho terminó de preparar el ERP de " + company + ". Entra y empieza.",
		URL: "/dashboard/#/inicio", Tag: "erp-ready"})
	if !n.Mail.Enabled() || ownerEmail == "" {
		return
	}
	first := ownerName
	if i := indexSpace(first); i > 0 {
		first = first[:i]
	}
	hi := "Hola"
	if first != "" {
		hi += " " + first
	}
	e := Email{
		Subject: "Tu ERP está listo",
		Title:   "Tu ERP ya está listo",
		Paragraph: []string{
			hi + ":",
			"AndRho terminó de preparar el ERP de " + company + ", con los módulos que elegiste.",
			"Ya puedes entrar a tu panel: ahí ves tus ventas, tu inventario y lo que AndRho te sugiere para tu negocio.",
		},
		Button: "Ir a mi panel",
		URL:    url,
		Footer: "Recibes este correo porque creaste tu cuenta en AndRho. Si no fuiste tú, respóndenos y lo revisamos.",
	}
	text, page := e.Render()
	if err := n.Mail.Send(ownerEmail, e.Subject, text, page); err != nil {
		log.Printf("notify: mail %s: %v", accountID, err)
	}
}

func indexSpace(s string) int {
	for i, r := range s {
		if r == ' ' {
			return i
		}
	}
	return -1
}
