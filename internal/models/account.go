// Package models holds the persistence-layer data structures.
package models

import "time"

// Account represents a row in the `accounts` table.
type Account struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	PasswordHash  string    `json:"-"`
	CompanyName   string    `json:"company_name"`
	SiteID        string    `json:"site_id"`
	OdooCompanyID *string   `json:"odoo_company_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// PublicAccount is the subset of Account fields safe to return in API
// responses (signup, login, /auth/me).
type PublicAccount struct {
	Email       string `json:"email"`
	CompanyName string `json:"company_name"`
	SiteID      string `json:"site_id"`
}

// ToPublic converts an Account to its PublicAccount representation.
func (a Account) ToPublic() PublicAccount {
	return PublicAccount{
		Email:       a.Email,
		CompanyName: a.CompanyName,
		SiteID:      a.SiteID,
	}
}
