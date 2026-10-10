package apply

// The opt-in real-binary contract test of the focus hold and release
// (daily-focus design section 10.1, decision D-F19). The recorded-argv tests
// (focus_argv_test.go) prove the SHAPE of the update; this one proves what a
// real `bd` does with it, because the hold and release were spiked by hand
// against bd 1.2.2 only. It drives apply.Run end to end against a real
// pg-connector and a real bd in a disposable workspace.
//
// Gating, in the pattern of internal/parity/realbin_test.go: the test SKIPS
// with an explicit message when the binaries are not named, and FAILS when
// PG_DECIDER_FOCUS_REQUIRE_BINARIES is set and one is missing. It is NOT wired
// into the default nix check. Hermeticity: bd init runs in a fresh temp dir
// with a time-based prefix under a cleaned environment, so it cannot bind to
// the operator's real tracker; bd 1.3.1 initializes an EMBEDDED Dolt engine
// there, so no dolt server is started or contacted. Every helper is prefixed
// "focusReal" because sibling packets add test files to this package.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-decider/internal/action"
	"github.com/phillipgreenii/pg-decider/internal/config"
	"github.com/phillipgreenii/pg-decider/internal/view"
)

// The environment variables of the opt-in test. Each of the three binary
// variables is the path of an executable file.
const (
	focusRealEnvConnector = "PG_DECIDER_FOCUS_PG_CONNECTOR_BIN"
	focusRealEnvBD        = "PG_DECIDER_FOCUS_BD_BIN"
	focusRealEnvDesk      = "PG_DECIDER_FOCUS_PG_DESK_BIN" // a real pg-desk, or a stub that accepts `issue refresh` and exits 0

	// focusRealEnvRequire, set to anything but "" or "0", turns a missing
	// binary from a skip into a failure.
	focusRealEnvRequire = "PG_DECIDER_FOCUS_REQUIRE_BINARIES"

	// focusRealQuery is the named query the dedup lookup reads; the disposable
	// pg-connector config defines it as every focus-item bead in every status.
	focusRealQuery = "focus_beads_query"

	// focusRealDeadline is a hang guard for real runs, not a performance
	// assertion: bd is far slower under host load.
	focusRealDeadline = 2 * time.Minute
)

type focusRealBins struct{ connector, bd, desk string }

// focusRealBinaries resolves the three binaries from getenv. missing names the
// variables that are unset or empty; a variable that is set but does not name
// an executable file is reported in bad. Pure, so the skip-versus-fail rule is
// itself unit-tested below.
func focusRealBinaries(getenv func(string) string) (b focusRealBins, missing, bad []string) {
	for _, v := range []struct {
		env string
		dst *string
	}{{focusRealEnvConnector, &b.connector}, {focusRealEnvBD, &b.bd}, {focusRealEnvDesk, &b.desk}} {
		p := getenv(v.env)
		if p == "" {
			missing = append(missing, v.env)
			continue
		}
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 || !filepath.IsAbs(p) {
			bad = append(bad, v.env+"="+p)
			continue
		}
		*v.dst = p
	}
	return b, missing, bad
}

func focusRealRequired(getenv func(string) string) bool {
	v := getenv(focusRealEnvRequire)
	return v != "" && v != "0"
}

// focusRealGate is the skip-versus-fail rule: a nil error means run the test.
// Unset binaries skip, unless required, which makes them an error; a named
// binary that is not an executable file is always an error.
func focusRealGate(getenv func(string) string) (focusRealBins, string, error) {
	b, missing, bad := focusRealBinaries(getenv)
	if len(bad) > 0 {
		return b, "", fmt.Errorf("not an absolute path of an executable file: %s", strings.Join(bad, ", "))
	}
	if len(missing) > 0 {
		msg := strings.Join(missing, ", ") + " not set"
		if focusRealRequired(getenv) {
			return b, "", fmt.Errorf("%s, and %s is set: the test that runs the real binaries cannot be skipped", msg, focusRealEnvRequire)
		}
		return b, msg + ": skipping the opt-in test that runs the real pg-connector and bd (see packages/pg-decider/README.md, Running the focus contract test)", nil
	}
	return b, "", nil
}

