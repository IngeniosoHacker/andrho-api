package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IngeniosoHacker/andrho-api/internal/models"
)

// CreateWaitlistSubmission inserts one completed waiting-list survey.
func CreateWaitlistSubmission(ctx context.Context, pool *pgxpool.Pool, s models.WaitlistSubmission) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO waitlist_submissions
			(id, name, company, email, sectors, company_size, sales_method, has_website,
			 website_url, restaurant_expiry, management, satisfaction, satisfaction_reason, improvement,
			 phone, sales_methods, sector_answers, unsold_handling, social_networks, messaging_apps,
			 current_software, pain_points, modules, expectations, plan_interest, suggested_modules)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
		         $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
		s.ID, s.Name, s.Company, s.Email, s.Sectors, s.CompanySize, s.SalesMethod, s.HasWebsite,
		s.WebsiteURL, s.RestaurantExpiry, s.Management, s.Satisfaction, s.SatisfactionReason, s.Improvement,
		s.Phone, s.SalesMethods, s.SectorAnswers, s.UnsoldHandling, s.SocialNetworks, s.MessagingApps,
		s.CurrentSoftware, s.PainPoints, s.Modules, s.Expectations, s.PlanInterest, s.SuggestedModules,
	)
	if err != nil {
		return fmt.Errorf("db: create waitlist submission: %w", err)
	}
	return nil
}
