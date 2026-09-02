package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// GenerateRefreshToken returns a random 32-byte, base64url-encoded opaque
// string. This is what the client stores and sends back verbatim; it is NOT
// a JWT and carries no claims.
func GenerateRefreshToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashRefreshToken returns the hex-encoded SHA-256 hash of a refresh token,
// which is what actually gets stored in the refresh_tokens table.
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
