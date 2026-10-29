package backfill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recLine builds one run-record line (the composed command's own shape) for a PR.
type recLine struct {
	at       time.Time
	pr, mode string
	exit     int
	class    string
	actions  []Planned
	applied  map[string]int // rule -> applied count, apply mode
	adoption *AdoptionRecord
	residual *int
	planErr  string
	// eventID overrides the event id (default: the PR id, as the driver passes it);
	// a router run carries `<entity>@<12 hex>`.
	eventID string
}

func (r recLine) event() string {
	if r.eventID != "" {
		return r.eventID
	}
	return r.pr
}

func (r recLine) json(t *testing.T) []byte {
	t.Helper()
	m := map[string]any{
		"contract": RunRecordContract, "run_id": "r-" + r.pr, "at": r.at.UTC().Format(time.RFC3339),
		"finished_at": r.at.UTC().Format(time.RFC3339), "entity": r.pr, "event_id": r.event(), "mode": r.mode,
		"hydrate": map[string]any{"result": "ok"}, "exit_code": r.exit,
	}
	if r.class != "" {
		m["error_class"] = r.class
	}
	if r.exit == 0 {
		var rules []map[string]any
		seen := map[string]bool{}
		by := map[string]int{}
		for _, a := range r.actions {
			by[a.Kind]++
			if seen[a.Rule] {
				continue
			}
			seen[a.Rule] = true
			ro := map[string]any{"rule": a.Rule, "outcome": "planned"}
			if r.mode == ModeApply {
				n := r.applied[a.Rule]
				ro["counts"] = map[string]int{"planned": 1, "applied": n}
				if n > 0 {
					ro["outcome"] = "applied"
				}
			}
			rules = append(rules, ro)
		}
		d := map[string]any{"exit_code": 0, "rules": rules, "actions": r.actions, "by_kind": by}
		if d["actions"] == nil || len(r.actions) == 0 {
			d["actions"] = []Planned{}
		}
		if r.planErr != "" {
			d["plan_error"] = r.planErr
		}
		m["decide"] = d
		if r.adoption != nil {
			m["adoption"] = r.adoption
		}
		if r.residual != nil {
			m["residual"] = *r.residual
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeLog(t *testing.T, path string, lines ...[]byte) {
	t.Helper()
	var b []byte
	for _, l := range lines {
		b = append(append(b, l...), '\n')
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func intp(n int) *int { return &n }

func testSet(prs ...string) *Set {
	s := &Set{Contract: SetContract}
	for _, p := range prs {
		s.Entries = append(s.Entries, Entry{PR: p, State: StateOpen, From: []string{FromQuery}})
	}
	return s
}

// fakeRate is a RateSource double that answers from a list, then repeats the last.
type fakeRate struct {
	mu   sync.Mutex
	vals []int
	err  error
	n    int
}

func (f *fakeRate) Newest(context.Context) (Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Reading{}, f.err
	}
	i := f.n
	if i >= len(f.vals) {
		i = len(f.vals) - 1
	}
	f.n++
	return Reading{Remaining: f.vals[i], Source: "fake"}, nil
}

// fakeChange is a double of the composed command: it appends a run record for each
// call, from a script of outcomes per PR, and counts the calls.
type fakeChange struct {
	t      *testing.T
	path   string
	now    func() time.Time
	script map[string]func(mode string) recLine // pr -> record to write
	calls  []string
	exit   map[string]int // pr -> exit code (default 0)
	stderr string
}

func (f *fakeChange) run(_ context.Context, c Call) Result {
	f.t.Helper()
	mode, pr := c.Args[len(c.Args)-2], c.Args[len(c.Args)-1]
	f.calls = append(f.calls, mode+" "+pr)
	code := f.exit[pr]
	rl := recLine{at: f.now(), pr: pr, mode: mode, exit: code}
	if code != 0 {
		rl.class = ExitClasses[code].Class
	}
	if s, ok := f.script[pr]; ok && code == 0 {
		rl = s(mode)
		rl.at, rl.pr, rl.mode = f.now(), pr, mode
	}
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatal(err)
	}
	_, _ = fh.Write(append(rl.json(f.t), '\n'))
	_ = fh.Close()
	res := Result{Code: code}
	if code != 0 {
		res.Stderr = []byte("pg-pr-change: failed (class=" + rl.class + ")\n")
	}
	return res
}

func (f *fakeChange) countOf(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

var baseTime = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

// clock returns a clock that advances one second per call.
func clock() func() time.Time {
	t := baseTime
	return func() time.Time { t = t.Add(time.Second); return t }
}

func tmpRecord(t *testing.T) string { return filepath.Join(t.TempDir(), "runs.jsonl") }

func newRun(t *testing.T, mode string, set *Set, rec string, f *fakeChange, rate RateSource) RunOptions {
	t.Helper()
	return RunOptions{
		Mode: mode, Set: set, RunRecord: rec, ChangeCmd: "pg-pr-change", Rate: rate, Floor: 3000,
		Runner: f.run, Sleep: func(context.Context, time.Duration) {}, Now: clock(),
	}
}
