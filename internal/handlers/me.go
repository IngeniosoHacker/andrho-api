package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/middleware"
)

// Me handles GET /auth/me. It re-reads the account from the database (rather
// than trusting the JWT claims verbatim) so that changes made after the
// token was issued (e.g. a future company_name update) are reflected.
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

	acc, err := db.GetAccountByID(c.Request.Context(), h.Accounts, claims.Sub)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusUnauthorized, "account not found")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusOK, acc.ToPublic())
}
