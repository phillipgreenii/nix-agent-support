package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/handlerstate"
	"github.com/phillipgreenii/pg-router-handlers/internal/rolefile"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
	"github.com/phillipgreenii/pg-router/conformance"
)

// INV-CCH-41: janitor tests. The scheduling (dedupe, single-flight, deadline,
// best effort, rotation) is exercised with injected passes and the REAL flock;
// the passes themselves have their own tests.

type janitorFixture struct {
	t        *testing.T
	stateDir string
	cfgPath  string
	opts     janitorOptions
}

// newJanitorFixture writes a launch config pointing the handler state at a temp
// dir and one role file per (name, poolDir) pair.
func newJanitorFixture(t *testing.T, roleSpecs ...[2]string) *janitorFixture {
	t.Helper()
	t.Setenv("CCPOOL_POOL", "")
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	cfg, _ := json.Marshal(map[string]any{"originProbe": map[string]any{"stateDir": stateDir}})
	cfgPath := filepath.Join(dir, "cfg.json")
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	f := &janitorFixture{t: t, stateDir: stateDir, cfgPath: cfgPath, opts: janitorOptions{cfgPath: cfgPath, deadline: time.Minute}}
	for _, spec := range roleSpecs {
		_, ccfg := workerRole("", "")
		if spec[1] != "" {
			ccfg["ccpool"].(map[string]any)["poolDir"] = spec[1]
		}
		raw, _ := json.Marshal(map[string]any{"role": map[string]any{"name": spec[0], "config": ccfg}})
		p := filepath.Join(dir, spec[0]+".role.json")
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		f.opts.roleFiles = append(f.opts.roleFiles, p)
	}
	return f
}

func (f *janitorFixture) mkDeps(config.Config, roles.Role) executor.Deps {
	return executor.Deps{LockDir: sessionlock.Dir(f.stateDir)}
}

func (f *janitorFixture) run(passes janitorPasses) (int, string) {
	var out bytes.Buffer
	code := serveJanitor(context.Background(), &out, f.opts, f.mkDeps, passes)
	return code, out.String()
}

// events returns the janitor_run rows of events.jsonl.
func (f *janitorFixture) events() []map[string]any {
	b, err := os.ReadFile(filepath.Join(f.stateDir, handlerstate.EventLogName))
	if err != nil {
		return nil
	}
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["kind"] == janitorRunEvent {
			rows = append(rows, m)
		}
	}
	return rows
}

// recorder is a set of passes that record their calls.
type recorder struct {
	mu          sync.Mutex
	closedBead  []string // role the pool-level pass ran under
	defaultPool int
	orphan      []string
	worktree    []string
	lockGC      int
}

func (r *recorder) passes() janitorPasses {
	return janitorPasses{
		closedBead: func(_ context.Context, role roles.Role, _ config.Config, _ executor.Deps) int {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.closedBead = append(r.closedBead, role.Name)
			return 1
		},
		defaultPool: func(context.Context, config.Config) int {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.defaultPool++
			return 0
		},
		orphan: func(_ context.Context, role roles.Role, _ config.Config, _ executor.Deps) int {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.orphan = append(r.orphan, role.Name)
			return 0
		},
		worktree: func(_ context.Context, role roles.Role, _ config.Config, _ executor.Deps) int {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.worktree = append(r.worktree, role.Name)
			return 0
		},
		lockGC: func(executor.Deps) int {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.lockGC++
			return 0
		},
	}
}

func TestJanitor_flagParsing(t *testing.T) {
	f := newJanitorFixture(t)
	cases := map[string][]string{
		"unknown flag":      {"--bogus"},
		"stray argument":    {"--config", f.cfgPath, "stray"},
		"no config":         {},
		"zero deadline":     {"--config", f.cfgPath, "--deadline", "0s"},
		"malformed":         {"--config", f.cfgPath, "--deadline", "soon"},
		"missing role file": {"--config", f.cfgPath, "--role-file", filepath.Join(t.TempDir(), "absent.json")},
	}
	t.Setenv(rolefile.EnvConfig, "")
	for name, args := range cases {
		if got := runJanitor(args); got != conformance.ExitUsage {
			t.Errorf("%s: exit %d, want %d", name, got, conformance.ExitUsage)
		}
	}
	if got := runJanitor([]string{"-h"}); got != conformance.ExitOK {
		t.Errorf("-h: exit %d, want 0", got)
	}
}