func TestFocusRealGateSkipsWhenUnsetAndFailsWhenRequired(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	all := map[string]string{focusRealEnvConnector: exe, focusRealEnvBD: exe, focusRealEnvDesk: exe}
	with := func(extra map[string]string, drop ...string) map[string]string {
		m := map[string]string{}
		for k, v := range all {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		for _, k := range drop {
			delete(m, k)
		}
		return m
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		wantSkip string // non-empty: must skip with a message containing this
		wantErr  string // non-empty: must fail with an error containing this
	}{
		{name: "nothing named skips", env: map[string]string{}, wantSkip: focusRealEnvConnector},
		{name: "one missing skips and names it", env: with(nil, focusRealEnvBD), wantSkip: focusRealEnvBD},
		{name: "nothing named but required fails", env: map[string]string{focusRealEnvRequire: "1"}, wantErr: focusRealEnvRequire},
		{name: "one missing but required fails", env: with(map[string]string{focusRealEnvRequire: "1"}, focusRealEnvDesk), wantErr: focusRealEnvDesk},
		{name: "required zero is not required", env: with(map[string]string{focusRealEnvRequire: "0"}, focusRealEnvDesk), wantSkip: focusRealEnvDesk},
		{name: "a named non-executable fails even when not required", env: with(map[string]string{focusRealEnvBD: plain}), wantErr: focusRealEnvBD},
		{name: "a named missing file fails", env: with(map[string]string{focusRealEnvBD: "/nonexistent/focus-bd"}), wantErr: focusRealEnvBD},
		{name: "all named runs", env: with(map[string]string{focusRealEnvRequire: "1"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, skip, err := focusRealGate(env(tc.env))
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want an error naming %q, got skip %q err %v", tc.wantErr, skip, err)
				}
			case tc.wantSkip != "":
				if err != nil || !strings.Contains(skip, tc.wantSkip) {
					t.Fatalf("want a skip naming %q, got skip %q err %v", tc.wantSkip, skip, err)
				}
			default:
				if err != nil || skip != "" || b.connector != exe || b.bd != exe || b.desk != exe {
					t.Fatalf("want to run, got %+v skip %q err %v", b, skip, err)
				}
			}
		})
	}
}

// focusRealEnviron is the child environment: the process environment minus the
// variables that bind bd to a workspace or tracker (a stray one would bypass
// the disposable workspace), with the JSON envelope pinned and every beads
// write attributed to the test.
func focusRealEnviron(extra ...string) []string {
	out := make([]string, 0, len(os.Environ())+len(extra)+3)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "BEADS_DIR", "BEADS_ACTOR", "WORKSPACE_ROOT", "ZR_MACHINE_SUPPORT_WORKSPACE_ROOT",
			"PG_CONNECTOR_ISSUE_BEADS_DIR", "PG_CONNECTOR_ISSUE_BEADS_ACTOR", "PG_PR_CONFIG", "BD_JSON_ENVELOPE":
			continue
		}
		out = append(out, kv)
	}
	return append(out, append([]string{
		"BD_JSON_ENVELOPE=1", "BEADS_ACTOR=focus-contract-test", "PG_CONNECTOR_ISSUE_BEADS_ACTOR=focus-contract-test",
	}, extra...)...)
}

// focusRealWorld is the disposable workspace and the exec factory over the
// real binaries.
type focusRealWorld struct {
	t      *testing.T
	bins   focusRealBins
	dir    string // the bd workspace: holds .beads
	config string // the pg-connector registry
	env    []string
	calls  []string // every "<binary> <args>" the factory exec'd, in order
}

