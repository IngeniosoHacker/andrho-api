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
	CreatedAt          time.Time
}
