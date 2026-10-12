// Package odoo talks to an account's Odoo over JSON-RPC (/jsonrpc), the
// same API Odoo's own clients use. One Client per account connection:
// it logs in once and reuses the uid.
package odoo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrAuth means Odoo rejected the login.
var ErrAuth = errors.New("odoo: invalid credentials")

// Client is one Odoo database with one user.
type Client struct {
	URL, DB, Login, Password string
	http                     *http.Client
	mu                       sync.Mutex
	uid                      int
}

// New builds a client; nothing is called until the first request.
func New(rawURL, db, login, password string) *Client {
	return &Client{URL: strings.TrimRight(rawURL, "/"), DB: db, Login: login, Password: password,
		http: &http.Client{Timeout: 20 * time.Second}}
}

type rpcError struct {
	Message string `json:"message"`
	Data    struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"data"`
}

func (c *Client) rpc(ctx context.Context, service, method string, args []any, out any) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "call", "id": 1,
		"params": map[string]any{"service": service, "method": method, "args": args}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/jsonrpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("odoo: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("odoo: bad response (%d)", res.StatusCode)
	}
	if env.Error != nil {
		if strings.Contains(env.Error.Data.Name, "AccessDenied") {
			return ErrAuth
		}
		msg := env.Error.Data.Message
		if msg == "" {
			msg = env.Error.Message
		}
		return fmt.Errorf("odoo: %s", msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

// Authenticate logs in and caches the uid.
func (c *Client) Authenticate(ctx context.Context) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.uid > 0 {
		return c.uid, nil
	}
	var uid any
	if err := c.rpc(ctx, "common", "login", []any{c.DB, c.Login, c.Password}, &uid); err != nil {
		return 0, err
	}
	n, ok := uid.(float64)
	if !ok || n <= 0 {
		return 0, ErrAuth
	}
	c.uid = int(n)
	return c.uid, nil
}

// Exec calls model.method(args, kwargs) and decodes the result into out.
func (c *Client) Exec(ctx context.Context, model, method string, args []any, kwargs map[string]any, out any) error {
	uid, err := c.Authenticate(ctx)
	if err != nil {
		return err
	}
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	return c.rpc(ctx, "object", "execute_kw", []any{c.DB, uid, c.Password, model, method, args, kwargs}, out)
}

// SearchRead is model.search_read(domain, fields, ...) into out (a slice).
func (c *Client) SearchRead(ctx context.Context, model string, domain []any, fields []string, order string, limit int, out any) error {
	kw := map[string]any{"fields": fields}
	if order != "" {
		kw["order"] = order
	}
	if limit > 0 {
		kw["limit"] = limit
	}
	if domain == nil {
		domain = []any{}
	}
	return c.Exec(ctx, model, "search_read", []any{domain}, kw, out)
}

// HasModel reports whether model exists (its module is installed).
func (c *Client) HasModel(ctx context.Context, model string) bool {
	var n int
	err := c.Exec(ctx, "ir.model", "search_count", []any{[]any{[]any{"model", "=", model}}}, nil, &n)
	return err == nil && n > 0
}

// CheckURL rejects what isn't an http(s) URL on a public address, so a
// customer-entered Odoo URL can't make the API reach internal services.
// allowPrivate skips the address check (development).
func CheckURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return errors.New("la URL debe ser http(s)://dominio")
	}
	if allowPrivate {
		return nil
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return errors.New("no encontramos ese dominio")
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("la URL debe ser pública")
		}
	}
	return nil
}

// M2O reads a many2one value ([id, "name"] or false) as its name.
func M2O(v any) string {
	if a, ok := v.([]any); ok && len(a) == 2 {
		if s, ok := a[1].(string); ok {
			return s
		}
	}
	return ""
}

// Str reads a char field (false when empty) as a string.
func Str(v any) string {
	s, _ := v.(string)
	return s
}

// Num reads a numeric field.
func Num(v any) float64 {
	f, _ := v.(float64)
	return f
}