func focusRealSetup(t *testing.T, bins focusRealBins) *focusRealWorld {
	t.Helper()
	root := t.TempDir()
	w := &focusRealWorld{t: t, bins: bins, dir: filepath.Join(root, "ws"), config: filepath.Join(root, "pg-pr.yaml")}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pg-connector registry with one issue backend and the named query the
	// focus dedup lookup reads: every focus-item bead in every status.
	cfg := "connector:\n  issue:\n    - pg-connector-issue-beads\nbackends:\n  pg-connector-issue-beads:\n    queries:\n      " +
		focusRealQuery + ": \"list --label focus-item --all\"\n"
	if err := os.WriteFile(w.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// PATH: the real pg-connector's and bd's directories, then the ambient PATH
	// (which supplies pg-connector-issue-beads, git and the rest).
	path := strings.Join([]string{filepath.Dir(bins.connector), filepath.Dir(bins.bd), os.Getenv("PATH")}, string(os.PathListSeparator))
	w.env = focusRealEnviron("PATH="+path, "PG_PR_CONFIG="+w.config)

	prefix := "tp" + fmt.Sprintf("%x", time.Now().UnixNano())[:10]
	w.bd("init", "--prefix", prefix, "--non-interactive", "-q", "--skip-agents", "--skip-hooks")
	return w
}

func (w *focusRealWorld) bd(args ...string) string {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), focusRealDeadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.bins.bd, args...)
	cmd.Dir, cmd.Env = w.dir, w.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		w.t.Fatalf("bd %s: %v\n%s%s", strings.Join(args, " "), err, out.String(), errb.String())
	}
	return out.String()
}

// factory execs the real pg-connector for "pg-connector" and the named pg-desk
// for "pg-desk", under the disposable environment, and records every call.
func (w *focusRealWorld) factory() CmdFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		bin := map[string]string{"pg-connector": w.bins.connector, "pg-desk": w.bins.desk}[name]
		if bin == "" {
			w.t.Fatalf("the apply layer exec'd %q, which this test does not provide", name)
		}
		w.calls = append(w.calls, name+" "+strings.Join(args, " "))
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env = w.dir, w.env
		return cmd
	}
}

func (w *focusRealWorld) cfg() *config.Config {
	return &config.Config{BeadsDir: w.dir, FocusBeadsQuery: focusRealQuery}
}

// updates are the `pg-connector issue update` calls recorded since mark.
func (w *focusRealWorld) updates(mark int) []string {
	var out []string
	for _, c := range w.calls[mark:] {
		if strings.HasPrefix(c, "pg-connector issue update") {
			out = append(out, c)
		}
	}
	return out
}

// focusRealIssue is the members of a bd issue record the test reads.
type focusRealIssue struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	Assignee string         `json:"assignee"`
	Defer    string         `json:"defer_until"`
	Labels   []string       `json:"labels"`
	Metadata map[string]any `json:"metadata"`
}

// focusRealDecode decodes a bd --json answer, enveloped ({"data": ...}) or
// bare, holding one record or an array of them.
func focusRealDecode(t *testing.T, out string) []focusRealIssue {
	t.Helper()
	raw := json.RawMessage(out)
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Data) > 0 {
		raw = env.Data
	}
	var many []focusRealIssue
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	var one focusRealIssue
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatalf("decode bd output: %v\n%s", err, out)
	}
	return []focusRealIssue{one}
}

func (w *focusRealWorld) show(id string) focusRealIssue {
	w.t.Helper()
	recs := focusRealDecode(w.t, w.bd("show", id, "--json"))
	if len(recs) != 1 || recs[0].ID != id {
		w.t.Fatalf("bd show %s answered %+v", id, recs)
	}
	return recs[0]
}

func (w *focusRealWorld) ids(args ...string) map[string]bool {
	w.t.Helper()
	out := map[string]bool{}
	for _, r := range focusRealDecode(w.t, w.bd(args...)) {
		out[r.ID] = true
	}
	return out
}

func (w *focusRealWorld) ready() map[string]bool { return w.ids("ready", "--json", "--limit", "0") }
func (w *focusRealWorld) list() map[string]bool {
	return w.ids("list", "--all", "--json", "--limit", "0")
}

