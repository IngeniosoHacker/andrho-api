package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout handles POST /auth/logout. Deleting a refresh token hash that
// doesn't exist is not an error, so logout is idempotent.
func (h *Handler) Logout(c *gin.Context) {
	var req logoutRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.RefreshToken == "" {
		// Nothing to invalidate; treat as a no-op success.
		c.JSON(http.StatusOK, gin.H{"ok": true})
		return
	}

	hash := auth.HashRefreshToken(req.RefreshToken)
	if err := db.DeleteRefreshToken(c.Request.Context(), h.Accounts, hash); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
