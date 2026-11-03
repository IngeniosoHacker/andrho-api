package matching

import (
	"testing"
	"time"
)

func TestPick(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-2 * time.Minute)

	cases := []struct {
		name  string
		skill string
		cands []Candidate
		want  string
	}{
		{"skill beats a free generalist", "web", []Candidate{
			{UserID: "gen", Status: "disponible", LastSeen: fresh},
			{UserID: "web", Skills: []string{"web"}, Status: "disponible", LastSeen: fresh, OpenTasks: 1},
		}, "web"},
		{"an overloaded specialist loses to a free one", "web", []Candidate{
			{UserID: "busy", Skills: []string{"web"}, Status: "disponible", LastSeen: fresh, OpenTasks: 6},
			{UserID: "free", Skills: []string{"web", "diseno"}, Status: "disponible", LastSeen: fresh},
		}, "free"},
		{"someone away loses to someone online", "erp", []Candidate{
			{UserID: "away", Skills: []string{"erp"}, Status: "fuera", LastSeen: fresh},
			{UserID: "here", Skills: []string{"erp"}, Status: "disponible", LastSeen: fresh},
		}, "here"},
		{"knowing the client breaks an otherwise even match", "social", []Candidate{
			{UserID: "new", Skills: []string{"social"}, Status: "disponible", LastSeen: fresh},
			{UserID: "known", Skills: []string{"social"}, Status: "disponible", LastSeen: fresh, KnowsClient: true},
		}, "known"},
		{"a wrong-skill specialist loses to a generalist", "erp", []Candidate{
			{UserID: "designer", Skills: []string{"diseno"}, Status: "disponible", LastSeen: fresh},
			{UserID: "gen", Status: "disponible", LastSeen: fresh},
		}, "gen"},
	}
	for _, c := range cases {
		r, ok := Pick(c.cands, c.skill, now)
		if !ok || r.UserID != c.want {
			t.Errorf("%s: got %q (%v), want %q", c.name, r.UserID, r.Reasons, c.want)
		}
	}
	if _, ok := Pick(nil, "web", now); ok {
		t.Error("no candidates should not match")
	}
}

func TestReliabilityIsSmoothed(t *testing.T) {
	if got := reliabilityScore(0, 0); got != 0.5 {
		t.Errorf("new creator: got %v, want 0.5", got)
	}
	if reliabilityScore(10, 10) <= reliabilityScore(1, 1) {
		t.Error("a long on-time record should beat a single delivery")
	}
}