// TestFocusHoldReleaseCycleRealBD runs the focus bead through its life against
// a real pg-connector and a real bd: mint through the decider's own action,
// hold, release, a metadata merge, and a claimed bead. Opt-in; see the file
// comment and packages/pg-decider/README.md.
func TestFocusHoldReleaseCycleRealBD(t *testing.T) {
	bins, skip, err := focusRealGate(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		t.Skip(skip)
	}
	w := focusRealSetup(t, bins)
	ctx, cancel := context.WithTimeout(context.Background(), 4*focusRealDeadline)
	defer cancel()

	var stderr bytes.Buffer
	env := Env{
		Command: w.factory(), Config: w.cfg(), Stderr: &stderr,
		Clock: func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) },
	}
	// applyOne plans the single focus.item action of v and applies it through
	// apply.Run, returning the plan and its one event.
	applyOne := func(v *view.View) (action.Action, Event) {
		t.Helper()
		a := focusArgvPlan(t, v, w.cfg())
		res := Run(ctx, Input{Type: v.Type, ID: v.ID, View: v, Actions: []action.Action{a}, Env: env})
		if len(res.Events) != 1 {
			t.Fatalf("want one event, got %+v", res.Events)
		}
		return a, res.Events[0]
	}
	transition := func(a action.Action) string { s, _ := a.Facts["transition"].(string); return s }
	wantApplied := func(ev Event) {
		t.Helper()
		if ev.Outcome != OutcomeApplied || ev.Err != nil {
			t.Fatalf("outcome %v, err %v, want applied\nstderr: %s\ncalls: %q", ev.Outcome, ev.Err, stderr.String(), w.calls)
		}
	}
	wantMarker := func(id, want string) {
		t.Helper()
		if got := w.show(id).Metadata["focus_hold"]; got != want {
			t.Fatalf("%s metadata.focus_hold = %v, want %q", id, got, want)
		}
	}

	// 1. Mint: the decider's own create. A second mint of the same source is a
	// dedup HIT through focus_beads_query, which proves the named query
	// resolves against the real backend and lists the bead.
	mintView := focusArgvViewFor(t, "ACME-7", focusArgvPeriod, nil)
	a, ev := applyOne(mintView)
	if transition(a) != "mint" {
		t.Fatalf("planned %q, want mint", transition(a))
	}
	wantApplied(ev)
	id := ev.WorkItemID
	if id == "" {
		t.Fatal("the mint returned no bead id")
	}
	rec := w.show(id)
	if rec.Status != "open" || !containsString(rec.Labels, "focus-item") {
		t.Fatalf("minted bead = %+v, want status open with label focus-item", rec)
	}
	if got := rec.Metadata["dedup_key"]; got != "issue:ACME-7:focus-item" {
		t.Fatalf("minted bead dedup_key = %v", got)
	}
	if !w.ready()[id] || !w.list()[id] {
		t.Fatalf("a minted bead must be in bd ready and bd list")
	}
	if _, again := applyOne(mintView); again.Outcome != OutcomeDeduped || again.WorkItemID != id {
		t.Fatalf("a second mint = %v %q (err %v), want deduped onto %s", again.Outcome, again.WorkItemID, again.Err, id)
	}

	// 2. Hold: the item leaves the plan. The bead leaves bd ready, stays in bd
	// list, and is deferred with the marker, by ONE update.
	mark := len(w.calls)
	a, ev = applyOne(focusArgvViewFor(t, "ACME-7", nil, &focusArgvLink{id: id, state: "open"}))
	if transition(a) != "hold" {
		t.Fatalf("planned %q, want hold", transition(a))
	}
	wantApplied(ev)
	if got, want := w.updates(mark), []string{"pg-connector issue update " + id + " --status deferred --metadata focus_hold=struck"}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the hold sent %q, want exactly %q", got, want)
	}
	rec = w.show(id)
	if rec.Status != "deferred" || rec.Assignee != "" {
		t.Fatalf("held bead = %+v, want status deferred and no assignee", rec)
	}
	wantMarker(id, "struck")
	if w.ready()[id] {
		t.Fatal("a held bead must not be in bd ready")
	}
	if !w.list()[id] {
		t.Fatal("a held bead must stay in bd list")
	}

	// 3. Release: the item is reselected. The bead is back in bd ready, open,
	// with the deferral cleared and the marker released, by ONE update.
	mark = len(w.calls)
	a, ev = applyOne(focusArgvViewFor(t, "ACME-7", focusArgvPeriod, &focusArgvLink{id: id, state: "deferred", marker: "struck"}))
	if transition(a) != "release" {
		t.Fatalf("planned %q, want release", transition(a))
	}
	wantApplied(ev)
	if got, want := w.updates(mark), []string{"pg-connector issue update " + id + " --status open --clear-defer --metadata focus_hold=released"}; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the release sent %q, want exactly %q", got, want)
	}
	rec = w.show(id)
	if rec.Status != "open" || rec.Defer != "" {
		t.Fatalf("released bead = %+v, want status open and no deferral", rec)
	}
	wantMarker(id, "released")
	if !w.ready()[id] {
		t.Fatal("a released bead must be back in bd ready")
	}

	// 4. Metadata merges and has no unset: an update naming another key leaves
	// focus_hold in place.
	if _, err := call(ctx, env, []string{"issue", "update", id, "--metadata", "other_key=x"}); err != nil {
		t.Fatal(err)
	}
	wantMarker(id, "released")
	if got := w.show(id).Metadata["other_key"]; got != "x" {
		t.Fatalf("metadata.other_key = %v, want x", got)
	}

	// 5. A claimed bead. The decider's view can lag a claim, so the live read
	// must see it and abandon the hold; and a bead deferred while claimed (the
	// window a hold can lose a race into) keeps its assignee, which the
	// post-write compensation then restores to in_progress.
	_, ev = applyOne(focusArgvViewFor(t, "ACME-8", focusArgvPeriod, nil))
	wantApplied(ev)
	id2 := ev.WorkItemID
	w.bd("update", id2, "--claim")
	claimer := w.show(id2).Assignee
	if claimer == "" {
		t.Fatalf("bd update --claim left no assignee: %+v", w.show(id2))
	}
	mark = len(w.calls)
	_, ev = applyOne(focusArgvViewFor(t, "ACME-8", nil, &focusArgvLink{id: id2, state: "open"}))
	if ev.Outcome != OutcomeSkippedStale {
		t.Fatalf("a hold of a claimed bead = %v (err %v), want skipped-stale\nstderr: %s", ev.Outcome, ev.Err, stderr.String())
	}
	if ups := w.updates(mark); len(ups) != 0 {
		t.Fatalf("an abandoned hold wrote %q", ups)
	}
	if rec = w.show(id2); rec.Status != "in_progress" || rec.Assignee != claimer {
		t.Fatalf("claimed bead after an abandoned hold = %+v, want in_progress held by %s", rec, claimer)
	}

	if _, err := call(ctx, env, updateArgs(env, id2, action.Fields{Status: "deferred", Metadata: map[string]string{"focus_hold": "struck"}}, false)); err != nil {
		t.Fatal(err)
	}
	if rec = w.show(id2); rec.Status != "deferred" || rec.Assignee != claimer {
		t.Fatalf("a deferred claimed bead = %+v, want deferred and still assigned to %s", rec, claimer)
	}
	if w.ready()[id2] {
		t.Fatal("a deferred claimed bead must not be in bd ready")
	}
	r := &runner{in: Input{Env: env}}
	if err := r.restoreClaim(ctx, id2); err != nil {
		t.Fatalf("restoreClaim: %v\nstderr: %s", err, stderr.String())
	}
	if rec = w.show(id2); rec.Status != "in_progress" || rec.Assignee != claimer {
		t.Fatalf("after the compensation = %+v, want in_progress held by %s", rec, claimer)
	}
	wantMarker(id2, "struck") // the compensation leaves the marker alone
}

func containsString(in []string, s string) bool {
	for _, v := range in {
		if v == s {
			return true
		}
	}
	return false
}