func TestJanitor_poolLevelPassesRunOncePerDistinctPool(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", "/pools/one"}, [2]string{"b", "/pools/one"}, [2]string{"c", "/pools/two"}, [2]string{"d", ""}, [2]string{"e", ""})
	rec := &recorder{}
	if code, _ := f.run(rec.passes()); code != conformance.ExitOK {
		t.Fatalf("exit %d", code)
	}
	// pools: one, two, and the unset pool (d and e): 3 closed-bead runs, not 5.
	if len(rec.closedBead) != 3 {
		t.Errorf("closed-bead passes ran %d times (%v), want 3 (one per distinct pool)", len(rec.closedBead), rec.closedBead)
	}
	// Role-scoped passes run for every role.
	if len(rec.orphan) != 5 || len(rec.worktree) != 5 {
		t.Errorf("orphan/worktree ran %d/%d times, want 5/5", len(rec.orphan), len(rec.worktree))
	}
	// The default-pool pass keys on a role pool dir (sweeps.go): once per run.
	if rec.defaultPool != 1 {
		t.Errorf("default-pool pass ran %d times, want 1", rec.defaultPool)
	}
	if rec.lockGC != 1 {
		t.Errorf("lock GC ran %d times, want 1", rec.lockGC)
	}
}

func TestJanitor_defaultPoolPassSkippedWhenNoRoleHasAPool(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""}, [2]string{"b", ""})
	rec := &recorder{}
	f.run(rec.passes())
	if rec.defaultPool != 0 {
		t.Errorf("default-pool pass ran %d times with no role pool and no CCPOOL_POOL, want 0", rec.defaultPool)
	}
	if len(rec.closedBead) != 1 || len(rec.orphan) != 2 {
		t.Errorf("roles on the default pool still get swept: closed-bead %v, orphan %v", rec.closedBead, rec.orphan)
	}
}

func TestJanitor_inheritedCCPoolPoolEnablesDefaultPoolPass(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""})
	t.Setenv("CCPOOL_POOL", "/handler/pool")
	rec := &recorder{}
	f.run(rec.passes())
	if rec.defaultPool != 1 {
		t.Errorf("default-pool pass ran %d times, want 1", rec.defaultPool)
	}
}

func TestJanitor_zeroRolesStillCompletes(t *testing.T) {
	f := newJanitorFixture(t)
	rec := &recorder{}
	code, out := f.run(rec.passes())
	if code != conformance.ExitOK || !strings.Contains(out, "outcome=completed") {
		t.Fatalf("exit %d, out %q", code, out)
	}
	if rec.lockGC != 1 {
		t.Errorf("lock GC ran %d times, want 1", rec.lockGC)
	}
	if ev := f.events(); len(ev) != 1 || ev[0]["outcome"] != janitorCompleted {
		t.Errorf("events = %v, want one completed row", ev)
	}
}

// Single-flight with a REAL flock: a second run while one holds the lock does
// nothing, says so, ends in success and records a skipped row.
func TestJanitor_singleFlightSecondRunSkips(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""})
	held, err := sessionlock.TryLock(sessionlock.Dir(f.stateDir), janitorLockKey)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	code, out := f.run(rec.passes())
	if code != conformance.ExitOK {
		t.Errorf("a skipped run exits %d, want 0", code)
	}
	if !strings.Contains(out, "already running") {
		t.Errorf("output %q lacks the already-running line", out)
	}
	if len(rec.orphan) != 0 || rec.lockGC != 0 {
		t.Errorf("a skipped run ran passes: %+v", rec)
	}
	if ev := f.events(); len(ev) != 1 || ev[0]["outcome"] != janitorSkipped {
		t.Errorf("events = %v, want one skipped row", ev)
	}
	// Once the holder is gone, the next run proceeds.
	held.Unlock()
	rec2 := &recorder{}
	if _, out := f.run(rec2.passes()); !strings.Contains(out, "outcome=completed") || len(rec2.orphan) != 1 {
		t.Errorf("run after release: out %q, orphan %v", out, rec2.orphan)
	}
}

// A run that reaches its deadline returns what it did, ends in success, and its
// row says deadline. The blocked role is bounded by its own share, so the next
// role still runs.
func TestJanitor_deadlineBoundsASlowRoleAndRecordsIt(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""}, [2]string{"b", ""})
	f.opts.deadline = 400 * time.Millisecond
	rec := &recorder{}
	p := rec.passes()
	inner := p.orphan
	p.orphan = func(ctx context.Context, role roles.Role, cfg config.Config, deps executor.Deps) int {
		inner(ctx, role, cfg, deps)
		if role.Name == "a" {
			<-ctx.Done() // a slow first role
		}
		return 0
	}
	start := time.Now()
	code, out := f.run(p)
	if code != conformance.ExitOK {
		t.Fatalf("exit %d", code)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("run took %v, deadline was 400ms", took)
	}
	if !strings.Contains(out, "outcome=deadline") {
		t.Errorf("output %q lacks outcome=deadline", out)
	}
	if len(rec.orphan) != 2 {
		t.Errorf("orphan ran for %v, want both roles (the slow one must not starve the next)", rec.orphan)
	}
	if ev := f.events(); len(ev) != 1 || ev[0]["outcome"] != janitorDeadline {
		t.Errorf("events = %v, want one deadline row", ev)
	}
}

