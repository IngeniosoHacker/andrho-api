// Package vps runs AndRho's provisioning scripts (the VPS/ directory: odoo/,
// whatsapp/) on the remote VPS over SSH. The API does not live on the VPS;
// everything about reaching it comes from the environment:
//
//	VPS_HOST, VPS_PORT (22), VPS_USER, VPS_SSH_KEY (private key, PEM or
//	base64 of it), VPS_HOST_KEY (the server's public key line, as in
//	known_hosts), VPS_SCRIPTS_DIR (/opt/andrho/VPS)
//
// Without VPS_HOST the runner is disabled and provisioning jobs wait
// (status esperando_vps) until it's configured.
package vps

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrDisabled means VPS_HOST is not set.
var ErrDisabled = errors.New("vps: VPS_HOST is not set")

// Config is the VPS access, from the environment.
type Config struct {
	Host, Port, User, PrivateKey, HostKey, ScriptsDir string
}

// Runner executes scripts on the VPS.
type Runner struct {
	cfg      Config
	signer   ssh.Signer
	hostCB   ssh.HostKeyCallback
	hostAlgs []string
}

// New validates cfg. A config without Host gives a disabled runner; one with
// Host but a bad key or no host key is an error, so a misconfiguration shows
// at boot, not at the first signup.
func New(cfg Config) (*Runner, error) {
	if cfg.Host == "" {
		return &Runner{}, nil
	}
	if cfg.Port == "" {
		cfg.Port = "22"
	}
	if cfg.ScriptsDir == "" {
		cfg.ScriptsDir = "/opt/andrho/VPS"
	}
	keyPEM := []byte(cfg.PrivateKey)
	if !bytes.Contains(keyPEM, []byte("PRIVATE KEY")) {
		if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.PrivateKey)); err == nil {
			keyPEM = decoded
		}
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("vps: VPS_SSH_KEY: %w", err)
	}
	if cfg.HostKey == "" {
		return nil, errors.New("vps: VPS_HOST_KEY is required (the server's key line from known_hosts / ssh-keyscan)")
	}
	_, _, hostKey, _, _, err := ssh.ParseKnownHosts([]byte(fieldsAfterHost(cfg.HostKey)))
	if err != nil {
		return nil, fmt.Errorf("vps: VPS_HOST_KEY: %w", err)
	}
	// Ask the server for the configured key's type; otherwise it may present
	// another of its keys and the fixed-key check fails.
	algs := []string{hostKey.Type()}
	if hostKey.Type() == ssh.KeyAlgoRSA {
		algs = []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return &Runner{cfg: cfg, signer: signer, hostCB: ssh.FixedHostKey(hostKey), hostAlgs: algs}, nil
}

// fieldsAfterHost accepts "ssh-ed25519 AAAA..." or a full known_hosts line
// "host ssh-ed25519 AAAA..." and returns a known_hosts line for any host.
func fieldsAfterHost(line string) string {
	f := strings.Fields(line)
	if len(f) >= 3 && !strings.HasPrefix(f[0], "ssh-") && !strings.HasPrefix(f[0], "ecdsa-") {
		f = f[1:]
	}
	return "* " + strings.Join(f, " ")
}

// Enabled reports whether scripts can run.
func (r *Runner) Enabled() bool { return r != nil && r.cfg.Host != "" }

// Arguments are single-quoted anyway; this keeps out quotes, `, $ and \
// as a second line of defense while allowing names like "Panadería Doña Ana".
var safeArg = regexp.MustCompile(`^[\p{L}\p{N}@._+:/ ,&()-]{0,200}$`)

// quote single-quotes one argument for the remote shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Run executes VPS_SCRIPTS_DIR/<script> with args and returns its stdout.
// script is a path inside the scripts dir (e.g. "odoo/provision-client.sh");
// args are validated and quoted, env is passed as VAR=value prefixes.
func (r *Runner) Run(ctx context.Context, script string, env map[string]string, args ...string) (string, error) {
	return r.RunLines(ctx, script, env, nil, args...)
}

// RunLines is Run, calling onLine with each line of stdout as it arrives
// (the scripts' PROGRESS lines).
func (r *Runner) RunLines(ctx context.Context, script string, env map[string]string, onLine func(string), args ...string) (string, error) {
	if !r.Enabled() {
		return "", ErrDisabled
	}
	if !regexp.MustCompile(`^[a-z0-9_-]+/[a-z0-9_-]+\.sh$`).MatchString(script) {
		return "", fmt.Errorf("vps: bad script name %q", script)
	}
	cmd := []string{}
	for k, v := range env {
		if !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(k) || !safeArg.MatchString(v) {
			return "", fmt.Errorf("vps: bad env %s", k)
		}
		cmd = append(cmd, k+"="+quote(v))
	}
	cmd = append(cmd, "bash", quote(r.cfg.ScriptsDir+"/"+script))
	for _, a := range args {
		if !safeArg.MatchString(a) {
			return "", fmt.Errorf("vps: bad argument %q", a)
		}
		cmd = append(cmd, quote(a))
	}

	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(r.cfg.Host, r.cfg.Port))
	if err != nil {
		return "", fmt.Errorf("vps: dial: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, r.cfg.Host, &ssh.ClientConfig{
		User: r.cfg.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(r.signer)}, HostKeyCallback: r.hostCB,
		HostKeyAlgorithms: r.hostAlgs, Timeout: 15 * time.Second,
	})
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("vps: ssh: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("vps: session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout, session.Stderr = &lineWriter{all: &stdout, onLine: onLine}, &stderr
	done := make(chan error, 1)
	go func() { done <- session.Run(strings.Join(cmd, " ")) }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return stdout.String(), ctx.Err()
	case err := <-done:
		if err != nil {
			return stdout.String(), fmt.Errorf("vps: %s: %w: %s", script, err, tail(stderr.String(), 400))
		}
		return stdout.String(), nil
	}
}

// lineWriter keeps everything written and hands each complete line to
// onLine.
type lineWriter struct {
	all     *bytes.Buffer
	onLine  func(string)
	pending []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.all.Write(p)
	if w.onLine == nil {
		return len(p), nil
	}
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.onLine(strings.TrimRight(string(w.pending[:i]), "\r"))
		w.pending = w.pending[i+1:]
	}
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// LastJSONLine returns the last line of out that looks like a JSON object:
// the scripts print their result as their final line.
func LastJSONLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") && strings.HasSuffix(l, "}") {
			return l
		}
	}
	return ""
}
