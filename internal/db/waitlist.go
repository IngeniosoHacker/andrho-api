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
			 website_url, restaurant_expiry, management, satisfaction, satisfaction_reason, improvement)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		s.ID, s.Name, s.Company, s.Email, s.Sectors, s.CompanySize, s.SalesMethod, s.HasWebsite,
		s.WebsiteURL, s.RestaurantExpiry, s.Management, s.Satisfaction, s.SatisfactionReason, s.Improvement,
	)
	if err != nil {
		return fmt.Errorf("db: create waitlist submission: %w", err)
	}
	return nil
}
