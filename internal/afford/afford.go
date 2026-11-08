// Package afford estimates whether a business can pay each plan, from the
// size of its team, its sector and the budget it says it has today
// (stats/afford.R, a Bayesian estimate of its monthly software capacity).
// The signup's suggestion uses it so a 2-person business that asks for
// everything isn't pushed to Galaxy: AndRho recommends the biggest plan it
// can sustain.
//
// R runs on the VPS (internal/rstats); without R the same model runs here
// in Go (assess), so the signup never waits on it.
package afford

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/IngeniosoHacker/andrho-api/internal/plans"
	"github.com/IngeniosoHacker/andrho-api/internal/rstats"
)

// Model parameters (assumptions, sent to the R script as they are here).
const (
	// Base is the prior median capacity of one person, quetzales al mes;
	// Elasticity how it grows with the team (n^0.75: 2 people ≈ Q235,
	// 8 ≈ Q665, 25 ≈ Q1,565); SD the prior's spread in log units.
	Base       = 140.0
	Elasticity = 0.75
	SD         = 0.9
	// Trust is how much of the posterior stays inside the budget the
	// person gave (spread evenly in quetzales); Spread how fast it fades
	// outside it (log units).
	Trust  = 0.8
	Spread = 0.35
	// Threshold: a plan is "alcanzable" when it's more likely than not.
	Threshold = 0.5
)

// sectorFactor shifts the prior by sector (spending on tools).
var sectorFactor = map[string]float64{
	"manufactura": 1.2, "salud": 1.15, "logistica": 1.1, "comercio": 1, "servicios": 0.95, "restaurantes": 0.9, "otro": 1,
}

// teamOf is the head count, or the middle of the survey's range.
func teamOf(n int, size string) int {
	if n > 0 {
		return n
	}
	return map[string]int{"solo": 1, "1-10": 5, "11-50": 25, "51-200": 100, "200+": 300}[size]
}

// Input is what's known of the business.
type Input struct {
	TeamSize    int
	CompanySize string
	AccessUsers int
	Sectors     []string
	Budget      string // plans.Budget id, "" when not answered
	WantsSite   bool   // the analysis says it needs a website
}

// PlanOdds is one plan for this business.
type PlanOdds struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Cost     int     `json:"cost"`      // what it would pay a month (users and site upkeep included)
	P        float64 `json:"p"`         // P(capacity >= cost)
	PStretch float64 `json:"p_stretch"` // P(capacity >= 0.85 cost): "con esfuerzo"
	Fits     bool    `json:"fits"`      // P >= Threshold
}

// Result is the estimate.
type Result struct {
	Capacity struct {
		Median int `json:"median"`
		P10    int `json:"p10"`
		P90    int `json:"p90"`
	} `json:"capacity"`
	Plans       []PlanOdds `json:"plans"`
	Recommended string     `json:"recommended"`
	Method      string     `json:"method"`
	Source      string     `json:"source"` // r | go
	Team        int        `json:"team"`
	Budget      string     `json:"budget,omitempty"`
}

// P returns the odds of a plan (0 when unknown).
func (r Result) P(id string) float64 {
	for _, p := range r.Plans {
		if p.ID == id {
			return p.P
		}
	}
	return 0
}

// costOf is what plan p costs this business a month: its price, the extra
// users it needs over the plan's (at the add-on price of its step; past the
// add-on's cap, the users the plan can't give are priced the same, so a
// plan far too small looks as expensive as it would get), and the site's
// upkeep when it wants a site the plan doesn't include.
func costOf(p plans.Plan, in Input) int {
	cost := p.Price
	if in.AccessUsers > 0 && p.Caps.Users != plans.Unlimited && in.AccessUsers > p.Caps.Users {
		extra := in.AccessUsers - p.Caps.Users
		per := 0
		for _, a := range plans.Addons(p.ID) {
			if a.FeatureID == "usuario_extra" {
				per = a.Price
			}
		}
		if per == 0 { // the step sells no single users: the next plan's
			per = 60
		}
		cost += extra * per
	}
	if in.WantsSite {
		if _, has := plans.IncludedSite(p.ID); !has {
			cost += plans.SiteLevels[0].Monthly
		}
	}
	return cost
}

type rInput struct {
	Team         int           `json:"team"`
	SectorFactor float64       `json:"sector_factor"`
	Base         float64       `json:"base"`
	Elasticity   float64       `json:"elasticity"`
	SD           float64       `json:"sd"`
	Budget       *plans.Budget `json:"budget"`
	Trust        float64       `json:"trust"`
	Spread       float64       `json:"spread"`
	Threshold    float64       `json:"threshold"`
	Plans        []rPlan       `json:"plans"`
}

type rPlan struct {
	ID   string `json:"id"`
	Cost int    `json:"cost"`
}

func build(in Input) rInput {
	team := teamOf(in.TeamSize, in.CompanySize)
	if team < 1 {
		team = 3 // nothing said: a small business
	}
	factor := 1.0
	if len(in.Sectors) > 0 {
		sum := 0.0
		for _, s := range in.Sectors {
			f, ok := sectorFactor[s]
			if !ok {
				f = 1
			}
			sum += f
		}
		factor = sum / float64(len(in.Sectors))
	}
	ri := rInput{Team: team, SectorFactor: factor, Base: Base, Elasticity: Elasticity, SD: SD, Trust: Trust, Spread: Spread, Threshold: Threshold}
	if b, ok := plans.GetBudget(in.Budget); ok {
		ri.Budget = &b
	}
	for _, p := range plans.All() {
		ri.Plans = append(ri.Plans, rPlan{ID: p.ID, Cost: costOf(p, in)})
	}
	return ri
}

