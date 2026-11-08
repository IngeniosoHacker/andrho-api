package afford

import (
	"context"
	"math"
	"os"
	"os/exec"
	"testing"

	"github.com/IngeniosoHacker/andrho-api/internal/rstats"
)

func TestTwoPeopleWantingEverything(t *testing.T) {
	r := Assess(context.Background(), nil, Input{TeamSize: 2, AccessUsers: 2, Sectors: []string{"comercio"}, Budget: "349_750", WantsSite: true})
	if r.Source != "go" || r.Recommended == "galaxy" || r.P("galaxy") > 0.05 {
		t.Fatalf("2 people: %+v", r)
	}
	if r.Recommended != "plataforma" && r.Recommended != "base" {
		t.Errorf("recommended %s: %+v", r.Recommended, r.Plans)
	}
	if Reason(r, "galaxy") == "" {
		t.Error("no reason when recommending below the fit")
	}
}

func TestBigBudgetBigTeam(t *testing.T) {
	r := Assess(context.Background(), nil, Input{TeamSize: 60, AccessUsers: 20, Sectors: []string{"manufactura"}, Budget: "desde_2990"})
	if r.Recommended != "galaxy" || r.P("galaxy") < 0.6 {
		t.Fatalf("%+v", r)
	}
	if Reason(r, "galaxy") != "" {
		t.Error("reason when the fit is affordable")
	}
}

func TestBudgetMovesTheEstimate(t *testing.T) {
	low := Assess(context.Background(), nil, Input{TeamSize: 8, Budget: "hasta_120"})
	high := Assess(context.Background(), nil, Input{TeamSize: 8, Budget: "1200_2990"})
	none := Assess(context.Background(), nil, Input{TeamSize: 8})
	if !(low.Capacity.Median < none.Capacity.Median && none.Capacity.Median < high.Capacity.Median) {
		t.Fatalf("medians %d %d %d", low.Capacity.Median, none.Capacity.Median, high.Capacity.Median)
	}
	// Probabilities never grow with the price.
	for i := 1; i < len(none.Plans); i++ {
		if none.Plans[i].P > none.Plans[i-1].P+1e-9 && none.Plans[i].Cost >= none.Plans[i-1].Cost {
			t.Errorf("monotonic: %+v", none.Plans)
		}
	}
}

func TestUsersRaiseTheCost(t *testing.T) {
	in := build(Input{TeamSize: 10, AccessUsers: 10})
	for _, p := range in.Plans {
		if p.ID == "despegue" && p.Cost != 750+4*15 { // 6 included, 4 extra at Q15
			t.Fatalf("despegue cost %d", p.Cost)
		}
	}
}

// The R script and the Go fallback agree.
func TestRMatchesGo(t *testing.T) {
	if _, err := exec.LookPath("Rscript"); err != nil {
		t.Skip("no Rscript")
	}
	os.Setenv("R_SCRIPTS_DIR", "../../stats")
	defer os.Unsetenv("R_SCRIPTS_DIR")
	for _, in := range []Input{
		{TeamSize: 2, AccessUsers: 2, Sectors: []string{"comercio"}, Budget: "349_750", WantsSite: true},
		{TeamSize: 40, Sectors: []string{"salud", "servicios"}, Budget: "desde_2990"},
		{CompanySize: "11-50"},
	} {
		r := Assess(context.Background(), &rstats.Client{}, in)
		g := assess(build(in))
		if r.Source != "r" {
			t.Fatalf("R not used: %+v", r)
		}
		if r.Recommended != g.Recommended || math.Abs(float64(r.Capacity.Median-g.Capacity.Median)) > 2 {
			t.Errorf("R %+v vs Go %+v", r, g)
		}
		for i := range g.Plans {
			if math.Abs(r.Plans[i].P-g.Plans[i].P) > 0.002 {
				t.Errorf("%s: R %v Go %v", g.Plans[i].ID, r.Plans[i].P, g.Plans[i].P)
			}
		}
	}
}
