package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Team presence (the dashboard's Usuarios) and per-person preferences
// (the cards on Inicio).

var validStatuses = map[string]bool{"disponible": true, "almorzando": true, "en_reunion": true, "en_ruta": true, "fuera": true}

// onlineWindow: seen by the dashboard's heartbeat within this, "en línea".
const onlineWindow = 3 * time.Minute

type presence struct {
	UserID   string    `json:"user_id"`
	Status   string    `json:"status"`
	Note     string    `json:"note"`
	StatusAt time.Time `json:"status_at"`
	LastSeen time.Time `json:"last_seen_at"`
	Online   bool      `json:"online"`
}

// TeamPresence handles GET /account/presence: everyone on the account.
func (h *Handler) TeamPresence(c *gin.Context) {
	claims, _ := currentClaims(c)
	rows, err := h.Accounts.Query(c.Request.Context(),
		`SELECT p.user_id, p.status, p.note, p.status_at, p.last_seen_at FROM user_presence p
		 JOIN users u ON u.id = p.user_id WHERE u.account_id = $1`, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	out := []presence{}
	for rows.Next() {
		var p presence
		if err := rows.Scan(&p.UserID, &p.Status, &p.Note, &p.StatusAt, &p.LastSeen); err != nil {
			respondError(c, http.StatusInternalServerError, "internal error")
			return
		}
		p.Online = time.Since(p.LastSeen) < onlineWindow
		out = append(out, p)
	}
	c.JSON(http.StatusOK, gin.H{"presence": out})
}

type statusRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// SetMyStatus handles PATCH /account/me/status.
func (h *Handler) SetMyStatus(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req statusRequest
	if err := c.ShouldBindJSON(&req); err != nil || !validStatuses[req.Status] {
		respondError(c, http.StatusBadRequest, "status: disponible, almorzando, en_reunion, en_ruta o fuera")
		return
	}
	note := strings.TrimSpace(req.Note)
	if len(note) > 140 {
		note = note[:140]
	}
	_, err := h.Accounts.Exec(c.Request.Context(),
		`INSERT INTO user_presence (user_id, status, note, status_at, last_seen_at) VALUES ($1, $2, $3, now(), now())
		 ON CONFLICT (user_id) DO UPDATE SET status = $2, note = $3, status_at = now(), last_seen_at = now()`,
		claims.Sub, req.Status, note)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Heartbeat handles POST /account/me/heartbeat: the dashboard is open.
func (h *Handler) Heartbeat(c *gin.Context) {
	claims, _ := currentClaims(c)
	_, err := h.Accounts.Exec(c.Request.Context(),
		`INSERT INTO user_presence (user_id) VALUES ($1)
		 ON CONFLICT (user_id) DO UPDATE SET last_seen_at = now()`, claims.Sub)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.Status(http.StatusNoContent)
}

// GetPreferences handles GET /account/me/preferences.
func (h *Handler) GetPreferences(c *gin.Context) {
	claims, _ := currentClaims(c)
	var prefs json.RawMessage
	err := h.Accounts.QueryRow(c.Request.Context(), `SELECT prefs FROM user_preferences WHERE user_id = $1`, claims.Sub).Scan(&prefs)
	if err != nil {
		prefs = json.RawMessage(`{}`)
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", prefs)
}

// SavePreferences handles PUT /account/me/preferences: a JSON object of at
// most 8 KB, stored as is (the dashboard owns its shape).
func (h *Handler) SavePreferences(c *gin.Context) {
	claims, _ := currentClaims(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var obj map[string]any
	if err := c.ShouldBindJSON(&obj); err != nil {
		respondError(c, http.StatusBadRequest, "las preferencias deben ser un objeto JSON de hasta 8 KB")
		return
	}
	raw, _ := json.Marshal(obj)
	_, err := h.Accounts.Exec(c.Request.Context(),
		`INSERT INTO user_preferences (user_id, prefs) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET prefs = $2, updated_at = now()`, claims.Sub, raw)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
