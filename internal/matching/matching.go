// Package matching picks the Creator for a piece of work: an accepted
// suggestion, a customer's website. Every Creator gets a score from five
// signals, each in [0, 1], and the highest score wins:
//
//	skill        3.0  does the work's skill appear in their skills? (a
//	                  generalist, with no skills set, counts 0.4)
//	availability 1.5  their dashboard status and last heartbeat: available
//	                  and seen in the last 15 min = 1, busy = 0.5, away or
//	                  not seen in a day = 0.1
//	load        -1.5  open tasks they already hold, 0 → 0 … 4+ → 1
//	reliability  1.0  share of their delivered tasks that were on time,
//	                  smoothed ((on time + 1) / (delivered + 2)) so a new
//	                  Creator starts at 0.5, not at 0 or 1
//	familiarity  1.0  they've worked for this account before
//
// Ties go to the Creator with fewer open tasks. The reasons behind the win
// are kept with the task (creator_tasks.match) so an admin can see why.
package matching

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	wSkill        = 3.0
	wAvailability = 1.5
	wLoad         = 1.5
	wReliability  = 1.0
	wFamiliarity  = 1.0
)

// Candidate is one Creator and the facts the score reads.
type Candidate struct {
	UserID      string
	Name        string
	Skills      []string
	Status      string // user_presence.status: disponible, almorzando, en_reunion, en_ruta, fuera
	LastSeen    time.Time
	OpenTasks   int
	Delivered   int
	OnTime      int
	KnowsClient bool
}

// Result is the chosen Creator and why.
type Result struct {
	UserID  string   `json:"user_id"`
	Name    string   `json:"name"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
	// Considered is how many Creators were scored.
	Considered int `json:"considered"`
}

func skillScore(skills []string, skill string) float64 {
	if len(skills) == 0 {
		return 0.4
	}
	for _, s := range skills {
		if s == skill {
			return 1
		}
	}
	return 0
}

func availabilityScore(status string, lastSeen, now time.Time) float64 {
	if lastSeen.IsZero() || now.Sub(lastSeen) > 24*time.Hour || status == "fuera" {
		return 0.1
	}
	if status != "disponible" {
		return 0.5
	}
	if now.Sub(lastSeen) <= 15*time.Minute {
		return 1
	}
	return 0.7
}

func loadScore(open int) float64 { return math.Min(1, float64(open)/4) }

func reliabilityScore(delivered, onTime int) float64 {
	return float64(onTime+1) / float64(delivered+2)
}

// Score returns c's score for work needing skill, and the reasons worth
// telling.
func Score(c Candidate, skill string, now time.Time) (float64, []string) {
	sk := skillScore(c.Skills, skill)
	av := availabilityScore(c.Status, c.LastSeen, now)
	ld := loadScore(c.OpenTasks)
	rl := reliabilityScore(c.Delivered, c.OnTime)
	fm := 0.0
	if c.KnowsClient {
		fm = 1
	}
	score := wSkill*sk + wAvailability*av - wLoad*ld + wReliability*rl + wFamiliarity*fm
	reasons := []string{}
	switch {
	case sk == 1:
		reasons = append(reasons, "domina "+skill)
	case sk > 0:
		reasons = append(reasons, "hace de todo")
	}
	if av == 1 {
		reasons = append(reasons, "disponible ahora")
	}
	if c.OpenTasks == 0 {
		reasons = append(reasons, "sin tareas pendientes")
	} else {
		reasons = append(reasons, fmt.Sprintf("%d tareas pendientes", c.OpenTasks))
	}
	if c.Delivered >= 3 && rl >= 0.75 {
		reasons = append(reasons, "entrega a tiempo")
	}
	if c.KnowsClient {
		reasons = append(reasons, "ya trabajó con este cliente")
	}
	return math.Round(score*100) / 100, reasons
}

// Pick chooses among candidates; ok=false when there are none.
func Pick(cands []Candidate, skill string, now time.Time) (Result, bool) {
	if len(cands) == 0 {
		return Result{}, false
	}
	type scored struct {
		c       Candidate
		s       float64
		reasons []string
	}
	all := make([]scored, 0, len(cands))
	for _, c := range cands {
		s, r := Score(c, skill, now)
		all = append(all, scored{c, s, r})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].s != all[j].s {
			return all[i].s > all[j].s
		}
		return all[i].c.OpenTasks < all[j].c.OpenTasks
	})
	w := all[0]
	return Result{UserID: w.c.UserID, Name: w.c.Name, Score: w.s, Reasons: w.reasons, Considered: len(all)}, true
}

// Candidates loads every Creator with the facts the score needs, for work
// on accountID.
func Candidates(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]Candidate, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.user_id::text, COALESCE(NULLIF(u.display_name, ''), u.email), s.skills,
		       COALESCE(p.status, 'disponible'), p.last_seen_at,
		       COUNT(t.id) FILTER (WHERE t.status = 'aceptada'),
		       COUNT(t.id) FILTER (WHERE t.status = 'entregada'),
		       COUNT(t.id) FILTER (WHERE t.status = 'entregada' AND (t.due_at IS NULL OR t.updated_at <= t.due_at)),
		       COALESCE(bool_or(t.account_id = $1), false)
		FROM staff s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN user_presence p ON p.user_id = s.user_id
		LEFT JOIN creator_tasks t ON t.creator_id = s.user_id
		WHERE s.role = 'creator'
		GROUP BY s.user_id, u.display_name, u.email, s.skills, p.status, p.last_seen_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("matching: candidates: %w", err)
	}
	defer rows.Close()
	out := []Candidate{}
	for rows.Next() {
		var c Candidate
		var seen *time.Time
		if err := rows.Scan(&c.UserID, &c.Name, &c.Skills, &c.Status, &seen, &c.OpenTasks, &c.Delivered, &c.OnTime, &c.KnowsClient); err != nil {
			return nil, err
		}
		if seen != nil {
			c.LastSeen = *seen
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Assign loads the Creators and picks one for skill on accountID.
func Assign(ctx context.Context, pool *pgxpool.Pool, accountID, skill string) (Result, bool, error) {
	cands, err := Candidates(ctx, pool, accountID)
	if err != nil {
		return Result{}, false, err
	}
	r, ok := Pick(cands, skill, time.Now())
	return r, ok, nil
}
