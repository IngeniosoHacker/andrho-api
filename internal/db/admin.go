package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Queries behind AndRho's own staff views: the waiting list, the minigame,
// the accounts. Only staff admins reach them (handlers/admin.go).

// Count is one value and how many times it appears.
type Count struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// WaitlistRow is one submission, as the admin list shows it.
type WaitlistRow struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Company         string    `json:"company"`
	Email           string    `json:"email"`
	Phone           string    `json:"phone"`
	Sectors         []string  `json:"sectors"`
	CompanySize     string    `json:"company_size"`
	Management      string    `json:"management"`
	Modules         []string  `json:"modules"`
	PlanInterest    string    `json:"plan_interest"`
	RecommendedPlan string    `json:"recommended_plan"`
	Improvement     string    `json:"improvement"`
	CreatedAt       time.Time `json:"created_at"`
}

// WaitlistStats is the waiting list in aggregate.
type WaitlistStats struct {
	Total           int                `json:"total"`
	Last7Days       int                `json:"last_7_days"`
	Daily           []Count            `json:"daily"` // last 30 days, value = YYYY-MM-DD
	Fields          map[string][]Count `json:"fields"`
	PlanMatch       []PlanMatch        `json:"plan_match"`
	Improvement     []string           `json:"improvements"` // latest free-text answers
	AvgSatisfaction float64            `json:"avg_satisfaction"`
}

// PlanMatch: how many picked `picked` when AndRho recommended `recommended`.
type PlanMatch struct {
	Recommended string `json:"recommended"`
	Picked      string `json:"picked"`
	Count       int    `json:"count"`
}

// waitlistFields are the columns counted value by value; arrays are unnested.
var waitlistFields = map[string]bool{ // name -> is array
	"sectors": true, "company_size": false, "sales_methods": true, "management": false, "has_website": false,
	"social_networks": true, "messaging_apps": true, "pain_points": true, "modules": true, "expectations": true,
	"plan_interest": false, "recommended_plan": false, "suggested_modules": true, "unsold_handling": false,
}

// GetWaitlistStats aggregates every submission.
func GetWaitlistStats(ctx context.Context, pool *pgxpool.Pool) (WaitlistStats, error) {
	st := WaitlistStats{Fields: map[string][]Count{}}
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE created_at >= now() - interval '7 days'),
		        COALESCE(ROUND(AVG(NULLIF(satisfaction, 0))::numeric, 2), 0)
		 FROM waitlist_submissions`).Scan(&st.Total, &st.Last7Days, &st.AvgSatisfaction); err != nil {
		return st, fmt.Errorf("db: waitlist totals: %w", err)
	}
	var err error
	if st.Daily, err = counts(ctx, pool,
		`SELECT to_char(created_at::date, 'YYYY-MM-DD'), COUNT(*) FROM waitlist_submissions
		 WHERE created_at >= now() - interval '30 days' GROUP BY 1 ORDER BY 1`); err != nil {
		return st, err
	}
	for field, isArray := range waitlistFields {
		col := field
		if isArray {
			col = "unnest(" + field + ")"
		}
		q := `SELECT v, COUNT(*) FROM (SELECT ` + col + ` AS v FROM waitlist_submissions) x
		      WHERE v <> '' GROUP BY v ORDER BY COUNT(*) DESC, v`
		if st.Fields[field], err = counts(ctx, pool, q); err != nil {
			return st, err
		}
	}
	rows, err := pool.Query(ctx,
		`SELECT recommended_plan, plan_interest, COUNT(*) FROM waitlist_submissions
		 WHERE recommended_plan <> '' AND plan_interest <> '' GROUP BY 1, 2 ORDER BY 3 DESC`)
	if err != nil {
		return st, fmt.Errorf("db: plan match: %w", err)
	}
	defer rows.Close()
	st.PlanMatch = []PlanMatch{}
	for rows.Next() {
		var m PlanMatch
		if err := rows.Scan(&m.Recommended, &m.Picked, &m.Count); err != nil {
			return st, err
		}
		st.PlanMatch = append(st.PlanMatch, m)
	}
	irows, err := pool.Query(ctx, `SELECT improvement FROM waitlist_submissions WHERE improvement <> '' ORDER BY created_at DESC LIMIT 30`)
	if err != nil {
		return st, fmt.Errorf("db: improvements: %w", err)
	}
	defer irows.Close()
	st.Improvement = []string{}
	for irows.Next() {
		var s string
		if err := irows.Scan(&s); err != nil {
			return st, err
		}
		st.Improvement = append(st.Improvement, s)
	}
	return st, nil
}

func counts(ctx context.Context, pool *pgxpool.Pool, q string, args ...any) ([]Count, error) {
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: counts: %w", err)
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Value, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListWaitlist returns the latest submissions.
func ListWaitlist(ctx context.Context, pool *pgxpool.Pool, limit, offset int) ([]WaitlistRow, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, name, company, email, phone, sectors, company_size, management, modules, plan_interest,
		        recommended_plan, improvement, created_at
		 FROM waitlist_submissions ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("db: list waitlist: %w", err)
	}
	defer rows.Close()
	out := []WaitlistRow{}
	for rows.Next() {
		var r WaitlistRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Company, &r.Email, &r.Phone, &r.Sectors, &r.CompanySize, &r.Management,
			&r.Modules, &r.PlanInterest, &r.RecommendedPlan, &r.Improvement, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AccountsOverview is AndRho's own customer base.
type AccountsOverview struct {
	Total        int     `json:"total"`
	Last30Days   int     `json:"last_30_days"`
	ByPlan       []Count `json:"by_plan"`
	BySetup      []Count `json:"by_setup"`
	Modules      []Count `json:"modules"`
	Provisioning []Count `json:"provisioning"` // value = kind:status
}

// GetAccountsOverview counts accounts, plans, signup paths, modules and jobs.
func GetAccountsOverview(ctx context.Context, pool *pgxpool.Pool) (AccountsOverview, error) {
	var o AccountsOverview
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE created_at >= now() - interval '30 days') FROM accounts`).
		Scan(&o.Total, &o.Last30Days); err != nil {
		return o, fmt.Errorf("db: accounts overview: %w", err)
	}
	var err error
	if o.ByPlan, err = counts(ctx, pool, `SELECT plan, COUNT(*) FROM accounts GROUP BY 1 ORDER BY 2 DESC`); err != nil {
		return o, err
	}
	if o.BySetup, err = counts(ctx, pool, `SELECT setup, COUNT(*) FROM account_setup GROUP BY 1 ORDER BY 2 DESC`); err != nil {
		return o, err
	}
	if o.Modules, err = counts(ctx, pool, `SELECT m, COUNT(*) FROM account_setup, unnest(modules) m GROUP BY 1 ORDER BY 2 DESC`); err != nil {
		return o, err
	}
	o.Provisioning, err = counts(ctx, pool, `SELECT kind || ':' || status, COUNT(*) FROM provisioning_jobs GROUP BY 1 ORDER BY 1`)
	return o, err
}

