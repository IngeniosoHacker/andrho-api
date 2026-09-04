package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/middleware"
	"github.com/IngeniosoHacker/andrho-api/internal/models"
)

// Me handles GET /auth/me. It re-reads the user and account from the
// database (rather than trusting the JWT claims verbatim) so that changes
// made after the token was issued (role change, plan change, display name,
// company_name update, ...) are reflected without waiting for the token to
// expire.
func (h *Handler) Me(c *gin.Context) {
	raw, ok := c.Get(middleware.AccountContextKey)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	claims, ok := raw.(*auth.AccessClaims)
	if !ok {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	ctx := c.Request.Context()

	user, err := db.GetUserByID(ctx, h.Accounts, claims.Sub)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusUnauthorized, "user not found")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	acc, err := db.GetAccountByID(ctx, h.Accounts, user.AccountID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusUnauthorized, "account not found")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusOK, models.MeResponse{
		UserID:      user.ID,
		AccountID:   acc.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		CompanyName: acc.CompanyName,
		SiteID:      acc.SiteID,
		Plan:        acc.Plan,
	})
}
