package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/analyzer"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// AndRho's own team: admins see AndRho's metrics (/admin/*), creators work
// on customer tasks (/creators/*). Staff roles live in the `staff` table,
// not in the JWT, so revoking one takes effect on the next request.

const staffRoleKey = "staff_role"

// RequireStaff lets through staff with one of roles (admin always passes).
func (h *Handler) RequireStaff(roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := currentClaims(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization"})
			return
		}
		role, err := db.StaffRole(c.Request.Context(), h.Accounts, claims.Sub)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		allowed := role == "admin"
		for _, r := range roles {
			allowed = allowed || role == r
		}
		if role == "" || !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "solo para el equipo de AndRho"})
			return
		}
		c.Set(staffRoleKey, role)
		c.Next()
	}
}

// ---------------------------------------------------------------------
// Admin
// ---------------------------------------------------------------------

func toMap(counts []db.Count) map[string]int {
	m := map[string]int{}
	for _, c := range counts {
		m[c.Value] = c.Count
	}
	return m
}

// AdminOverview handles GET /admin/overview: the waiting list in aggregate,
// AndRho's accounts and the ranking of what to build first.
func (h *Handler) AdminOverview(c *gin.Context) {
	ctx := c.Request.Context()
	wl, err := db.GetWaitlistStats(ctx, h.Accounts)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	acc, err := db.GetAccountsOverview(ctx, h.Accounts)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"waitlist": wl,
		"accounts": acc,
		"urgency": analyzer.Urgency(toMap(wl.Fields["modules"]), toMap(wl.Fields["suggested_modules"]),
			toMap(wl.Fields["pain_points"]), toMap(acc.Modules)),
	})
}

// AdminWaitlist handles GET /admin/waitlist?limit=&offset=: the submissions.
func (h *Handler) AdminWaitlist(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.ListWaitlist(c.Request.Context(), h.Accounts, limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"submissions": rows, "limit": limit, "offset": offset})
}

// AdminGame handles GET /admin/game: the landing minigame's results.
func (h *Handler) AdminGame(c *gin.Context) {
	g, err := db.GetGameStats(c.Request.Context(), h.Tracker, h.Cfg.AndRhoSiteID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, g)
}

// ---------------------------------------------------------------------
// Creators
// ---------------------------------------------------------------------

// CreatorTasks handles GET /creators/tasks: a creator sees the open tasks
// and theirs; an admin sees every task.
func (h *Handler) CreatorTasks(c *gin.Context) {
	claims, _ := currentClaims(c)
	who := claims.Sub
	if c.GetString(staffRoleKey) == "admin" {
		who = ""
	}
	tasks, err := db.ListCreatorTasks(c.Request.Context(), h.Accounts, who)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}

type creatorTaskAction struct {
	Action      string `json:"action"` // aceptar | entregar | liberar
	DeliveryURL string `json:"delivery_url"`
	Notes       string `json:"notes"`
}

// UpdateCreatorTask handles PATCH /creators/tasks/:id.
//
//	aceptar  an open task becomes the creator's
//	entregar the creator's task is delivered (delivery_url required)
//	liberar  the creator gives the task back to the open list
func (h *Handler) UpdateCreatorTask(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req creatorTaskAction
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	t, err := db.GetCreatorTask(ctx, h.Accounts, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "tarea no encontrada")
		return
	}
	admin := c.GetString(staffRoleKey) == "admin"
	mine := t.CreatorID != nil && *t.CreatorID == claims.Sub
	req.DeliveryURL = strings.TrimSpace(req.DeliveryURL)
	notes := strings.TrimSpace(req.Notes)
	if len(notes) > 2000 || len(req.DeliveryURL) > 500 {
		respondError(c, http.StatusBadRequest, "texto demasiado largo")
		return
	}
	if notes == "" {
		notes = t.Notes
	}

	var status string
	creator := t.CreatorID
	var event string
	switch req.Action {
	case "aceptar":
		if t.Status != "abierta" {
			respondError(c, http.StatusConflict, "la tarea ya no está abierta")
			return
		}
		status, creator, event = "aceptada", &claims.Sub, "Un Creator aceptó: "+t.Title
	case "entregar":
		if !mine && !admin {
			respondError(c, http.StatusForbidden, "no es tu tarea")
			return
		}
		if !strings.HasPrefix(req.DeliveryURL, "https://") && !strings.HasPrefix(req.DeliveryURL, "http://") {
			respondError(c, http.StatusBadRequest, "la entrega necesita un enlace")
			return
		}
		status, event = "entregada", "Entregado: "+t.Title
	case "liberar":
		if !mine && !admin {
			respondError(c, http.StatusForbidden, "no es tu tarea")
			return
		}
		status, creator, event = "abierta", nil, "Tarea liberada: "+t.Title
	default:
		respondError(c, http.StatusBadRequest, "action debe ser aceptar, entregar o liberar")
		return
	}
	delivery := t.DeliveryURL
	if req.DeliveryURL != "" {
		delivery = req.DeliveryURL
	}
	if err := db.UpdateCreatorTask(ctx, h.Accounts, t.ID, status, creator, delivery, notes); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), t.AccountID, nil, "creator_task_"+status, event)
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": status})
}

// CreatorTaskLogo handles GET /creators/tasks/:id/logo: the customer's logo
// for the creator working on it.
func (h *Handler) CreatorTaskLogo(c *gin.Context) {
	t, err := db.GetCreatorTask(c.Request.Context(), h.Accounts, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "tarea no encontrada")
		return
	}
	h.serveLogo(c, t.AccountID)
}
