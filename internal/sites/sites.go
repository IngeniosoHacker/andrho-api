// Package sites gives AndRho clients their website: one Railway service
// per client (site-<account_id>) in the Clients project, served at
// <subdomain>.web.andrho.com through sites-router, which owns the wildcard
// *.web.andrho.com and forwards each host to its client's service over
// Railway's private network (andrho-tracker-dashboard/sites, SITE_MODE=router).
//
// The host comes from SITE_HOST_PATTERN, "{id}" being the subdomain
// (default "{id}.web.andrho.com"). With SITE_WILDCARD=true (the default)
// one wildcard covers every client: nothing per client in DNS or in
// Railway's domains. Creating the client's service needs:
//
//	RAILWAY_API_TOKEN, RAILWAY_CLIENTS_PROJECT_ID, RAILWAY_CLIENTS_ENVIRONMENT_ID
//	SITES_TEMPLATE_REPO (default IngeniosoHacker/andrho-tracker-dashboard),
//	SITES_TEMPLATE_BRANCH (main), SITES_TEMPLATE_ROOT (sites),
//	SITES_API_URL (andrho-api's public URL, for the client's service)
//
// SITE_WILDCARD=false keeps the old per-host setup for a client's own
// domain: Railway's customDomainCreate on sites-router
// (RAILWAY_ROUTER_SERVICE_ID) and the DNS records in Porkbun
// (PORKBUN_API_KEY, PORKBUN_SECRET_KEY, SITE_DNS_ZONE). It's off the normal
// signup path.
package sites

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Config is the sites setup, from the environment.
type Config struct {
	HostPattern string
	Wildcard    bool
	DNSZone     string

	RailwayToken string
	// The Clients project and its production environment, where every
	// client's service lives (sites-router too).
	ClientsProject, ClientsEnv string
	// RouterService is sites-router's id: custom domains (SITE_WILDCARD=false
	// or a client's own domain) are added to it.
	RouterService string
	// What each client's service is created from.
	TemplateRepo, TemplateBranch, TemplateRoot string
	// APIURL is andrho-api's public URL, given to each client's service.
	APIURL string
	// Port the client's services listen on (sites-router forwards to
	// <service>.railway.internal:Port).
	Port int

	PorkbunKey, PorkbunSecret string

	HTTP *http.Client
	// Endpoints, overridable for tests.
	RailwayURL, PorkbunURL string
}

