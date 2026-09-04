package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AccessClaims is the exact JWT claim contract shared with
// andrho-tracker-dashboard (mirrored in that repo's src/middleware/auth.js).
// Do not rename or remove fields without coordinating with that repo.
//
// `sub` identifies the *user* (a row in `users`), not the account/tenant --
// that's `account_id` below. This changed with the multi-user/roles
// migration; anything that used to treat `sub` as the account id must read
// `account_id` instead now.
type AccessClaims struct {
	Sub         string `json:"sub"`        // user id
	AccountID   string `json:"account_id"` // account/tenant id
	Email       string `json:"email"`
	SiteID      string `json:"site_id"`
	CompanyName string `json:"company_name"`
	Role        string `json:"role"` // owner | admin | editor | viewer, see roles.go
	jwt.RegisteredClaims
}

// IssueAccessToken creates and signs an HS256 access token carrying the
// user's identity, their account's site_id/company_name, and their role,
// expiring after ttl.
func IssueAccessToken(secret, userID, accountID, email, siteID, companyName, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		Sub:         userID,
		AccountID:   accountID,
		Email:       email,
		SiteID:      siteID,
		CompanyName: companyName,
		Role:        role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseAccessToken verifies signature and expiration and returns the claims.
func ParseAccessToken(secret, tokenString string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("auth: unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("auth: invalid token")
	}
	return claims, nil
}
