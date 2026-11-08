// Package notify tells people when something AndRho was doing for them is
// ready (today: their ERP after signup), by email (info@andrho.com, on the
// mailcow of AndRho's server) and by Web Push to the browsers they allowed.
// Both are optional: without SMTP_* or VAPID_* configured, that channel is
// skipped and nothing else changes.
package notify

import (
	"crypto/tls"
	"fmt"
	"html"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Mailer sends email over SMTP with STARTTLS (mailcow's submission port).
type Mailer struct {
	Host, Port, User, Password string
	From                       string // "AndRho <info@andrho.com>"
}

// MailerFromEnv reads SMTP_HOST (default mail.andrho.com), SMTP_PORT (587),
// SMTP_USER, SMTP_PASSWORD and SMTP_FROM (default "AndRho <SMTP_USER>").
func MailerFromEnv() *Mailer {
	m := &Mailer{Host: os.Getenv("SMTP_HOST"), Port: os.Getenv("SMTP_PORT"), User: os.Getenv("SMTP_USER"),
		Password: os.Getenv("SMTP_PASSWORD"), From: os.Getenv("SMTP_FROM")}
	if m.Host == "" {
		m.Host = "mail.andrho.com"
	}
	if m.Port == "" {
		m.Port = "587"
	}
	if m.From == "" && m.User != "" {
		m.From = "AndRho <" + m.User + ">"
	}
	return m
}

// Enabled reports whether there is a mailbox to send from.
func (m *Mailer) Enabled() bool { return m != nil && m.User != "" && m.Password != "" }

func addrOf(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		return strings.TrimSuffix(from[i+1:], ">")
	}
	return from
}

// Send sends one message as multipart/alternative (text and HTML).
func (m *Mailer) Send(to, subject, text, htmlBody string) error {
	if !m.Enabled() {
		return fmt.Errorf("notify: SMTP not configured")
	}
	boundary := "andrho-" + uuid.NewString()
	from := addrOf(m.From)
	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", m.From)
	header("To", to)
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+uuid.NewString()+"@"+domainOf(from)+">")
	header("MIME-Version", "1.0")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ ct, body string }{{"text/plain", text}, {"text/html", htmlBody}} {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + part.ct + "; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(strings.ReplaceAll(part.body, "\n", "\r\n") + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(m.Host, m.Port), 15*time.Second)
	if err != nil {
		return fmt.Errorf("notify: smtp dial: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("notify: smtp: %w", err)
	}
	defer c.Close()
	if err := c.StartTLS(&tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}); err != nil {
		return fmt.Errorf("notify: starttls: %w", err)
	}
	if err := c.Auth(smtp.PlainAuth("", m.User, m.Password, m.Host)); err != nil {
		return fmt.Errorf("notify: smtp auth: %w", err)
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("notify: mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("notify: rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: data: %w", err)
	}
	return c.Quit()
}

func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return "andrho.com"
}

// Email is one message to someone, written as paragraphs and a button.
type Email struct {
	Subject   string
	Title     string
	Paragraph []string
	Button    string // label
	URL       string
	Footer    string
}

// The brand of andrho.com (as andrho_mail's template): fonts with
// fallbacks, a 600 px card with a cyan top bar.
const (
	night      = "#0b1020"
	inkSoft    = "#3f4a63"
	muted      = "#64748b"
	border     = "#e2e8f0"
	canvasSoft = "#f8fafc"
	cyan       = "#00b4ff"
	mint       = "#00e6b8"
	fontBody   = "Inter, -apple-system, 'Segoe UI', Roboto, Arial, sans-serif"
	fontHead   = "'Space Grotesk', Inter, Arial, sans-serif"
)

// Render returns the text and HTML bodies.
func (e Email) Render() (string, string) {
	var t, h strings.Builder
	t.WriteString(e.Title + "\n\n")
	for _, p := range e.Paragraph {
		t.WriteString(p + "\n\n")
		h.WriteString(fmt.Sprintf(`<p style="margin:0 0 14px 0;font-family:%s;font-size:15px;line-height:24px;color:%s;">%s</p>`, fontBody, inkSoft, html.EscapeString(p)))
	}
	if e.URL != "" {
		t.WriteString(e.Button + ": " + e.URL + "\n\n")
		h.WriteString(fmt.Sprintf(`<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:22px 0 8px;"><tr><td style="background:%s;border-radius:999px;">`+
			`<a href="%s" style="display:inline-block;padding:13px 26px;font-family:%s;font-size:15px;font-weight:700;color:%s;text-decoration:none;">%s</a></td></tr></table>`,
			mint, html.EscapeString(e.URL), fontBody, night, html.EscapeString(e.Button)))
	}
	if e.Footer != "" {
		t.WriteString("—\n" + e.Footer + "\n")
	}
	page := fmt.Sprintf(`<!DOCTYPE html>
<html lang="es"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head>
<body style="margin:0;padding:0;background:%s;">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" border="0" style="background:%s;">
<tr><td align="center" style="padding:24px 12px;">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:600px;max-width:100%%;background:#ffffff;border:1px solid %s;border-radius:12px;">
<tr><td style="height:4px;line-height:4px;font-size:0;background:%s;border-radius:12px 12px 0 0;">&nbsp;</td></tr>
<tr><td style="padding:28px 32px;">
<p style="margin:0 0 18px 0;font-family:%s;font-size:18px;font-weight:700;color:%s;">AndRho</p>
<h1 style="margin:0 0 16px 0;font-family:%s;font-size:26px;line-height:32px;font-weight:700;color:%s;">%s</h1>
%s
</td></tr>
<tr><td style="padding:16px 32px 24px;border-top:1px solid %s;font-family:%s;font-size:12px;line-height:18px;color:%s;">%s</td></tr>
</table>
</td></tr>
</table>
</body></html>`,
		html.EscapeString(e.Subject), canvasSoft, canvasSoft, border, cyan, fontHead, night, fontHead, night, html.EscapeString(e.Title),
		h.String(), border, fontBody, muted, html.EscapeString(e.Footer))
	return t.String(), page
}
