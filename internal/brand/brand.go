// Package brand is what the signup learns about a business' identity: its
// kit (logo, colors, data, team; account_setup.kit) and the palette read
// from its logo and the images it uploads (Palette).
package brand

import (
	"regexp"
	"strings"
)

// Kit is the long signup's answers about the brand and the team. Each of
// logo, colors and data is "tengo" (uploaded), "crear" (a Creator makes
// it; colors also "logo": read from the logo; data also "cero": starts
// from scratch) or "despues" (uploaded later from the panel).
type Kit struct {
	Logo        string   `json:"logo"`
	Colors      string   `json:"colors"`
	Palette     []string `json:"palette"` // the colors the person kept, "#rrggbb"
	Data        string   `json:"data"`
	TeamSize    int      `json:"team_size"`
	AccessUsers int      `json:"access_users"`
}

var (
	logoChoices  = map[string]bool{"tengo": true, "crear": true, "despues": true}
	colorChoices = map[string]bool{"tengo": true, "logo": true, "crear": true, "despues": true}
	dataChoices  = map[string]bool{"tengo": true, "cero": true, "despues": true}
	hexRe        = regexp.MustCompile(`^#[0-9a-f]{6}$`)
)

// MaxPalette is the most colors a brand keeps.
const MaxPalette = 8

// Sanitize drops what isn't one of the choices and bad colors.
func (k Kit) Sanitize() Kit {
	pick := func(v string, ok map[string]bool) string {
		if ok[v] {
			return v
		}
		return ""
	}
	out := Kit{Logo: pick(k.Logo, logoChoices), Colors: pick(k.Colors, colorChoices), Data: pick(k.Data, dataChoices), Palette: CleanPalette(k.Palette)}
	if k.TeamSize > 0 && k.TeamSize <= 100000 {
		out.TeamSize = k.TeamSize
	}
	if k.AccessUsers > 0 && k.AccessUsers <= 100000 {
		out.AccessUsers = k.AccessUsers
	}
	if out.TeamSize > 0 && out.AccessUsers > out.TeamSize {
		out.TeamSize = out.AccessUsers
	}
	return out
}

// Empty is a kit nobody filled (the quick signup).
func (k Kit) Empty() bool {
	return k.Logo == "" && k.Colors == "" && k.Data == "" && len(k.Palette) == 0 && k.TeamSize == 0 && k.AccessUsers == 0
}

// CleanPalette keeps valid, distinct "#rrggbb" colors, at most MaxPalette.
func CleanPalette(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if hexRe.MatchString(c) && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
		if len(out) == MaxPalette {
			break
		}
	}
	return out
}

// CompanySize is the survey's range for a head count (recommend.Answers).
func CompanySize(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "solo"
	case n <= 10:
		return "1-10"
	case n <= 50:
		return "11-50"
	case n <= 200:
		return "51-200"
	}
	return "200+"
}
