package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
)

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Refresh handles POST /auth/refresh. It rotates the refresh token on every
// use (the old hash is deleted and a new one issued) to limit the blast
// radius of a leaked refresh token; see README for the rationale.
func (h *Handler) Refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.RefreshToken == "" {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}

	ctx := c.Request.Context()
	hash := auth.HashRefreshToken(req.RefreshToken)

	stored, err := db.GetRefreshToken(ctx, h.Accounts, hash)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusUnauthorized, "invalid or expired refresh token")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	if time.Now().After(stored.ExpiresAt) {
		_ = db.DeleteRefreshToken(ctx, h.Accounts, hash)
		respondError(c, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	if stored.UserID == "" {
		// Issued before the multi-user migration (no user_id column yet). The
		// caller needs to log in again once to get a token issued the new way.
		respondError(c, http.StatusUnauthorized, "invalid or expired refresh token")
		return
	}

	user, err := db.GetUserByID(ctx, h.Accounts, stored.UserID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	acc, err := db.GetAccountByID(ctx, h.Accounts, user.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	// Rotate: invalidate the presented refresh token before issuing a new pair.
	if err := db.DeleteRefreshToken(ctx, h.Accounts, hash); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	tokens, err := h.issueTokenPair(ctx, user, acc)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusOK, refreshResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
	})
}
