package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/models"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// waitlistRequest mirrors the answers object built by andrho's waitlist.html
// wizard (lib/waitlist.js), so the frontend can POST it as-is. The fields
// after Improvement were added with the v2 survey (waitinglist.md); older
// clients simply leave them empty.
type waitlistRequest struct {
	Name               string   `json:"name"`
	Company            string   `json:"company"`
	Email              string   `json:"email"`
	Sectors            []string `json:"sectors"`
	CompanySize        string   `json:"companySize"`
	SalesMethod        string   `json:"salesMethod"`
	HasWebsite         string   `json:"hasWebsite"`
	WebsiteURL         string   `json:"websiteUrl"`
	RestaurantExpiry   string   `json:"restaurantExpiry"`
	Management         string   `json:"management"`
	Satisfaction       int      `json:"satisfaction"`
	SatisfactionReason string   `json:"satisfactionReason"`
	Improvement        string   `json:"improvement"`

	Phone           string            `json:"phone"`
	SalesMethods    []string          `json:"salesMethods"`
	SectorAnswers   map[string]string `json:"sectorAnswers"`
	UnsoldHandling  string            `json:"unsoldHandling"`
	SocialNetworks  []string          `json:"socialNetworks"`
	MessagingApps   []string          `json:"messagingApps"`
	CurrentSoftware string            `json:"currentSoftware"`
	PainPoints      []string          `json:"painPoints"`
	Modules         []string          `json:"modules"`
	Expectations    []string          `json:"expectations"`
	PlanInterest    string            `json:"planInterest"`
}

// Limits for a public, unauthenticated endpoint: option lists are short
// slugs, free text is a few paragraphs at most.
const (
	waitlistMaxList      = 20
	waitlistMaxSlug      = 80
	waitlistMaxText      = 2000
	waitlistMaxExpectSel = 3
)

// cleanList trims, drops empties and duplicates; ok=false if the list or one
// of its items is over the limits.
func cleanList(in []string) (out []string, ok bool) {
	out = []string{}
	if len(in) > waitlistMaxList {
		return out, false
	}
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		if len(v) > waitlistMaxSlug {
			return out, false
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, true
}

// CreateWaitlistSubmission handles POST /waitlist: stores one completed run
// of the pre-launch survey. Public, unauthenticated, rate-limited (see
// middleware.WaitlistRateLimit) since it's open to the whole internet.
func (h *Handler) CreateWaitlistSubmission(c *gin.Context) {
	var req waitlistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Company = strings.TrimSpace(req.Company)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	if req.Name == "" || req.Company == "" {
		respondError(c, http.StatusBadRequest, "name and company are required")
		return
	}
	if !emailRe.MatchString(req.Email) {
		respondError(c, http.StatusBadRequest, "invalid email")
		return
	}
	if msg := cleanWaitlistRequest(&req); msg != "" {
		respondError(c, http.StatusBadRequest, msg)
		return
	}

	rec := recommend.Recommend(req.answers())

	submission := models.WaitlistSubmission{
		ID:                 uuid.NewString(),
		Name:               req.Name,
		Company:            req.Company,
		Email:              req.Email,
		Sectors:            req.Sectors,
		CompanySize:        req.CompanySize,
		SalesMethod:        req.SalesMethod,
		HasWebsite:         req.HasWebsite,
		WebsiteURL:         req.WebsiteURL,
		RestaurantExpiry:   req.RestaurantExpiry,
		Management:         req.Management,
		Satisfaction:       req.Satisfaction,
		SatisfactionReason: req.SatisfactionReason,
		Improvement:        req.Improvement,
		Phone:              req.Phone,
		SalesMethods:       req.SalesMethods,
		SectorAnswers:      req.SectorAnswers,
		UnsoldHandling:     req.UnsoldHandling,
		SocialNetworks:     req.SocialNetworks,
		MessagingApps:      req.MessagingApps,
		CurrentSoftware:    req.CurrentSoftware,
		PainPoints:         req.PainPoints,
		Modules:            req.Modules,
		Expectations:       req.Expectations,
		PlanInterest:       req.PlanInterest,
		SuggestedModules:   rec.Modules,
		RecommendedPlan:    rec.Plan,
	}

	if err := db.CreateWaitlistSubmission(c.Request.Context(), h.Accounts, submission); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": submission.ID, "recommendation": rec})
}

// RecommendPlan handles POST /waitlist/recommendation: the plan that best
// matches the answers given so far (any subset of the survey), with the
// reasons and the suggested modules. Stores nothing.
func (h *Handler) RecommendPlan(c *gin.Context) {
	var req waitlistRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := cleanWaitlistRequest(&req); msg != "" {
		respondError(c, http.StatusBadRequest, msg)
		return
	}
	c.JSON(http.StatusOK, recommend.Recommend(req.answers()))
}

func (r waitlistRequest) answers() recommend.Answers {
	return recommend.Answers{
		Sectors:        r.Sectors,
		CompanySize:    r.CompanySize,
		SalesMethods:   r.SalesMethods,
		SectorAnswers:  r.SectorAnswers,
		RestaurantExp:  r.RestaurantExpiry,
		UnsoldHandling: r.UnsoldHandling,
		HasWebsite:     r.HasWebsite,
		SocialNetworks: r.SocialNetworks,
		MessagingApps:  r.MessagingApps,
		Management:     r.Management,
		Satisfaction:   r.Satisfaction,
		PainPoints:     r.PainPoints,
		Modules:        r.Modules,
		Expectations:   r.Expectations,
	}
}

// cleanWaitlistRequest trims and bounds every field in place; it returns a
// client-facing error message, or "" when the request is acceptable.
func cleanWaitlistRequest(req *waitlistRequest) string {
	lists := []*[]string{
		&req.Sectors, &req.SalesMethods, &req.SocialNetworks, &req.MessagingApps,
		&req.PainPoints, &req.Modules, &req.Expectations,
	}
	for _, l := range lists {
		cleaned, ok := cleanList(*l)
		if !ok {
			return "too many or too long options"
		}
		*l = cleaned
	}
	if len(req.Expectations) > waitlistMaxExpectSel {
		return "pick at most 3 expectations"
	}

	if req.SectorAnswers == nil {
		req.SectorAnswers = map[string]string{}
	}
	if len(req.SectorAnswers) > waitlistMaxList {
		return "too many sector answers"
	}
	for k, v := range req.SectorAnswers {
		if len(k) > waitlistMaxSlug || len(v) > waitlistMaxText {
			return "sector answer too long"
		}
	}

	for _, t := range []*string{
		&req.Name, &req.Company, &req.Phone, &req.CompanySize, &req.SalesMethod, &req.HasWebsite,
		&req.WebsiteURL, &req.RestaurantExpiry, &req.Management, &req.SatisfactionReason,
		&req.Improvement, &req.UnsoldHandling, &req.CurrentSoftware, &req.PlanInterest,
	} {
		*t = strings.TrimSpace(*t)
		if len(*t) > waitlistMaxText {
			return "text field too long"
		}
	}

	if req.Satisfaction < 0 || req.Satisfaction > 5 {
		return "satisfaction must be between 0 and 5"
	}
	return ""
}
