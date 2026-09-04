package handlers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/models"
	"github.com/IngeniosoHacker/andrho-api/internal/trackerdb"
)

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

type signupRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	CompanyName string `json:"company_name"`
}

type signupResponse struct {
	AccessToken  string               `json:"access_token"`
	RefreshToken string               `json:"refresh_token"`
	Account      models.PublicAccount `json:"account"`
}

// Signup handles POST /auth/signup: creates the account (company/tenant) and
// its first user, with role 'owner'. andrho-api doesn't wrap this in a DB
// transaction (nothing else in this codebase uses one yet, and pgxpool.Pool
// vs pgx.Tx would need a shared interface across every db/*.go function to
// do it properly) -- if user creation fails after the account was created,
// we best-effort delete the orphaned account rather than leave a
// company-with-no-owner around.
func (h *Handler) Signup(c *gin.Context) {
	var req signupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.CompanyName = strings.TrimSpace(req.CompanyName)

	if !emailRe.MatchString(req.Email) {
		respondError(c, http.StatusBadRequest, "invalid email")
		return
	}
	if len(req.Password) < 8 {
		respondError(c, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if req.CompanyName == "" {
		respondError(c, http.StatusBadRequest, "company_name is required")
		return
	}

	ctx := c.Request.Context()

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	siteID, err := auth.GenerateSiteID(ctx, req.CompanyName, func(ctx context.Context, candidate string) (bool, error) {
		return db.SiteIDExists(ctx, h.Accounts, candidate)
	})
	if err != nil {
		respondError(c, http.StatusInternalServerError, "could not generate a unique site id")
		return
	}

	acc := models.Account{
		ID:           uuid.NewString(),
		Email:        req.Email,
		PasswordHash: passwordHash,
		CompanyName:  req.CompanyName,
		SiteID:       siteID,
		Plan:         "base",
	}

	if err := db.CreateAccount(ctx, h.Accounts, acc); err != nil {
		if errors.Is(err, db.ErrEmailTaken) {
			respondError(c, http.StatusConflict, "email already registered")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	user := models.User{
		ID:           uuid.NewString(),
		AccountID:    acc.ID,
		Email:        req.Email,
		PasswordHash: passwordHash,
		DisplayName:  req.CompanyName,
		Role:         "owner",
	}
	user, err = db.CreateUser(ctx, h.Accounts, user)
	if err != nil {
		_ = db.DeleteAccount(ctx, h.Accounts, acc.ID) // best-effort: don't leave an owner-less account
		if errors.Is(err, db.ErrEmailTaken) {
			respondError(c, http.StatusConflict, "email already registered")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	// Register the site with the tracker so ingestion accepts it right away.
	// This is best-effort in the sense that WebTracker would otherwise
	// auto-register the site on first /collect anyway (unless
	// REQUIRE_KNOWN_SITE=true there), but we still surface failures here so
	// operators notice a misconfigured TRACKER_DATABASE_URL quickly.
	if err := trackerdb.RegisterSite(ctx, h.Tracker, siteID, req.CompanyName); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	tokens, err := h.issueTokenPair(ctx, user, acc)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), acc.ID, &user.ID, "account_created", "Cuenta creada")

	c.JSON(http.StatusCreated, signupResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Account:      acc.ToPublic(),
	})
}

func respondError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}