// Assess runs the model in R when it can, in Go otherwise.
func Assess(ctx context.Context, r *rstats.Client, in Input) Result {
	ri := build(in)
	var out Result
	source := "go"
	if r.Available() {
		if err := r.Run(ctx, "afford", ri, &out, 8*time.Second); err == nil && len(out.Plans) == len(ri.Plans) {
			source = "r"
		} else {
			if err != nil {
				log.Printf("afford: R failed, using Go: %v", err)
			}
			out = Result{}
		}
	}
	if source == "go" {
		out = assess(ri)
	}
	out.Source, out.Team, out.Budget = source, ri.Team, in.Budget
	for i := range out.Plans {
		p := plans.MustGet(out.Plans[i].ID)
		out.Plans[i].Name = p.Name
		out.Plans[i].Fits = out.Plans[i].P >= Threshold
	}
	return out
}

// assess is stats/afford.R in Go: the same grid, prior, likelihood and rule.
func assess(in rInput) Result {
	const n = 4000
	lo0, hi0 := math.Log(20), math.Log(200000)
	grid := make([]float64, n)
	post := make([]float64, n)
	mu := math.Log(in.Base * in.SectorFactor * math.Pow(float64(in.Team), in.Elasticity))
	sum := 0.0
	for i := range grid {
		grid[i] = lo0 + (hi0-lo0)*float64(i)/float64(n-1)
		z := (grid[i] - mu) / in.SD
		post[i] = math.Exp(-z * z / 2)
		sum += post[i]
	}
	for i := range post {
		post[i] /= sum
	}
	if b := in.Budget; b != nil {
		lo := math.Log(math.Max(float64(b.Min), 20))
		hi := math.Inf(1)
		if b.Max > 0 {
			hi = math.Log(float64(b.Max))
		}
		inside := make([]bool, n)
		outside := make([]float64, n)
		within := make([]float64, n)
		pIn, pOut := 0.0, 0.0
		for i, g := range grid {
			inside[i] = g >= lo && g < hi
			if inside[i] {
				// Even in quetzales (∝ C on the log grid); the open top range keeps the prior.
				within[i] = post[i]
				if !math.IsInf(hi, 1) {
					within[i] = math.Exp(g)
				}
				pIn += within[i]
				continue
			}
			d := lo - g
			if g >= hi {
				d = g - hi
			}
			outside[i] = post[i] * math.Exp(-d*d/(2*in.Spread*in.Spread))
			pOut += outside[i]
		}
		next := make([]float64, n)
		total := 0.0
		for i := range post {
			switch {
			case inside[i] && pIn > 0 && pOut > 0:
				next[i] = within[i] * in.Trust / pIn
			case !inside[i] && pIn > 0 && pOut > 0:
				next[i] = outside[i] * (1 - in.Trust) / pOut
			case inside[i] && pIn > 0:
				next[i] = within[i]
			case pIn == 0:
				next[i] = post[i]
			}
			total += next[i]
		}
		for i := range next {
			post[i] = next[i] / total
		}
	}
	quant := func(q float64) int {
		c := 0.0
		for i, p := range post {
			c += p
			if c >= q {
				return int(math.Round(math.Exp(grid[i])))
			}
		}
		return int(math.Round(math.Exp(grid[n-1])))
	}
	afford := func(cost float64) float64 {
		l := math.Log(math.Max(cost, 1))
		s := 0.0
		for i, g := range grid {
			if g >= l {
				s += post[i]
			}
		}
		return math.Round(s*1000) / 1000
	}
	var r Result
	r.Method = "bayes_grid"
	r.Capacity.Median, r.Capacity.P10, r.Capacity.P90 = quant(0.5), quant(0.1), quant(0.9)
	cheapest := 0
	for i, p := range in.Plans {
		po := PlanOdds{ID: p.ID, Cost: p.Cost, P: afford(float64(p.Cost)), PStretch: afford(0.85 * float64(p.Cost))}
		r.Plans = append(r.Plans, po)
		if po.P >= in.Threshold {
			r.Recommended = p.ID
		}
		if p.Cost < in.Plans[cheapest].Cost {
			cheapest = i
		}
	}
	if r.Recommended == "" && len(in.Plans) > 0 {
		r.Recommended = in.Plans[cheapest].ID
	}
	return r
}

// Reason says, in plain words for the owner, why the recommendation (no
// statistics: those stay in the result for AndRho).
func Reason(r Result, bestFit string) string {
	rec := plans.MustGet(r.Recommended)
	if plans.Rank(r.Recommended) >= plans.Rank(bestFit) {
		return ""
	}
	who := "tu negocio"
	if r.Team > 0 {
		who = fmt.Sprintf("un equipo de %d", r.Team)
		if r.Team == 1 {
			who = "un negocio de una persona"
		}
	}
	if r.Budget != "" {
		return fmt.Sprintf("Con lo que puedes invertir hoy y %s, %s es lo que mejor puedes sostener; cuando crezcas, subes de plan sin perder nada.", who, rec.Name)
	}
	return fmt.Sprintf("Para %s, %s es lo que suele poder sostenerse; cuando crezcas, subes de plan sin perder nada.", who, rec.Name)
}
