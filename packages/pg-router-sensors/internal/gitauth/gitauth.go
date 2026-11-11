// Package gitauth implements git-auth-sensor: the resource git_auth_<remote>
// is AVAILABLE only while the ssh certificate selected by a configured key id
// or principal is valid with at least a margin left, and, when ls_remote_url
// is set, a bounded `git ls-remote` succeeds.
//
// The certificate comes from `ssh-add -L | ssh-keygen -L -f -` run with
// TZ=UTC; ssh-keygen's exit status is NOT usable (a plain key prints "is not a
// certificate"), so the text is parsed. The unknown value of a credential is
// the WORST level: every failure to read the agent answers UNAVAILABLE.
//
// Config keys: resource (required, git_auth_<remote>), exactly one of key_id
// or principal, margin (duration, default 30m), ls_remote_url (optional).
// The pull query's environment allowlist is SSH_AUTH_SOCK.
//
// Exported names: Sensor, New, Cert, ParseCerts, and the argv constants.
package gitauth

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/originprobe"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// The only argv the sensor runs.
const (
	SSHAddName    = "ssh-add"
	SSHKeygenName = "ssh-keygen"
)

// SSHAddArgs and SSHKeygenArgs are the fixed arguments of the two ssh probes.
var (
	SSHAddArgs    = []string{"-L"}
	SSHKeygenArgs = []string{"-L", "-f", "-"}
)

// Defaults and bounds.
const (
	DefaultMargin   = 30 * time.Minute
	LsRemoteTimeout = 10 * time.Second
	probeTimeout    = 5 * time.Second
	reasonUnit      = "s_left"
)

// Cert is one certificate parsed from ssh-keygen -L output.
type Cert struct {
	KeyID      string
	Principals []string
	// ValidFrom is the zero time for "from the beginning of time".
	ValidFrom time.Time
	// ValidTo is the zero time for "forever".
	ValidTo time.Time
}

// Forever reports whether the certificate never expires.
func (c Cert) Forever() bool { return c.ValidTo.IsZero() }

const tsLayout = "2006-01-02T15:04:05"

// ParseCerts parses `ssh-keygen -L -f -` output (produced with TZ=UTC). Blocks
// that are not certificates ("is not a certificate") are skipped.
func ParseCerts(out string) []Cert {
	var certs []Cert
	var cur *Cert
	inPrincipals := false
	flush := func() {
		if cur != nil {
			certs = append(certs, *cur)
			cur = nil
		}
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(line, "(stdin)") || (!strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && strings.Contains(line, "):")) {
			flush()
			inPrincipals = false
			if !strings.Contains(trim, "is not a certificate") && strings.HasSuffix(trim, ":") {
				cur = &Cert{}
			}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(trim, "Key ID:"):
			inPrincipals = false
			cur.KeyID = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trim, "Key ID:")), `"`)
		case strings.HasPrefix(trim, "Valid:"):
			inPrincipals = false
			cur.ValidFrom, cur.ValidTo = parseValid(strings.TrimSpace(strings.TrimPrefix(trim, "Valid:")))
		case strings.HasPrefix(trim, "Principals:"):
			inPrincipals = true
			rest := strings.TrimSpace(strings.TrimPrefix(trim, "Principals:"))
			if rest != "" && rest != "(none)" {
				cur.Principals = append(cur.Principals, rest)
			}
		case strings.HasSuffix(trim, ":") || strings.Contains(trim, ": "):
			inPrincipals = false
		case inPrincipals && trim != "":
			cur.Principals = append(cur.Principals, trim)
		}
	}
	flush()
	return certs
}

// parseValid reads "forever", "from T to T", "from T" or "to T".
func parseValid(s string) (from, to time.Time) {
	f := strings.Fields(s)
	for i := 0; i+1 < len(f); i++ {
		t, err := time.ParseInLocation(tsLayout, f[i+1], time.UTC)
		if err != nil {
			continue
		}
		switch f[i] {
		case "from":
			from = t
		case "to":
			to = t
		}
	}
	return from, to
}

// Sensor is the git-auth-sensor probe set.
type Sensor struct {
	// Run is the command seam (production: cmdrun.Exec).
	Run         cmdrun.Runner
	Resource    string
	KeyID       string
	Principal   string
	Margin      time.Duration
	LsRemoteURL string
}

// New returns a Sensor on the production command runner. It answers nothing
// until configured.
func New() *Sensor { return &Sensor{Run: cmdrun.Exec, Margin: DefaultMargin} }

// Resources implements sensorcli.Sensor.
func (s *Sensor) Resources() []string {
	if s.Resource == "" {
		return nil
	}
	return []string{s.Resource}
}

// Bands implements sensorcli.Sensor: a two-level scale has no bands.
func (s *Sensor) Bands() []levels.Info {
	if s.Resource == "" {
		return nil
	}
	return []levels.Info{levels.NoBands(s.Resource, reasonUnit)}
}

