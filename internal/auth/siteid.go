package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

var (
	nonAlnumRe   = regexp.MustCompile(`[^a-z0-9]+`)
	dashRunRe    = regexp.MustCompile(`-+`)
	maxSiteIDLen = 48
)

// Slugify lowercases s, replaces runs of non-alphanumeric characters with a
// single '-', collapses repeated dashes and trims leading/trailing dashes.
func Slugify(s string) string {
	slug := strings.ToLower(strings.TrimSpace(s))
	slug = nonAlnumRe.ReplaceAllString(slug, "-")
	slug = dashRunRe.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "site"
	}
	if len(slug) > maxSiteIDLen {
		slug = strings.Trim(slug[:maxSiteIDLen], "-")
	}
	return slug
}

// SiteIDExistsFunc checks whether a candidate site_id is already taken.
type SiteIDExistsFunc func(ctx context.Context, candidate string) (bool, error)

// GenerateSiteID slugifies companyName and, if the resulting id is already
// taken, retries a handful of times by appending a random 4-hex-char suffix
// until a free one is found.
func GenerateSiteID(ctx context.Context, companyName string, exists SiteIDExistsFunc) (string, error) {
	base := Slugify(companyName)

	taken, err := exists(ctx, base)
	if err != nil {
		return "", err
	}
	if !taken {
		return base, nil
	}

	const maxAttempts = 8
	for i := 0; i < maxAttempts; i++ {
		suffix, err := randomHex4()
		if err != nil {
			return "", err
		}
		candidate := fmt.Sprintf("%s-%s", base, suffix)
		taken, err := exists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("auth: could not generate a unique site_id for %q after %d attempts", companyName, maxAttempts)
}

func randomHex4() (string, error) {
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
