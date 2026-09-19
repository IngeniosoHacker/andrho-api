package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/models"
)

// waitlistRequest mirrors the `form` state shape in andrho's
// MissionForm.jsx/lib/waitlist.js one-to-one, so the frontend can POST its
// state object as-is.
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
	if req.Satisfaction < 0 || req.Satisfaction > 5 {
		respondError(c, http.StatusBadRequest, "satisfaction must be between 0 and 5")
		return
	}

	if req.Sectors == nil {
		req.Sectors = []string{}
	}

	submission := models.WaitlistSubmission{
		ID:                 uuid.NewString(),
		Name:               req.Name,
		Company:            req.Company,
		Email:              req.Email,
		Sectors:            req.Sectors,
		CompanySize:        req.CompanySize,
		SalesMethod:        req.SalesMethod,
		HasWebsite:         req.HasWebsite,
		WebsiteURL:         strings.TrimSpace(req.WebsiteURL),
		RestaurantExpiry:   req.RestaurantExpiry,
		Management:         req.Management,
		Satisfaction:       req.Satisfaction,
		SatisfactionReason: req.SatisfactionReason,
		Improvement:        req.Improvement,
	}

	if err := db.CreateWaitlistSubmission(c.Request.Context(), h.Accounts, submission); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": submission.ID})
}
