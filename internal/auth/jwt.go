package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AccessClaims is the exact JWT claim contract shared with
// andrho-tracker-dashboard. Do not rename or remove fields without
// coordinating with that repo.
type AccessClaims struct {
	Sub         string `json:"sub"`
	Email       string `json:"email"`
	SiteID      string `json:"site_id"`
	CompanyName string `json:"company_name"`
	jwt.RegisteredClaims
}

// IssueAccessToken creates and signs an HS256 access token carrying the
// account's identity, site_id and company_name, expiring after ttl.
func IssueAccessToken(secret string, accountID, email, siteID, companyName string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		Sub:         accountID,
		Email:       email,
		SiteID:      siteID,
		CompanyName: companyName,
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
