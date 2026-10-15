package keeper

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-keep-awake/internal/lease"
)

// External-holder probe constants.
const (
	// PmsetPath is the absolute path of pmset; it is never resolved via PATH.
	PmsetPath = "/usr/bin/pmset"
	// ExternalProbeTimeout bounds one `pmset -g assertions` run, so a wedged
	// powerd cannot stall a keeper tick.
	ExternalProbeTimeout = 5 * time.Second
)

// ExternalProbe lists the power-assertion holders other than this tool's own
// caffeinate. It is best effort and fails soft: any failure is an empty list.
type ExternalProbe struct {
	// Path is the pmset binary; empty means PmsetPath. Tests point it at a
	// fixture script.
	Path string
	// Timeout bounds one run; zero or less means ExternalProbeTimeout.
	Timeout time.Duration
}

// Probe runs `pmset -g assertions` and returns the holders whose pid is not
// ownCaffeinatePID (the caffeinate pid recorded in assertion.json; 0 excludes
// nothing). A start failure, a non-zero exit, a timeout or output that yields
// no entry gives an empty, non-nil list; it never returns an error, so the
// tick cannot fail on it.
func (p ExternalProbe) Probe(ownCaffeinatePID int) []lease.StatusExternal {
	path := p.Path
	if path == "" {
		path = PmsetPath
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = ExternalProbeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-g", "assertions")
	// Do not wait on a grandchild that inherited the pipe after the kill.
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return []lease.StatusExternal{}
	}
	return ParseAssertions(string(out), ownCaffeinatePID)
}

// holderLine matches an entry of the "Listed by owning process" section:
//
//	pid 419(powerd): [0x00025af00001860e] 00:01:35 PreventUserIdleSystemSleep named: "..."
//
// The process name may itself contain spaces or parentheses, so it is matched
// greedily up to the "): [0x" that follows it.
var holderLine = regexp.MustCompile(`^\s*pid (\d+)\((.*)\): \[0x[0-9A-Fa-f]+\] (\S+) (\w+) named:`)

// ParseAssertions parses `pmset -g assertions` output into the holders other
// than ownCaffeinatePID. process is the name in parentheses, type is the
// assertion-type column verbatim, and age_s is the age column (H:MM:SS, or
// D+H:MM:SS for a day or more) in seconds. A line that looks like an entry but
// does not parse is skipped without emptying the list; output with no
// parsable entry gives an empty, non-nil list. Only the one-line entry header
// is read; the indented detail lines and the kernel-assertion lines are
// ignored.
func ParseAssertions(out string, ownCaffeinatePID int) []lease.StatusExternal {
	res := []lease.StatusExternal{}
	for _, line := range strings.Split(out, "\n") {
		m := holderLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid <= 0 {
			continue
		}
		age, ok := parseAssertionAge(m[3])
		if !ok {
			continue
		}
		if ownCaffeinatePID > 0 && pid == ownCaffeinatePID {
			continue
		}
		res = append(res, lease.StatusExternal{PID: pid, Process: m[2], Type: m[4], AgeS: age})
	}
	return res
}

// parseAssertionAge converts "HH:MM:SS" or "D+HH:MM:SS" to seconds.
func parseAssertionAge(s string) (float64, bool) {
	days := 0
	if d, rest, found := strings.Cut(s, "+"); found {
		n, err := strconv.Atoi(d)
		if err != nil || n < 0 {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, false
		}
		v[i] = n
	}
	return float64(days*86400 + v[0]*3600 + v[1]*60 + v[2]), true
}
