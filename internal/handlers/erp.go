package handlers

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

// The dashboard's ERP sections (Inventario, Clientes, Prospectos,
// Sucursales and the history in Resumen) read the account's Odoo: the one
// AndRho provisioned at signup, or one the business already had and
// connected (POST /account/erp). Every /erp/* answer is 404
// {"error":"erp_not_connected"} until there's a connection.

var odooClients sync.Map // account id -> *odoo.Client

type row = map[string]any

func (h *Handler) odooFor(ctx context.Context, accountID string) (*odoo.Client, error) {
	if c, ok := odooClients.Load(accountID); ok {
		return c.(*odoo.Client), nil
	}
	conn, err := db.GetOdooConnection(ctx, h.Accounts, accountID)
	if err != nil {
		return nil, err
	}
	pw, err := h.Secrets.Open(conn.Secret)
	if err != nil {
		return nil, err
	}
	c := odoo.New(conn.URL, conn.DBName, conn.Login, pw)
	odooClients.Store(accountID, c)
	return c, nil
}

// erp runs fn with the account's Odoo client and answers with its result.
func (h *Handler) erp(c *gin.Context, fn func(ctx context.Context, cl *odoo.Client) (any, error)) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}
	ctx := c.Request.Context()
	cl, err := h.odooFor(ctx, claims.AccountID)
	if errors.Is(err, db.ErrNotFound) {
		respondError(c, http.StatusNotFound, "erp_not_connected")
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	out, err := fn(ctx, cl)
	if err != nil {
		if errors.Is(err, odoo.ErrAuth) {
			odooClients.Delete(claims.AccountID)
			respondError(c, http.StatusBadGateway, "tu ERP rechazó el acceso de AndRho; vuelve a conectarlo")
			return
		}
		respondError(c, http.StatusBadGateway, "no pudimos leer tu ERP: "+err.Error())
		return
	}
	c.JSON(http.StatusOK, out)
}

type connectERPRequest struct {
	URL      string `json:"url"`
	DB       string `json:"db"`
	Login    string `json:"login"`
	Password string `json:"password"`
}

// ConnectERP handles POST /account/erp (admin+): connects an Odoo the
// business already uses. The login is tested before anything is stored.
func (h *Handler) ConnectERP(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req connectERPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.URL, req.DB, req.Login = strings.TrimSpace(req.URL), strings.TrimSpace(req.DB), strings.TrimSpace(req.Login)
	if req.DB == "" || req.Login == "" || req.Password == "" || len(req.URL) > 300 || len(req.DB) > 100 || len(req.Login) > 200 {
		respondError(c, http.StatusBadRequest, "URL, base de datos, usuario y contraseña son obligatorios")
		return
	}
	if err := odoo.CheckURL(req.URL, h.Cfg.OdooAllowPrivate); err != nil {
		respondError(c, http.StatusBadRequest, err.Error())
		return
	}
	ctx := c.Request.Context()
	cl := odoo.New(req.URL, req.DB, req.Login, req.Password)
	if _, err := cl.Authenticate(ctx); err != nil {
		if errors.Is(err, odoo.ErrAuth) {
			respondError(c, http.StatusBadRequest, "Odoo rechazó ese usuario o contraseña")
			return
		}
		respondError(c, http.StatusBadRequest, "no pudimos conectar con ese Odoo")
		return
	}
	sealed, err := h.Secrets.Seal(req.Password)
	if err == nil {
		err = db.SaveOdooConnection(ctx, h.Accounts, db.OdooConnection{AccountID: claims.AccountID, URL: cl.URL, DBName: req.DB, Login: req.Login, Secret: sealed})
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	odooClients.Store(claims.AccountID, cl)
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "erp_connected", "ERP conectado: "+cl.URL)
	c.JSON(http.StatusOK, gin.H{"connected": true, "url": cl.URL, "db": req.DB, "login": req.Login})
}

// ERPStatus handles GET /erp/status.
func (h *Handler) ERPStatus(c *gin.Context) {
	claims, _ := currentClaims(c)
	conn, err := db.GetOdooConnection(c.Request.Context(), h.Accounts, claims.AccountID)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusOK, gin.H{"connected": false})
		return
	}
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"connected": true, "url": conn.URL, "db": conn.DBName, "login": conn.Login})
}

