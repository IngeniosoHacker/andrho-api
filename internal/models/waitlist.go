package models

import "time"

// WaitlistSubmission represents a row in the `waitlist_submissions` table --
// one completed run of andrho's pre-launch "MissionForm" survey. Public,
// unauthenticated: no account exists yet for whoever submits this.
type WaitlistSubmission struct {
	ID                 string
	Name               string
	Company            string
	Email              string
	Sectors            []string
	CompanySize        string
	SalesMethod        string
	HasWebsite         string
	WebsiteURL         string
	RestaurantExpiry   string
	Management         string
	Satisfaction       int
	SatisfactionReason string
	Improvement        string
	Phone              string
	SalesMethods       []string
	SectorAnswers      map[string]string
	UnsoldHandling     string
	SocialNetworks     []string
	MessagingApps      []string
	CurrentSoftware    string
	PainPoints         []string
	Modules            []string
	Expectations       []string
	PlanInterest       string
	SuggestedModules   []string
	RecommendedPlan    string
	CreatedAt          time.Time
}
