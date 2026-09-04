package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/middleware"
	"github.com/IngeniosoHacker/andrho-api/internal/models"
)

// inviteTTL is how long an invite link stays valid before the recipient must
// be re-invited.
const inviteTTL = 7 * 24 * time.Hour

func currentClaims(c *gin.Context) (*auth.AccessClaims, bool) {
	raw, ok := c.Get(middleware.AccountContextKey)
	if !ok {
		return nil, false
	}
	claims, ok := raw.(*auth.AccessClaims)
	return claims, ok
}

// ListUsers handles GET /account/users -- every member (including pending
// invites) can see their own team.
func (h *Handler) ListUsers(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	users, err := db.ListUsersByAccount(c.Request.Context(), h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	out := make([]models.PublicUser, 0, len(users))
	for _, u := range users {
		out = append(out, u.ToPublic())
	}
	c.JSON(http.StatusOK, gin.H{"users": out})
}

type inviteUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type inviteUserResponse struct {
	User        models.PublicUser `json:"user"`
	InviteToken string            `json:"invite_token"`
}

// InviteUser handles POST /account/users/invite (admin+ only, enforced by
// middleware.RequireRole in router.go). There is no email-delivery
// integration yet, so this returns the plain invite token exactly once --
// the caller is responsible for sharing the resulting accept-invite link
// with the invitee (WhatsApp, email client, however).
func (h *Handler) InviteUser(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	var req inviteUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Role = strings.TrimSpace(strings.ToLower(req.Role))

	if !emailRe.MatchString(req.Email) {
		respondError(c, http.StatusBadRequest, "invalid email")
		return
	}
	// Owner is never assigned through an invite -- it's set once at signup.
	if req.Role == "" || req.Role == "owner" || !auth.IsValidRole(req.Role) {
		respondError(c, http.StatusBadRequest, "role must be one of: viewer, editor, admin")
		return
	}

	ctx := c.Request.Context()

	rawToken, err := auth.GenerateRefreshToken() // generic opaque-token generator, not refresh-specific
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	tokenHash := auth.HashRefreshToken(rawToken)
	expiresAt := time.Now().Add(inviteTTL)

	invitedBy := claims.Sub
	user := models.User{
		ID:              uuid.NewString(),
		AccountID:       claims.AccountID,
		Email:           req.Email,
		DisplayName:     req.DisplayName,
		Role:            req.Role,
		InvitedBy:       &invitedBy,
		InviteTokenHash: &tokenHash,
		InviteExpiresAt: &expiresAt,
	}

	user, err = db.CreateUser(ctx, h.Accounts, user)
	if err != nil {
		if errors.Is(err, db.ErrEmailTaken) {
			respondError(c, http.StatusConflict, "that email is already part of a team")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "user_invited",
		"Se invitó a "+req.Email+" como "+req.Role)

	c.JSON(http.StatusCreated, inviteUserResponse{User: user.ToPublic(), InviteToken: rawToken})
}

type acceptInviteRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// AcceptInvite handles POST /auth/accept-invite (public, no auth): sets a
// password for a pending invite and logs the new user in immediately.
func (h *Handler) AcceptInvite(c *gin.Context) {
	var req acceptInviteRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Token == "" {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Password) < 8 {
		respondError(c, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	ctx := c.Request.Context()
	tokenHash := auth.HashRefreshToken(req.Token)

	user, err := db.GetUserByInviteTokenHash(ctx, h.Accounts, tokenHash)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusUnauthorized, "invalid or expired invite")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	if err := db.SetUserPassword(ctx, h.Accounts, user.ID, passwordHash); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	user.PasswordHash = passwordHash

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

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), acc.ID, &user.ID, "invite_accepted", user.Email+" se unió al equipo")

	c.JSON(http.StatusOK, loginResponse{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Account:      acc.ToPublic(),
	})
}

type updateUserRoleRequest struct {
	Role string `json:"role"`
}

// UpdateUserRole handles PATCH /account/users/:id/role (admin+ only).
func (h *Handler) UpdateUserRole(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	var req updateUserRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Role = strings.TrimSpace(strings.ToLower(req.Role))
	if req.Role == "" || req.Role == "owner" || !auth.IsValidRole(req.Role) {
		respondError(c, http.StatusBadRequest, "role must be one of: viewer, editor, admin")
		return
	}

	ctx := c.Request.Context()
	targetID := c.Param("id")

	target, err := db.GetUserByID(ctx, h.Accounts, targetID)
	if err != nil || target.AccountID != claims.AccountID {
		respondError(c, http.StatusNotFound, "user not found")
		return
	}
	if target.Role == "owner" {
		respondError(c, http.StatusBadRequest, "the account owner's role can't be changed")
		return
	}

	if err := db.UpdateUserRole(ctx, h.Accounts, targetID, req.Role); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "user_role_changed",
		target.Email+" ahora es "+req.Role)

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RemoveUser handles DELETE /account/users/:id (admin+ only). The owner can't
// be removed, and you can't remove your own membership through this endpoint
// (avoids an accidental self-lockout; leaving a team is a separate concern
// this v1 doesn't cover yet).
func (h *Handler) RemoveUser(c *gin.Context) {
	claims, ok := currentClaims(c)
	if !ok {
		respondError(c, http.StatusUnauthorized, "missing authorization")
		return
	}

	ctx := c.Request.Context()
	targetID := c.Param("id")

	target, err := db.GetUserByID(ctx, h.Accounts, targetID)
	if err != nil || target.AccountID != claims.AccountID {
		respondError(c, http.StatusNotFound, "user not found")
		return
	}
	if target.Role == "owner" {
		respondError(c, http.StatusBadRequest, "the account owner can't be removed")
		return
	}
	if targetID == claims.Sub {
		respondError(c, http.StatusBadRequest, "you can't remove yourself")
		return
	}

	if err := db.DeleteUser(ctx, h.Accounts, targetID); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	_ = db.LogEvent(ctx, h.Accounts, uuid.NewString(), claims.AccountID, &claims.Sub, "user_removed",
		"Se eliminó a "+target.Email+" del equipo")

	c.JSON(http.StatusOK, gin.H{"ok": true})
}