// ERPSSO handles POST /erp/sso: where the dashboard's ERP button goes. For
// the ERP AndRho provisioned it's a one-time address that opens the user's
// session there, with their AndRho email (internal/odoo.SSOURL); for an
// Odoo the business connected itself, just its address.
func (h *Handler) ERPSSO(c *gin.Context) {
	claims, _ := currentClaims(c)
	ctx := c.Request.Context()
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
		c.JSON(http.StatusOK, gin.H{"url": conn.URL, "sso": false})
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
	url := odoo.SSOURL(conn.URL, conn.DBName, secret, odoo.SSOUser{Login: claims.Email, Name: name, Role: claims.Role}, time.Now())
	c.JSON(http.StatusOK, gin.H{"url": url, "sso": true})
}

// ERPInventory handles GET /erp/inventory: products with stock and value,
// the ones running out, and the latest movements.
func (h *Handler) ERPInventory(c *gin.Context) {
	h.erp(c, func(ctx context.Context, cl *odoo.Client) (any, error) {
		var products []row
		if err := cl.SearchRead(ctx, "product.product", []any{[]any{"type", "=", "consu"}, []any{"default_code", "!=", "TIPS"}},
			[]string{"name", "default_code", "qty_available", "list_price", "standard_price", "uom_id", "categ_id"}, "name", 1000, &products); err != nil {
			return nil, err
		}
		var moves []row
		_ = cl.SearchRead(ctx, "stock.move.line", []any{[]any{"state", "=", "done"}},
			[]string{"date", "product_id", "quantity", "location_id", "location_dest_id", "reference"}, "date desc", 60, &moves)

		items := []row{}
		value, out, low := 0.0, 0, 0
		for _, p := range products {
			qty := odoo.Num(p["qty_available"])
			cost := odoo.Num(p["standard_price"])
			value += qty * cost
			status := "ok"
			switch {
			case qty <= 0:
				status, out = "agotado", out+1
			case qty <= 5:
				status, low = "bajo", low+1
			}
			items = append(items, row{"name": p["name"], "code": odoo.Str(p["default_code"]), "qty": qty, "price": odoo.Num(p["list_price"]),
				"cost": cost, "uom": odoo.M2O(p["uom_id"]), "category": odoo.M2O(p["categ_id"]), "status": status})
		}
		sort.SliceStable(items, func(i, j int) bool { return rank(items[i]["status"]) < rank(items[j]["status"]) })
		mv := []row{}
		for _, m := range moves {
			mv = append(mv, row{"date": m["date"], "product": odoo.M2O(m["product_id"]), "qty": odoo.Num(m["quantity"]),
				"from": odoo.M2O(m["location_id"]), "to": odoo.M2O(m["location_dest_id"]), "reference": odoo.Str(m["reference"])})
		}
		return row{"summary": row{"products": len(items), "stock_value": value, "out_of_stock": out, "low_stock": low},
			"products": items, "moves": mv}, nil
	})
}

func rank(status any) int {
	return map[any]int{"agotado": 0, "bajo": 1, "ok": 2}[status]
}

// paidOrders returns the account's point of sale orders, newest first.
func paidOrders(ctx context.Context, cl *odoo.Client, domain []any, limit int) []row {
	var orders []row
	d := append([]any{[]any{"state", "in", []string{"paid", "done", "invoiced"}}}, domain...)
	_ = cl.SearchRead(ctx, "pos.order", d, []string{"name", "date_order", "amount_total", "partner_id", "config_id"}, "date_order desc", limit, &orders)
	return orders
}