// GameStats is the landing's minigame (an experiment on color themes), read
// from the tracker's events for AndRho's own site.
type GameStats struct {
	Sessions      int         `json:"sessions"`
	Registrations int         `json:"registrations"`
	AvgDestroyed  float64     `json:"avg_destroyed"`
	ByTheme       []ThemeStat `json:"by_theme"`
	ByCommerce    []Count     `json:"by_commerce"`
	DiscountTiers []Count     `json:"discount_tiers"`
	Daily         []Count     `json:"daily"`
}

// ThemeStat: how one color theme performed.
type ThemeStat struct {
	Theme      string  `json:"theme"`
	Segments   int     `json:"segments"`
	Destroyed  int     `json:"destroyed"`
	Missed     int     `json:"missed"`
	RatePerSec float64 `json:"rate_per_sec"`
}

// GetGameStats aggregates the game_* events of siteID in the tracker db.
func GetGameStats(ctx context.Context, tracker *pgxpool.Pool, siteID string) (GameStats, error) {
	var g GameStats
	if err := tracker.QueryRow(ctx,
		`SELECT
		   (SELECT COUNT(DISTINCT payload->>'session_id') FROM events WHERE site_id = $1 AND type = 'game_session'),
		   (SELECT COUNT(DISTINCT payload->>'session_id') FROM events WHERE site_id = $1 AND type = 'game_registration'),
		   COALESCE((SELECT ROUND(AVG((payload->>'destroyed')::numeric), 1) FROM events WHERE site_id = $1 AND type = 'game_session'), 0)`,
		siteID).Scan(&g.Sessions, &g.Registrations, &g.AvgDestroyed); err != nil {
		return g, fmt.Errorf("db: game totals: %w", err)
	}
	rows, err := tracker.Query(ctx,
		`SELECT payload->>'theme', COUNT(*), COALESCE(SUM((payload->>'destroyed')::int), 0), COALESCE(SUM((payload->>'missed')::int), 0),
		        COALESCE(ROUND(SUM((payload->>'destroyed')::numeric) / NULLIF(SUM((payload->>'duration_ms')::numeric) / 1000, 0), 3), 0)
		 FROM events WHERE site_id = $1 AND type = 'game_theme_segment' AND payload ? 'theme'
		 GROUP BY 1 ORDER BY 5 DESC`, siteID)
	if err != nil {
		return g, fmt.Errorf("db: game themes: %w", err)
	}
	defer rows.Close()
	g.ByTheme = []ThemeStat{}
	for rows.Next() {
		var t ThemeStat
		if err := rows.Scan(&t.Theme, &t.Segments, &t.Destroyed, &t.Missed, &t.RatePerSec); err != nil {
			return g, err
		}
		g.ByTheme = append(g.ByTheme, t)
	}
	if g.ByCommerce, err = counts(ctx, tracker,
		`SELECT COALESCE(NULLIF(payload->>'commerce_type', ''), 'sin dato'), COUNT(*) FROM events
		 WHERE site_id = $1 AND type = 'game_registration' GROUP BY 1 ORDER BY 2 DESC`, siteID); err != nil {
		return g, err
	}
	if g.DiscountTiers, err = counts(ctx, tracker,
		`SELECT COALESCE(payload->>'discount_percent', '?') || '%', COUNT(*) FROM events
		 WHERE site_id = $1 AND type = 'game_registration' GROUP BY 1 ORDER BY 1`, siteID); err != nil {
		return g, err
	}
	g.Daily, err = counts(ctx, tracker,
		`SELECT to_char(occurred_at::date, 'YYYY-MM-DD'), COUNT(DISTINCT payload->>'session_id') FROM events
		 WHERE site_id = $1 AND type = 'game_session' AND occurred_at >= now() - interval '30 days' GROUP BY 1 ORDER BY 1`, siteID)
	return g, err
}
