package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/models"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken  string               `json:"access_token"`
	RefreshToken string               `json:"refresh_token"`
	Account      models.PublicAccount `json:"account"`
}

// Login handles POST /auth/login. Rate limiting is applied by the
// middleware.LoginRateLimit middleware mounted ahead of this handler. Auth is
// by *user* (email/password on the `users` table) since the multi-user
// migration -- a pending invite (password_hash still NULL) can't log in yet.
func (h *Handler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	ctx := c.Request.Context()

	user, err := db.GetUserByEmail(ctx, h.Accounts, req.Email)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusUnauthorized, "invalid email or password")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	if user.PasswordHash == "" || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		respondError(c, http.StatusUnauthorized, "invalid email or password")
		return
	}

	acc, err := db.GetAccountByID(ctx, h.Accounts, user.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	tokens, err := h.issueTokenPair(ctx, user, acc)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusOK, loginResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Account:      acc.ToPublic(),
	})
}
