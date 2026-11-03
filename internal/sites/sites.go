// Package sites gives every account without a website its AndRho site on
// Railway: the sites service (andrho-tracker-dashboard/sites) answers on
// the account's host with an "en construcción" page built from its data,
// until the Creator's site is published there.
//
// The host comes from SITE_HOST_PATTERN, "{id}" being the site_id
// (default "web.{id}.andrho.com"). Each host must be a custom domain of the
// sites service on Railway and resolve to it. With the credentials below,
// Setup does both on its own; without them it only reports what's missing
// and staff adds the domain by hand:
//
//	RAILWAY_API_TOKEN, RAILWAY_SITES_SERVICE_ID, RAILWAY_ENVIRONMENT_ID
//	    → Railway's customDomainCreate on the sites service
//	PORKBUN_API_KEY, PORKBUN_SECRET_KEY (+ SITE_DNS_ZONE, default andrho.com)
//	    → the DNS records Railway asks for (CNAME and verification TXT)
//
// A pattern a single wildcard covers ("{id}.web.andrho.com" with
// *.web.andrho.com on Railway and in DNS) needs neither: set
// SITE_WILDCARD=true. "web.{id}.andrho.com" is two labels deep, which no
// wildcard certificate covers, so it needs the per-client setup.
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
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Config is the sites setup, from the environment.
type Config struct {
	HostPattern string
	Wildcard    bool
	DNSZone     string

	RailwayToken, RailwayService, RailwayEnv string
	PorkbunKey, PorkbunSecret                string

	HTTP *http.Client
	// Endpoints, overridable for tests.
	RailwayURL, PorkbunURL string
}

// FromEnv reads the configuration.
func FromEnv() Config {
	c := Config{
		HostPattern:    os.Getenv("SITE_HOST_PATTERN"),
		Wildcard:       os.Getenv("SITE_WILDCARD") == "true",
		DNSZone:        os.Getenv("SITE_DNS_ZONE"),
		RailwayToken:   os.Getenv("RAILWAY_API_TOKEN"),
		RailwayService: os.Getenv("RAILWAY_SITES_SERVICE_ID"),
		RailwayEnv:     os.Getenv("RAILWAY_ENVIRONMENT_ID"),
		PorkbunKey:     os.Getenv("PORKBUN_API_KEY"),
		PorkbunSecret:  os.Getenv("PORKBUN_SECRET_KEY"),
		RailwayURL:     os.Getenv("RAILWAY_API_URL"),
		PorkbunURL:     os.Getenv("PORKBUN_API_URL"),
	}
	if c.HostPattern == "" {
		c.HostPattern = "web.{id}.andrho.com"
	}
	if c.DNSZone == "" {
		c.DNSZone = "andrho.com"
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

// Setup makes host reach the sites service. It returns a short state for
// staff ("listo", or what's missing/failed); an error only for a failure
// worth retrying.
func (c Config) Setup(ctx context.Context, host string) (string, error) {
	if c.Wildcard {
		return "listo: cubierto por el comodín", nil
	}
	if c.RailwayToken == "" || c.RailwayService == "" || c.RailwayEnv == "" {
		return "pendiente: agregar " + host + " como dominio del servicio de sitios en Railway (faltan RAILWAY_API_TOKEN, RAILWAY_SITES_SERVICE_ID o RAILWAY_ENVIRONMENT_ID)", nil
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
			"domain": host, "serviceId": c.RailwayService, "environmentId": c.RailwayEnv}}}, &out)
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
