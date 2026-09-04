package models

import "time"

// User represents a row in the `users` table -- an individual login that
// belongs to exactly one account (company/tenant). See internal/auth/roles.go
// for the role hierarchy.
type User struct {
	ID              string
	AccountID       string
	Email           string
	PasswordHash    string // empty/NULL while an invite is pending
	DisplayName     string
	Role            string
	InvitedBy       *string
	InviteTokenHash *string
	InviteExpiresAt *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PublicUser is the subset of User fields safe to return over the API (team
// list, invite responses).
type PublicUser struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	Pending     bool      `json:"pending"` // true while an invite hasn't been accepted yet
	CreatedAt   time.Time `json:"created_at"`
}

// ToPublic converts a User to its PublicUser representation.
func (u User) ToPublic() PublicUser {
	return PublicUser{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		Pending:     u.PasswordHash == "",
		CreatedAt:   u.CreatedAt,
	}
}

// MeResponse is the shape returned by GET /auth/me: the requesting user's
// identity/role plus their account's tenant info. This is what
// andrho-tracker-dashboard's dashboard boots from (see public/dashboard/app.js).
type MeResponse struct {
	UserID      string `json:"user_id"`
	AccountID   string `json:"account_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	CompanyName string `json:"company_name"`
	SiteID      string `json:"site_id"`
	Plan        string `json:"plan"`
}
