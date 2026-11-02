package insights

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IngeniosoHacker/andrho-api/internal/rstats"
)

// Integration test of the Web section's R analyses against a real tracker
// database. Opt-in: needs Rscript and a seeded WebTracker database, e.g.
//
//	INSIGHTS_TRACKER_URL=postgres://... INSIGHTS_SITE_ID=demo-site \
//	R_SCRIPTS_DIR=$PWD/stats go test ./internal/insights -run WebDeep -v
func TestWebDeepIntegration(t *testing.T) {
	url, site := os.Getenv("INSIGHTS_TRACKER_URL"), os.Getenv("INSIGHTS_SITE_ID")
	stats := &rstats.Client{}
	if url == "" || site == "" || !stats.Available() {
		t.Skip("set INSIGHTS_TRACKER_URL, INSIGHTS_SITE_ID and install R to run")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var a analysis
	webDeep(ctx, Sources{Tracker: pool, SiteID: site, Stats: stats}, &a)
	if !a.hasData || len(a.findings) == 0 {
		t.Fatalf("expected findings from the R analyses, got %+v", a)
	}
	for _, f := range a.findings {
		t.Logf("[%s/%s] %s", f.Kind, f.Severity, f.Text)
	}
	for _, p := range a.proposals {
		t.Logf("PROPUESTA %q: %s\n    evidencia: %s", p.Title, p.Body, p.Evidence)
		if !strings.Contains(p.Evidence, "p") {
			t.Errorf("proposal %q has no statistical evidence", p.Title)
		}
	}
}
