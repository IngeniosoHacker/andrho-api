package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/IngeniosoHacker/andrho-api/internal/auth"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/models"
)

// tokenPair is the access/refresh token pair returned by signup, login and
// (for the access token) refresh.
type tokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// issueTokenPair mints a new access token (JWT) and a new opaque refresh
// token, persisting only the refresh token's SHA-256 hash.
func (h *Handler) issueTokenPair(ctx context.Context, acc models.Account) (tokenPair, error) {
	accessToken, err := auth.IssueAccessToken(h.Cfg.JWTSecret, acc.ID, acc.Email, acc.SiteID, acc.CompanyName, h.accessTTL())
	if err != nil {
		return tokenPair{}, fmt.Errorf("issue access token: %w", err)
	}

	refreshToken, err := auth.GenerateRefreshToken()
	if err != nil {
		return tokenPair{}, fmt.Errorf("generate refresh token: %w", err)
	}

	hash := auth.HashRefreshToken(refreshToken)
	expiresAt := time.Now().Add(h.refreshTTL())
	if err := db.CreateRefreshToken(ctx, h.Accounts, hash, acc.ID, expiresAt); err != nil {
		return tokenPair{}, fmt.Errorf("persist refresh token: %w", err)
	}

	return tokenPair{AccessToken: accessToken, RefreshToken: refreshToken}, nil
}