func env(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// FromEnv reads the configuration.
func FromEnv() Config {
	c := Config{
		HostPattern:    env("SITE_HOST_PATTERN"),
		Wildcard:       os.Getenv("SITE_WILDCARD") != "false",
		DNSZone:        env("SITE_DNS_ZONE"),
		RailwayToken:   env("RAILWAY_API_TOKEN"),
		ClientsProject: env("RAILWAY_CLIENTS_PROJECT_ID"),
		ClientsEnv:     env("RAILWAY_CLIENTS_ENVIRONMENT_ID"),
		// RAILWAY_SITES_SERVICE_ID is the old name (the shared sites service).
		RouterService:  env("RAILWAY_ROUTER_SERVICE_ID", "RAILWAY_SITES_SERVICE_ID"),
		TemplateRepo:   env("SITES_TEMPLATE_REPO"),
		TemplateBranch: env("SITES_TEMPLATE_BRANCH"),
		TemplateRoot:   env("SITES_TEMPLATE_ROOT"),
		APIURL:         strings.TrimRight(env("SITES_API_URL"), "/"),
		PorkbunKey:     env("PORKBUN_API_KEY"),
		PorkbunSecret:  env("PORKBUN_SECRET_KEY"),
		RailwayURL:     env("RAILWAY_API_URL"),
		PorkbunURL:     env("PORKBUN_API_URL"),
		Port:           8080,
	}
	if c.HostPattern == "" {
		c.HostPattern = "{id}.web.andrho.com"
	}
	if c.DNSZone == "" {
		c.DNSZone = "andrho.com"
	}
	if c.TemplateRepo == "" {
		c.TemplateRepo = "IngeniosoHacker/andrho-tracker-dashboard"
	}
	if c.TemplateBranch == "" {
		c.TemplateBranch = "main"
	}
	if c.TemplateRoot == "" {
		c.TemplateRoot = "sites"
	}
	if c.RailwayURL == "" {
		c.RailwayURL = "https://backboard.railway.com/graphql/v2"
	}
	if c.PorkbunURL == "" {
		c.PorkbunURL = "https://api.porkbun.com/api/json/v3"
	}
	c.HTTP = &http.Client{Timeout: 20 * time.Second}
	return c
}

// Railway reports whether the client services can be created on their own.
func (c Config) Railway() bool {
	return c.RailwayToken != "" && c.ClientsProject != "" && c.ClientsEnv != ""
}

// Missing says which Railway settings are missing ("" when none).
func (c Config) Missing() string {
	var m []string
	for k, v := range map[string]string{"RAILWAY_API_TOKEN": c.RailwayToken, "RAILWAY_CLIENTS_PROJECT_ID": c.ClientsProject,
		"RAILWAY_CLIENTS_ENVIRONMENT_ID": c.ClientsEnv} {
		if v == "" {
			m = append(m, k)
		}
	}
	if len(m) == 0 {
		return ""
	}
	sortStrings(m)
	return strings.Join(m, ", ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ServiceName is the client's service name: stable, from the account id
// (never the business name, which can change).
func ServiceName(accountID string) string { return "site-" + accountID }

// Reserved names no client can take.
var Reserved = map[string]bool{"www": true, "api": true, "erp": true, "admin": true, "app": true, "web": true,
	"mail": true, "smtp": true, "ftp": true, "andrho": true, "dashboard": true, "panel": true, "static": true, "cdn": true}

// MaxSubdomain is the longest subdomain.
const MaxSubdomain = 40

var dashes = regexp.MustCompile(`-+`)

// Normalize turns a business name into a subdomain: lower case, no
// accents, only [a-z0-9-], at most MaxSubdomain characters. A reserved or
// empty result gets "sitio" in front or instead.
func Normalize(name string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(name)) {
		switch {
		case unicode.Is(unicode.Mn, r): // accents
		case r == 'ñ':
			b.WriteByte('n')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(dashes.ReplaceAllString(b.String(), "-"), "-")
	if len(s) > MaxSubdomain {
		s = strings.TrimRight(s[:MaxSubdomain], "-")
	}
	if s == "" {
		return "sitio"
	}
	if Reserved[s] {
		return Normalize("sitio-" + s)
	}
	return s
}

// Candidates is base and its numbered variants (base-2, base-3, … up to
// n), each within MaxSubdomain.
func Candidates(base string, n int) []string {
	out := []string{base}
	for i := 2; i <= n; i++ {
		suffix := fmt.Sprintf("-%d", i)
		b := base
		if len(b)+len(suffix) > MaxSubdomain {
			b = strings.TrimRight(b[:MaxSubdomain-len(suffix)], "-")
		}
		out = append(out, b+suffix)
	}
	return out
}

// Host is the site host for a site_id.
func (c Config) Host(siteID string) (string, error) {
	if !idRe.MatchString(siteID) {
		return "", fmt.Errorf("sites: invalid site id %q", siteID)
	}
	return strings.ReplaceAll(c.HostPattern, "{id}", siteID), nil
}

// DNSRecord is one record Railway needs for a custom domain.
type DNSRecord struct {
	Type, Name, Value string // Name is relative to DNSZone ("web.acme")
}

// Setup makes host reach sites-router by itself: a custom domain on Railway
// and its DNS records in Porkbun. Only for SITE_WILDCARD=false or a
// client's own domain; with the wildcard there's nothing to do per host.
// It returns a short state for staff ("listo", or what's missing/failed);
// an error only for a failure worth retrying.
func (c Config) Setup(ctx context.Context, host string) (string, error) {
	if c.Wildcard {
		return "wildcard", nil
	}
	if c.RailwayToken == "" || c.RouterService == "" || c.ClientsEnv == "" {
		return "pendiente: agregar " + host + " como dominio de sites-router en Railway (faltan RAILWAY_API_TOKEN, RAILWAY_ROUTER_SERVICE_ID o RAILWAY_CLIENTS_ENVIRONMENT_ID)", nil
	}
	records, err := c.railwayDomain(ctx, host)
	if err != nil {
		return "", err
	}
	if c.PorkbunKey == "" || c.PorkbunSecret == "" {
		parts := []string{}
		for _, r := range records {
			parts = append(parts, r.Type+" "+r.Name+" → "+r.Value)
		}
		return "pendiente DNS: crear en Porkbun " + strings.Join(parts, "; ") + " (faltan PORKBUN_API_KEY o PORKBUN_SECRET_KEY)", nil
	}
	for _, r := range records {
		if err := c.porkbunRecord(ctx, r); err != nil {
			return "", err
		}
	}
	return "listo: dominio en Railway y DNS en Porkbun", nil
}

func (c Config) post(ctx context.Context, url string, headers map[string]string, body, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

const customDomainCreate = `mutation($input: CustomDomainCreateInput!) {
  customDomainCreate(input: $input) {
    id
    domain
    status { dnsRecords { recordType hostlabel fqdn requiredValue } }
  }
}`

func (c Config) railwayDomain(ctx context.Context, host string) ([]DNSRecord, error) {
	var out struct {
		Data struct {
			CustomDomainCreate struct {
				Status struct {
					DNSRecords []struct {
						RecordType    string `json:"recordType"`
						Hostlabel     string `json:"hostlabel"`
						FQDN          string `json:"fqdn"`
						RequiredValue string `json:"requiredValue"`
					} `json:"dnsRecords"`
				} `json:"status"`
			} `json:"customDomainCreate"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	err := c.post(ctx, c.RailwayURL, map[string]string{"Authorization": "Bearer " + c.RailwayToken},
		map[string]any{"query": customDomainCreate, "variables": map[string]any{"input": map[string]any{
			"domain": host, "serviceId": c.RouterService, "environmentId": c.ClientsEnv}}}, &out)
	if err != nil {
		return nil, fmt.Errorf("sites: railway: %w", err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("sites: railway: %s", out.Errors[0].Message)
	}
	zone := "." + c.DNSZone
	recs := []DNSRecord{}
	for _, r := range out.Data.CustomDomainCreate.Status.DNSRecords {
		name := strings.TrimSuffix(strings.TrimSuffix(r.FQDN, "."), zone)
		if name == "" || name == r.FQDN {
			name = r.Hostlabel
		}
		typ := strings.TrimPrefix(strings.ToUpper(r.RecordType), "DNS_RECORD_TYPE_")
		if name == "" || r.RequiredValue == "" || (typ != "CNAME" && typ != "TXT" && typ != "A") {
			continue
		}
		recs = append(recs, DNSRecord{Type: typ, Name: name, Value: r.RequiredValue})
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("sites: railway returned no DNS records for %s", host)
	}
	return recs, nil
}

func (c Config) porkbunRecord(ctx context.Context, r DNSRecord) error {
	var out struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	err := c.post(ctx, c.PorkbunURL+"/dns/create/"+c.DNSZone, nil, map[string]string{
		"apikey": c.PorkbunKey, "secretapikey": c.PorkbunSecret,
		"name": r.Name, "type": r.Type, "content": strings.TrimSuffix(r.Value, "."), "ttl": "600",
	}, &out)
	if err != nil {
		return fmt.Errorf("sites: porkbun %s %s: %w", r.Type, r.Name, err)
	}
	if out.Status != "SUCCESS" {
		return fmt.Errorf("sites: porkbun %s %s: %s", r.Type, r.Name, out.Message)
	}
	return nil
}