// ERPCustomers handles GET /erp/customers: who bought, how much, how often
// and since when they haven't come back.
func (h *Handler) ERPCustomers(c *gin.Context) {
	h.erp(c, func(ctx context.Context, cl *odoo.Client) (any, error) {
		orders := paidOrders(ctx, cl, nil, 5000)
		type agg struct {
			name        string
			total       float64
			count       int
			first, last string
		}
		by := map[string]*agg{}
		for _, o := range orders {
			p := odoo.M2O(o["partner_id"])
			if p == "" {
				continue
			}
			a := by[p]
			if a == nil {
				a = &agg{name: p, first: odoo.Str(o["date_order"]), last: odoo.Str(o["date_order"])}
				by[p] = a
			}
			a.total += odoo.Num(o["amount_total"])
			a.count++
			d := odoo.Str(o["date_order"])
			if d < a.first {
				a.first = d
			}
			if d > a.last {
				a.last = d
			}
		}
		var partners []row
		if err := cl.SearchRead(ctx, "res.partner", []any{[]any{"customer_rank", ">", 0}},
			[]string{"name", "email", "phone", "create_date"}, "name", 2000, &partners); err != nil {
			return nil, err
		}
		contact := map[string]row{}
		for _, p := range partners {
			contact[odoo.Str(p["name"])] = p
		}
		list := []row{}
		now := time.Now()
		for _, a := range by {
			days := 0
			if t, err := time.Parse("2006-01-02 15:04:05", a.last); err == nil {
				days = int(now.Sub(t).Hours() / 24)
			}
			ct := contact[a.name]
			list = append(list, row{"name": a.name, "email": odoo.Str(ct["email"]), "phone": odoo.Str(ct["phone"]), "total": a.total,
				"orders": a.count, "average": a.total / float64(a.count), "first_order": a.first, "last_order": a.last, "days_since": days,
				// A regular who stopped coming: more than three orders, more than 30 days away.
				"at_risk": a.count >= 3 && days > 30})
		}
		sort.Slice(list, func(i, j int) bool { return list[i]["total"].(float64) > list[j]["total"].(float64) })
		revenue := 0.0
		for _, o := range orders {
			revenue += odoo.Num(o["amount_total"])
		}
		return row{"summary": row{"customers": len(list), "orders": len(orders), "revenue": revenue}, "customers": list}, nil
	})
}

// ERPLeads handles GET /erp/leads: people who left their details and
// haven't bought. CRM leads when the CRM is installed; otherwise contacts
// without orders.
func (h *Handler) ERPLeads(c *gin.Context) {
	h.erp(c, func(ctx context.Context, cl *odoo.Client) (any, error) {
		if cl.HasModel(ctx, "crm.lead") {
			var leads []row
			if err := cl.SearchRead(ctx, "crm.lead", []any{[]any{"active", "=", true}},
				[]string{"name", "partner_name", "contact_name", "email_from", "phone", "stage_id", "expected_revenue", "probability", "create_date", "type"},
				"create_date desc", 500, &leads); err != nil {
				return nil, err
			}
			out := []row{}
			for _, l := range leads {
				out = append(out, row{"name": l["name"], "company": odoo.Str(l["partner_name"]), "contact": odoo.Str(l["contact_name"]),
					"email": odoo.Str(l["email_from"]), "phone": odoo.Str(l["phone"]), "stage": odoo.M2O(l["stage_id"]),
					"expected": odoo.Num(l["expected_revenue"]), "probability": odoo.Num(l["probability"]), "created": l["create_date"]})
			}
			return row{"source": "crm", "leads": out}, nil
		}
		var partners []row
		if err := cl.SearchRead(ctx, "res.partner", []any{[]any{"customer_rank", "=", 0}, []any{"is_company", "=", false}, []any{"user_ids", "=", false}},
			[]string{"name", "email", "phone", "create_date"}, "create_date desc", 500, &partners); err != nil {
			return nil, err
		}
		out := []row{}
		for _, p := range partners {
			out = append(out, row{"name": p["name"], "contact": p["name"], "email": odoo.Str(p["email"]), "phone": odoo.Str(p["phone"]), "created": p["create_date"]})
		}
		return row{"source": "contactos", "leads": out}, nil
	})
}

