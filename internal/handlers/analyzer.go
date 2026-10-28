package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/IngeniosoHacker/andrho-api/internal/analyzer"
	"github.com/IngeniosoHacker/andrho-api/internal/recommend"
)

// Analyze handles POST /analyzer: the survey's answers (same body as
// /waitlist/recommendation) -> the business' needs and three packages.
// Public: the signup calls it before the account exists.
func (h *Handler) Analyze(c *gin.Context) {
	var a recommend.Answers
	if err := c.ShouldBindJSON(&a); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	c.JSON(http.StatusOK, analyzer.Analyze(analyzer.Sanitize(a)))
}

type describeRequest struct {
	Description string `json:"description"`
}

// Describe handles POST /analyzer/describe: the quick setup's free-text
// description -> answers (Claude, or keyword rules without it) -> the same
// analysis as /analyzer.
func (h *Handler) Describe(c *gin.Context) {
	var req describeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	text := strings.TrimSpace(req.Description)
	if utf8.RuneCountInString(text) < 15 {
		respondError(c, http.StatusBadRequest, "cuéntanos un poco más de tu negocio")
		return
	}
	if utf8.RuneCountInString(text) > analyzer.MaxDescription {
		respondError(c, http.StatusBadRequest, "la descripción no puede pasar de 2000 caracteres")
		return
	}
	d := analyzer.Describe(c.Request.Context(), h.AI, text)
	c.JSON(http.StatusOK, gin.H{"description": d, "analysis": analyzer.Analyze(d.Answers)})
}

type suggestRequest struct {
	recommend.Answers
	analyzer.Details
	Description string `json:"description"`
	Brand       struct {
		Choice  string `json:"choice"`
		Essence string `json:"essence"`
	} `json:"brand"`
}

// Suggest handles POST /analyzer/suggest: everything the signup asked (the
// survey's answers, the description if there was one, the brand) -> the
// rules' analysis blended with Claude's (analyzer.Suggest). Public, like
// /analyzer: the account doesn't exist yet.
func (h *Handler) Suggest(c *gin.Context) {
	var req suggestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if utf8.RuneCountInString(req.Description) > analyzer.MaxDescription {
		respondError(c, http.StatusBadRequest, "la descripción no puede pasar de 2000 caracteres")
		return
	}
	if utf8.RuneCountInString(req.Brand.Essence) > maxBrandEssence {
		respondError(c, http.StatusBadRequest, "la esencia de tu marca no puede pasar de 1000 caracteres")
		return
	}
	in := analyzer.SuggestInput{Answers: req.Answers, Details: req.Details, Description: strings.TrimSpace(req.Description)}
	in.Brand.Choice, in.Brand.Essence = req.Brand.Choice, strings.TrimSpace(req.Brand.Essence)
	c.JSON(http.StatusOK, analyzer.Suggest(c.Request.Context(), h.AI, in))
}
