package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

// ListEvents handles GET /account/events?limit=&offset= -- the activity feed
// backing the dashboard's "Actualizaciones" tab.
func (h *Handler) ListEvents(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		offset = 0
	}

	events, total, err := db.ListEvents(c.Request.Context(), h.Accounts, claims.AccountID, limit, offset)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	out := make([]db.PublicAccountEvent, 0, len(events))
	for _, e := range events {
		out = append(out, e.ToPublic())
	}
	c.JSON(http.StatusOK, gin.H{"events": out, "total": total})
}