// ERPBranches handles GET /erp/branches: warehouses and points of sale,
// with whether each register is open and what it sold today.
func (h *Handler) ERPBranches(c *gin.Context) {
	h.erp(c, func(ctx context.Context, cl *odoo.Client) (any, error) {
		var warehouses, configs []row
		if err := cl.SearchRead(ctx, "stock.warehouse", nil, []string{"name", "code", "lot_stock_id", "partner_id"}, "name", 100, &warehouses); err != nil {
			return nil, err
		}
		_ = cl.SearchRead(ctx, "pos.config", nil, []string{"name", "current_session_state", "current_user_id", "picking_type_id"}, "name", 100, &configs)
		today := time.Now().UTC().Format("2006-01-02")
		todays := paidOrders(ctx, cl, []any{[]any{"date_order", ">=", today + " 00:00:00"}}, 5000)
		sales := map[string]float64{}
		count := map[string]int{}
		for _, o := range todays {
			k := odoo.M2O(o["config_id"])
			sales[k] += odoo.Num(o["amount_total"])
			count[k]++
		}
		wh := []row{}
		for _, w := range warehouses {
			var products int
			if loc, ok := w["lot_stock_id"].([]any); ok && len(loc) > 0 {
				_ = cl.Exec(ctx, "stock.quant", "search_count",
					[]any{[]any{[]any{"location_id", "child_of", loc[0]}, []any{"quantity", ">", 0}}}, nil, &products)
			}
			wh = append(wh, row{"name": w["name"], "code": odoo.Str(w["code"]), "address": odoo.M2O(w["partner_id"]), "products_in_stock": products})
		}
		pos := []row{}
		for _, p := range configs {
			name := odoo.Str(p["name"])
			state := odoo.Str(p["current_session_state"])
			pos = append(pos, row{"name": name, "open": state == "opened", "state": state, "cashier": odoo.M2O(p["current_user_id"]),
				"sales_today": sales[name], "orders_today": count[name]})
		}
		return row{"warehouses": wh, "points_of_sale": pos}, nil
	})
}

// ERPHistory handles GET /erp/history?category=ventas|inventario|clientes:
// the account's operations in the ERP, newest first.
func (h *Handler) ERPHistory(c *gin.Context) {
	category := c.Query("category")
	h.erp(c, func(ctx context.Context, cl *odoo.Client) (any, error) {
		items := []row{}
		if category == "" || category == "ventas" {
			for _, o := range paidOrders(ctx, cl, nil, 100) {
				desc := "Venta " + odoo.Str(o["name"])
				if p := odoo.M2O(o["partner_id"]); p != "" {
					desc += " a " + p
				}
				items = append(items, row{"category": "ventas", "date": o["date_order"], "description": desc, "amount": odoo.Num(o["amount_total"]), "where": odoo.M2O(o["config_id"])})
			}
		}
		if category == "" || category == "inventario" {
			var picks []row
			_ = cl.SearchRead(ctx, "stock.picking", []any{[]any{"state", "=", "done"}}, []string{"name", "date_done", "picking_type_id", "origin", "partner_id"}, "date_done desc", 100, &picks)
			for _, p := range picks {
				desc := odoo.M2O(p["picking_type_id"]) + " " + odoo.Str(p["name"])
				if o := odoo.Str(p["origin"]); o != "" {
					desc += " (" + o + ")"
				}
				items = append(items, row{"category": "inventario", "date": p["date_done"], "description": desc, "where": odoo.M2O(p["partner_id"])})
			}
			var adjustments []row
			_ = cl.SearchRead(ctx, "stock.move", []any{[]any{"state", "=", "done"}, []any{"is_inventory", "=", true}},
				[]string{"date", "product_id", "quantity"}, "date desc", 100, &adjustments)
			for _, m := range adjustments {
				items = append(items, row{"category": "inventario", "date": m["date"], "description": "Ajuste de inventario: " + odoo.M2O(m["product_id"]), "quantity": odoo.Num(m["quantity"])})
			}
		}
		if category == "" || category == "clientes" {
			var partners []row
			_ = cl.SearchRead(ctx, "res.partner", []any{[]any{"customer_rank", ">", 0}}, []string{"name", "create_date"}, "create_date desc", 100, &partners)
			for _, p := range partners {
				items = append(items, row{"category": "clientes", "date": p["create_date"], "description": "Nuevo cliente: " + odoo.Str(p["name"])})
			}
		}
		sort.SliceStable(items, func(i, j int) bool { return odoo.Str(items[i]["date"]) > odoo.Str(items[j]["date"]) })
		if len(items) > 200 {
			items = items[:200]
		}
		return row{"items": items}, nil
	})
}