// Each pass is best effort: one that panics does not stop the others.
func TestJanitor_aPassFailingDoesNotStopTheRest(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""})
	rec := &recorder{}
	p := rec.passes()
	p.orphan = func(context.Context, roles.Role, config.Config, executor.Deps) int { panic("boom") }
	code, out := f.run(p)
	if code != conformance.ExitOK || !strings.Contains(out, "outcome=completed") {
		t.Fatalf("exit %d out %q", code, out)
	}
	if len(rec.worktree) != 1 || rec.lockGC != 1 || len(rec.closedBead) != 1 {
		t.Errorf("later passes did not run: %+v", rec)
	}
	ev := f.events()
	if len(ev) != 1 {
		t.Fatalf("events = %v", ev)
	}
	passes := ev[0]["passes"].(map[string]any)
	if got := passes[passOrphan].(map[string]any)["errors"]; got != float64(1) {
		t.Errorf("orphan errors = %v, want 1", got)
	}
}

// The role a run starts on rotates between runs, so a slow first role cannot
// starve the rest every run.
func TestJanitor_startRoleRotatesBetweenRuns(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", ""}, [2]string{"b", ""}, [2]string{"c", ""})
	var firsts []string
	for i := 0; i < 4; i++ {
		rec := &recorder{}
		f.run(rec.passes())
		firsts = append(firsts, rec.orphan[0])
		if len(rec.orphan) != 3 {
			t.Fatalf("run %d covered %v, want all three roles", i, rec.orphan)
		}
	}
	if got := strings.Join(firsts, ""); got != "abca" {
		t.Errorf("first roles = %q, want abca", got)
	}
}

func TestJanitor_runRecordCarriesPassCounts(t *testing.T) {
	f := newJanitorFixture(t, [2]string{"a", "/p"})
	rec := &recorder{}
	f.run(rec.passes())
	ev := f.events()
	if len(ev) != 1 {
		t.Fatalf("events = %v", ev)
	}
	st := ev[0]["passes"].(map[string]any)[passClosedBead].(map[string]any)
	if st["runs"] != float64(1) || st["reclaimed"] != float64(1) {
		t.Errorf("closed-bead stat = %v, want runs 1 reclaimed 1", st)
	}
	if ev[0]["reclaimed"] != float64(1) {
		t.Errorf("total reclaimed = %v, want 1", ev[0]["reclaimed"])
	}
}

func TestJanitor_dryRunChangesNothing(t *testing.T) {
	f := newJanitorFixture(t)
	f.opts.dryRun = true
	rec := &recorder{}
	code, out := f.run(rec.passes())
	if code != conformance.ExitOK || !strings.Contains(out, "dry run") {
		t.Fatalf("exit %d out %q", code, out)
	}
	if rec.lockGC != 0 || len(f.events()) != 0 {
		t.Errorf("dry run ran passes or wrote events: %+v %v", rec, f.events())
	}
	if _, err := os.Stat(filepath.Join(sessionlock.Dir(f.stateDir), janitorLockKey+".lock")); err == nil {
		t.Error("dry run took the single-flight lock")
	}
}

func TestJanitor_invalidRoleFileIsAUsageError(t *testing.T) {
	f := newJanitorFixture(t)
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"role":{"name":"x","config":{"nope":1}}}`), 0o644)
	f.opts.roleFiles = []string{bad}
	if code, _ := f.run((&recorder{}).passes()); code != conformance.ExitUsage {
		t.Errorf("exit %d, want %d", code, conformance.ExitUsage)
	}
}

// The transitional switch (INV-CCH-41): default on, a launch config can turn it off.
func TestConfig_inlineSweepsDefaultsOnAndLoads(t *testing.T) {
	if !config.Default().InlineSweeps {
		t.Error("InlineSweeps default is false, want true (today's behavior)")
	}
	p := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(p, []byte(`{"inlineSweeps":false}`), 0o644)
	c, err := rolefile.LoadConfig(p)
	if err != nil || c.InlineSweeps {
		t.Errorf("LoadConfig inlineSweeps=false: %v, InlineSweeps=%v", err, c.InlineSweeps)
	}
	os.WriteFile(p, []byte(`{}`), 0o644)
	if c, err := rolefile.LoadConfig(p); err != nil || !c.InlineSweeps {
		t.Errorf("LoadConfig {}: %v, InlineSweeps=%v, want true", err, c.InlineSweeps)
	}
}
