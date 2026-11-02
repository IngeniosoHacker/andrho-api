// Package rstats runs the R scripts in stats/ (multifactor ANOVA, rank
// trends) for the insights pipeline: JSON on stdin, JSON on stdout.
//
// In production R runs on AndRho's VPS, over the same SSH connection that
// provisions the Odoo containers (internal/vps): the scripts are installed
// in VPS_SCRIPTS_DIR/stats/ next to odoo/ and whatsapp/, and
// stats/run-r.sh <name> runs <name>.R with the payload on stdin. The API on
// Railway needs no R of its own.
//
// Without the VPS (local development, tests) it falls back to a local
// Rscript (RSCRIPT_BIN, default "Rscript"; scripts in R_SCRIPTS_DIR, default
// "stats"). With neither, Available() is false and callers keep their plain
// Go statistics, so the panel never breaks for lack of R.
package rstats

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/IngeniosoHacker/andrho-api/internal/vps"
)

// Client runs the analyses on the VPS when it's configured, locally otherwise.
type Client struct {
	VPS *vps.Runner
}

var (
	once     sync.Once
	localBin string
	nameRe   = regexp.MustCompile(`^[a-z0-9_]+$`)
)

func local() string {
	once.Do(func() {
		name := os.Getenv("RSCRIPT_BIN")
		if name == "" {
			name = "Rscript"
		}
		if p, err := exec.LookPath(name); err == nil {
			localBin = p
		}
	})
	return localBin
}

// Available reports whether R can be run (VPS or local).
func (c *Client) Available() bool {
	return c != nil && (c.VPS.Enabled() || local() != "")
}

// Run executes the analysis `name` (stats/<name>.R) with in as JSON on stdin
// and decodes its output into out.
func (c *Client) Run(ctx context.Context, name string, in, out any, timeout time.Duration) error {
	if !c.Available() {
		return fmt.Errorf("rstats: no R available (VPS_HOST unset and no local Rscript)")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("rstats: bad script name %q", name)
	}
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("rstats: encode input: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout string
	if c.VPS.Enabled() {
		stdout, err = c.VPS.RunInput(ctx, "stats/run-r.sh", body, name)
		if err != nil {
			return fmt.Errorf("rstats: %s on the VPS: %w", name, err)
		}
	} else {
		stdout, err = runLocal(ctx, name, body)
		if err != nil {
			return err
		}
	}
	if err := json.Unmarshal([]byte(lastLine(stdout)), out); err != nil {
		return fmt.Errorf("rstats: %s: decode output: %w", name, err)
	}
	return nil
}

func runLocal(ctx context.Context, name string, body []byte) (string, error) {
	dir := os.Getenv("R_SCRIPTS_DIR")
	if dir == "" {
		dir = "stats"
	}
	cmd := exec.CommandContext(ctx, local(), "--vanilla", filepath.Join(dir, name+".R"))
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return "", fmt.Errorf("rstats: %s: %w: %s", name, err, msg)
	}
	return stdout.String(), nil
}

// The scripts print one JSON document; anything before it (an SSH banner, a
// stray message) is ignored.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}
