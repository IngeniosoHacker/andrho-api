package odoo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSSOURL(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	raw := SSOURL("https://ana.erp.andrho.com/", "odoo_ana", "s3cret", SSOUser{Login: " Ana@Panaderia.GT ", Name: "Ana", Role: "owner"}, now)
	u, err := url.Parse(raw)
	if err != nil || u.Host != "ana.erp.andrho.com" || u.Path != "/andrho/sso" {
		t.Fatalf("url = %s", raw)
	}
	body, sig, ok := strings.Cut(u.Query().Get("t"), ".")
	if !ok {
		t.Fatal("token without signature")
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(body))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != sig {
		t.Error("bad signature")
	}
	var p map[string]any
	b, _ := base64.RawURLEncoding.DecodeString(body)
	_ = json.Unmarshal(b, &p)
	if p["login"] != "ana@panaderia.gt" || p["aud"] != "odoo_ana" || p["exp"] != float64(now.Add(SSOTTL).Unix()) || p["jti"] == "" {
		t.Errorf("payload = %v", p)
	}
}
