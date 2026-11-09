package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/locate"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// installedModels caches, per account, which ERP models exist, so the
// search bar only offers apps the business has (10 minutes).
var installedModels sync.Map // account id -> modelCache

type modelCache struct {
	at     time.Time
	models map[string]bool
}

func (h *Handler) erpHas(ctx context.Context, accountID string, cl *odoo.Client) func(string) bool {
	if v, ok := installedModels.Load(accountID); ok {
		mc := v.(modelCache)
		if time.Since(mc.at) < 10*time.Minute {
			return func(m string) bool { return mc.models[m] }
		}
	}
	wanted := []any{}
	for _, p := range locate.Places {
		if p.Model != "" {
			wanted = append(wanted, p.Model)
		}
	}
	var rows []map[string]any
	models := map[string]bool{}
	if err := cl.SearchRead(ctx, "ir.model", []any{[]any{"model", "in", wanted}}, []string{"model"}, "", 0, &rows); err == nil {
		for _, r := range rows {
			models[odoo.Str(r["model"])] = true
		}
		installedModels.Store(accountID, modelCache{at: time.Now(), models: models})
	} else {
		return nil // unknown: offer everything
	}
	return func(m string) bool { return models[m] }
}

// ERPLocate handles GET /erp/locate?q=: the dashboard's search bar (like
// `locate`): where in AndRho or the ERP something is.
func (h *Handler) ERPLocate(c *gin.Context) {
	claims, _ := currentClaims(c)
	q := strings.TrimSpace(c.Query("q"))
	if len(q) > 80 {
		q = q[:80]
	}
	var has func(string) bool
	erp := false
	if cl, err := h.odooFor(c.Request.Context(), claims.AccountID); err == nil {
		erp = true
		has = h.erpHas(c.Request.Context(), claims.AccountID, cl)
	}
	results := locate.Search(q, has, 8)
	if !erp {
		kept := results[:0]
		for _, r := range results {
			if r.Where == "dashboard" {
				kept = append(kept, r)
			}
		}
		results = kept
	}
	c.JSON(http.StatusOK, gin.H{"results": results, "erp_connected": erp})
}

// ssoTo answers {url} for the ERP at next (an /odoo/... or /report/...
// path), with the dashboard's one-time entry when the ERP is AndRho's.
func (h *Handler) ssoTo(c *gin.Context, next string) {
	claims, _ := currentClaims(c)
	ctx := c.Request.Context()
	if next != "" && !locate.ValidERPPath(next) {
		respondError(c, http.StatusBadRequest, "ruta inválida")
		return
	}
	conn, err := db.GetOdooConnection(ctx, h.Accounts, claims.AccountID)
	if errors.Is(err, db.ErrNotFound) {
		respondError(c, http.StatusNotFound, "erp_not_connected")
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if len(conn.SSOSecret) == 0 {
		c.JSON(http.StatusOK, gin.H{"url": strings.TrimRight(conn.URL, "/") + next, "sso": false})
		return
	}
	secret, err := h.Secrets.Open(conn.SSOSecret)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	name := claims.Email
	if u, err := db.GetUserByID(ctx, h.Accounts, claims.Sub); err == nil && u.DisplayName != "" {
		name = u.DisplayName
	}
	url := odoo.SSOURL(conn.URL, conn.DBName, secret, odoo.SSOUser{Login: claims.Email, Name: name, Role: claims.Role, Next: next}, time.Now())
	c.JSON(http.StatusOK, gin.H{"url": url, "sso": true})
}
