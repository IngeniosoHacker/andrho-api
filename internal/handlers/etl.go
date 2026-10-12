package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/etl"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

type importRequest struct {
	Kind     string              `json:"kind"` // products | customers
	Filename string              `json:"filename"`
	DryRun   bool                `json:"dry_run"`
	Rows     []map[string]string `json:"rows"` // already mapped to etl.Fields
}

// ImportERP handles POST /erp/import (editor+): a migration from the
// business' previous system. dry_run validates and previews without
// writing; otherwise the clean rows are loaded into Odoo.
func (h *Handler) ImportERP(c *gin.Context) {
	claims, _ := currentClaims(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20)
	var req importRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, ok := etl.Fields[req.Kind]; !ok {
		respondError(c, http.StatusBadRequest, "kind debe ser products o customers")
		return
	}
	if len(req.Rows) == 0 || len(req.Rows) > etl.MaxRows {
		respondError(c, http.StatusBadRequest, "entre 1 y "+strconv.Itoa(etl.MaxRows)+" filas por migración")
		return
	}
	if len(req.Filename) > 200 {
		req.Filename = req.Filename[:200]
	}

	recs, issues := etl.Transform(req.Kind, req.Rows)
	rep := etl.Report{Kind: req.Kind, DryRun: req.DryRun, Rows: len(req.Rows), Valid: len(recs), Issues: issues, Preview: []map[string]any{}}
	for i, r := range recs {
		if i == 10 {
			break
		}
		p := map[string]any{}
		for k, v := range r.Values {
			p[k] = v
		}
		for k, v := range r.Nums {
			p[k] = v
		}
		rep.Preview = append(rep.Preview, p)
	}

	h.erp(c, func(ctx context.Context, cl *odoo.Client) (any, error) {
		if !req.DryRun {
			// The load outlives a slow connection: detach it, with a cap.
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			if err := etl.Load(lctx, cl, req.Kind, recs, &rep); err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(rep)
		_, _ = h.Accounts.Exec(ctx, `INSERT INTO etl_runs (id, account_id, user_id, kind, dry_run, filename, report) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			uuid.NewString(), claims.AccountID, claims.Sub, req.Kind, req.DryRun, req.Filename, raw)
		if !req.DryRun {
			what := map[string]string{"products": "productos", "customers": "clientes"}[req.Kind]
			_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "erp_import",
				"Migración de "+what+": "+strconv.Itoa(rep.Created)+" nuevos, "+strconv.Itoa(rep.Updated)+" actualizados")
		}
		return rep, nil
	})
}

// ListImports handles GET /erp/imports: the account's latest migrations.
func (h *Handler) ListImports(c *gin.Context) {
	claims, _ := currentClaims(c)
	rows, err := h.Accounts.Query(c.Request.Context(),
		`SELECT kind, dry_run, filename, report, created_at FROM etl_runs WHERE account_id = $1 ORDER BY created_at DESC LIMIT 30`, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []gin.H{}
	for rows.Next() {
		var kind, filename string
		var dry bool
		var report json.RawMessage
		var created time.Time
		if err := rows.Scan(&kind, &dry, &filename, &report, &created); err != nil {
			respondError(c, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, gin.H{"kind": kind, "dry_run": dry, "filename": filename, "report": report, "created_at": created})
	}
	c.JSON(http.StatusOK, gin.H{"imports": out})
}
