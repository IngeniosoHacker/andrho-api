package odoo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SSOTTL is how long an entry token works. It's used once, right away: the
// dashboard opens the ERP with it as soon as the API hands it over.
const SSOTTL = 60 * time.Second

// SSOUser is who enters the ERP from the dashboard.
type SSOUser struct {
	Login string // the AndRho email
	Name  string
	Role  string // owner | admin | editor | viewer
}

// SSOURL is the address that logs u into the ERP at baseURL without an
// Odoo password: andrho_core's /andrho/sso (andrho_odoo repo) checks the
// token with the same secret (the database's andrho.sso_secret) and opens
// the session. The token is
//
//	base64url(payload) "." base64url(HMAC-SHA256(secret, base64url(payload)))
//
// with payload {login, name, role, aud: database, exp, jti}; Odoo accepts
// each jti once.
func SSOURL(baseURL, database, secret string, u SSOUser, now time.Time) string {
	payload, _ := json.Marshal(map[string]any{
		"login": strings.ToLower(strings.TrimSpace(u.Login)),
		"name":  u.Name,
		"role":  u.Role,
		"aud":   database,
		"exp":   now.Add(SSOTTL).Unix(),
		"jti":   uuid.NewString(),
	})
	body := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	token := body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return strings.TrimRight(baseURL, "/") + "/andrho/sso?t=" + url.QueryEscape(token)
}