// Configure implements sensorcli.Sensor.
func (s *Sensor) Configure(cfg sensorcli.Config) error {
	if err := cfg.CheckKeys("resource", "key_id", "principal", "margin", "ls_remote_url"); err != nil {
		return err
	}
	res, err := cfg.String("resource", "")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(res, "git_auth_") || len(res) == len("git_auth_") {
		return fmt.Errorf("config key resource: want git_auth_<remote>, got %q", res)
	}
	keyID, err := cfg.String("key_id", "")
	if err != nil {
		return err
	}
	principal, err := cfg.String("principal", "")
	if err != nil {
		return err
	}
	if (keyID == "") == (principal == "") {
		return fmt.Errorf("config: exactly one of key_id or principal is required")
	}
	margin, err := cfg.Duration("margin", DefaultMargin)
	if err != nil {
		return err
	}
	url, err := cfg.String("ls_remote_url", "")
	if err != nil {
		return err
	}
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("config key ls_remote_url: %q must not start with '-'", url)
	}
	s.Resource, s.KeyID, s.Principal, s.Margin, s.LsRemoteURL = res, keyID, principal, margin, url
	return nil
}

// pick returns the matching certificate with the latest expiry.
func (s *Sensor) pick(certs []Cert) (Cert, bool) {
	var best Cert
	found := false
	for _, c := range certs {
		match := false
		if s.KeyID != "" {
			match = c.KeyID == s.KeyID
		} else {
			for _, p := range c.Principals {
				if p == s.Principal {
					match = true
				}
			}
		}
		if !match {
			continue
		}
		if !found || later(c, best) {
			best, found = c, true
		}
	}
	return best, found
}

func later(a, b Cert) bool {
	if a.Forever() {
		return !b.Forever()
	}
	return !b.Forever() && a.ValidTo.After(b.ValidTo)
}

// Measure implements sensorcli.Sensor. Every failure to establish validity
// answers UNAVAILABLE (the unknown value of a credential is the worst level).
func (s *Sensor) Measure(ctx context.Context, env sensorcli.Env) ([]levels.Observation, error) {
	if s.Resource == "" {
		return nil, levels.ErrNoReading
	}
	if env.Resource != "" && env.Resource != s.Resource {
		return nil, levels.ErrNoReading
	}
	level, left, why := s.verdict(ctx, env)
	if why != "" {
		fmt.Fprintf(env.Stderr, "git-auth-sensor: %s: %s\n", s.Resource, why)
	}
	return []levels.Observation{{
		Resource:    s.Resource,
		Scale:       levels.Binary,
		Level:       level,
		Reading:     &levels.Reading{Value: left.Seconds(), Unit: reasonUnit},
		BypassDwell: env.Refresh,
	}}, nil
}

func (s *Sensor) verdict(ctx context.Context, env sensorcli.Env) (string, time.Duration, string) {
	bad := levels.Binary.Worst()
	sock := env.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return bad, 0, "SSH_AUTH_SOCK is missing or empty"
	}
	agentEnv := []string{"SSH_AUTH_SOCK=" + sock}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	add := s.Run(cctx, cmdrun.Call{Name: SSHAddName, Args: SSHAddArgs, Env: agentEnv, OwnProcGroup: true})
	if !add.OK() {
		return bad, 0, fmt.Sprintf("ssh-add -L failed: %s", failText(add))
	}
	if strings.TrimSpace(string(add.Stdout)) == "" {
		return bad, 0, "ssh-add -L lists no identity"
	}
	kg := s.Run(cctx, cmdrun.Call{Name: SSHKeygenName, Args: SSHKeygenArgs, Stdin: add.Stdout, Env: []string{"TZ=UTC"}, OwnProcGroup: true})
	if kg.StartErr != nil || kg.CtxErr != nil {
		return bad, 0, fmt.Sprintf("ssh-keygen -L failed: %s", failText(kg))
	}
	cert, ok := s.pick(ParseCerts(string(kg.Stdout)))
	if !ok {
		return bad, 0, "no certificate matches the configured key id or principal"
	}
	now := env.Now
	if !cert.ValidFrom.IsZero() && now.Before(cert.ValidFrom) {
		return bad, 0, "certificate is not yet valid"
	}
	var left time.Duration
	if cert.Forever() {
		left = 1<<62 - 1
	} else {
		left = cert.ValidTo.Sub(now)
		if left < s.Margin {
			if left < 0 {
				left = 0
			}
			return bad, left, fmt.Sprintf("certificate has %s left, margin %s", left.Truncate(time.Second), s.Margin)
		}
	}
	if s.LsRemoteURL != "" {
		if why := s.lsRemote(ctx); why != "" {
			return bad, left, why
		}
	}
	return levels.Binary.Best(), left, ""
}

// lsRemote runs the bounded, non-interactive probe (the copied originprobe, on
// the same command seam); "" means success.
func (s *Sensor) lsRemote(ctx context.Context) string {
	class, tail := originprobe.Probe(ctx, s.Run, "", s.LsRemoteURL, LsRemoteTimeout)
	if class == originprobe.OK {
		return ""
	}
	return fmt.Sprintf("git ls-remote %s: %s", class, tail)
}

func failText(r cmdrun.Result) string {
	switch {
	case r.StartErr != nil:
		return r.StartErr.Error()
	case r.CtxErr != nil:
		return r.CtxErr.Error()
	}
	return fmt.Sprintf("exit %d: %s", r.Code, strings.TrimSpace(string(r.Stderr)))
}
