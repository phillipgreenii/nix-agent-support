// harness_test.go: the in-process double for pg-connector and pg-desk. It is an
// apply.Runner (Env.Runner), so no test spawns a child process: under -race
// every child costs a flat second at exit, which once pushed this package past
// the test runner's cap. Each call is recorded (argv, and the
// PG_CONNECTOR_ISSUE_BEADS_DIR value when the call carried one), then answered
// from the first rule whose match text occurs in "<binary> <args...>". A call
// no rule matches gets the stock answer for its verb. The real exec path is
// covered by runexec_test.go.
package apply

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/config"
)

type record struct {
	Name     string
	Args     []string
	BeadsDir *string
}

func (r record) line() string { return r.Name + " " + strings.Join(r.Args, " ") }

type respRule struct {
	Match string
	// Nth, when non-zero, restricts the rule to the Nth call (counting this
	// one) whose "<binary> <args...>" text contains Match, so a bead can read
	// one way before a write and another way after it.
	Nth    int
	Exit   int
	Stdout string // "{seq}" is replaced by the call's 1-based sequence number
	Stderr string
}

type double struct {
	t     *testing.T
	rules []respRule

	mu   sync.Mutex // apply is sequential today; this keeps the double safe if it is not
	recs []record
}

// newDouble returns a double answering with rules (first match wins).
func newDouble(t *testing.T, rules ...respRule) *double {
	t.Helper()
	return &double{t: t, rules: rules}
}

// runner is the apply.Runner the double plugs into Env.Runner.
func (d *double) runner() Runner {
	return func(_ context.Context, c Call) CallResult {
		rec := record{Name: c.Name, Args: append([]string(nil), c.Args...)}
		for _, kv := range c.Env {
			if v, ok := strings.CutPrefix(kv, beadsDirEnv+"="); ok {
				rec.BeadsDir = &v
			}
		}
		line := rec.line()

		d.mu.Lock()
		defer d.mu.Unlock()
		seq := len(d.recs) + 1
		matchedBefore := func(match string) int {
			n := 0
			for _, pr := range d.recs[:seq-1] { // earlier calls only: this one is already recorded
				if strings.Contains(pr.line(), match) {
					n++
				}
			}
			return n
		}
		d.recs = append(d.recs, rec)

		for _, r := range d.rules {
			if !strings.Contains(line, r.Match) {
				continue
			}
			if r.Nth > 0 && matchedBefore(r.Match)+1 != r.Nth {
				continue
			}
			return CallResult{
				Stdout: []byte(strings.ReplaceAll(r.Stdout, "{seq}", strconv.Itoa(seq))),
				Stderr: []byte(r.Stderr),
				Code:   r.Exit,
			}
		}
		switch {
		case strings.HasPrefix(line, "pg-connector issue create"):
			return CallResult{Stdout: []byte(`{"result":{"id":"wb-` + strconv.Itoa(seq) + `"}}`)}
		case strings.HasPrefix(line, "pg-connector issue list"):
			return CallResult{Stdout: []byte(`{"entities":[],"present_ids":[],"sources":[]}`)}
		}
		return CallResult{Stdout: []byte(`{"result":{}}`)}
	}
}

func (d *double) calls() []record {
	d.t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]record(nil), d.recs...)
}

// lines is the joined "<binary> <args>" text of every exec, in order.
func (d *double) lines() []string {
	var out []string
	for _, r := range d.calls() {
		out = append(out, r.line())
	}
	return out
}

func testEnv(d *double, cfg *config.Config) Env {
	if cfg == nil {
		cfg = &config.Config{}
	}
	fixed := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	return Env{Runner: d.runner(), Config: cfg, Clock: func() time.Time { return fixed }}
}
