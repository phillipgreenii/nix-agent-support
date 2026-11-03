package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router-handlers/internal/config"
	"github.com/phillipgreenii/pg-router-handlers/internal/executor"
	"github.com/phillipgreenii/pg-router-handlers/internal/roles"
	"github.com/phillipgreenii/pg-router-handlers/internal/sessionlock"
)

// INV-CCH-41: the worktree census and the build_info gauges, against real temp
// trees and the real serveJanitor, with the census bounds and the clock injected.

// makeTree creates <root>/<name>/ for each name with files of the given sizes.
func makeTree(t *testing.T, root string, trees map[string][]int) {
	t.Helper()
	for name, sizes := range trees {
		d := filepath.Join(root, name, "sub")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for i, sz := range sizes {
			if err := os.WriteFile(filepath.Join(d, "f"+string(rune('a'+i))), make([]byte, sz), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A plain file beside the worktrees is not a worktree.
	if err := os.WriteFile(filepath.Join(root, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanWorktreeDirs_countAndBytes(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, map[string][]int{"w1": {10, 20}, "w2": {5}})
	st := scanWorktreeDirs(context.Background(), []string{root}, censusMaxEntries)
	if st.count != 2 || st.bytes != 35 || st.truncated {
		t.Errorf("got %+v, want count 2 bytes 35 not truncated", st)
	}
	if st := scanWorktreeDirs(context.Background(), []string{filepath.Join(root, "missing")}, 100); st != (worktreeCensus{}) {
		t.Errorf("missing dir = %+v, want zero", st)
	}
}

func TestScanWorktreeDirs_entryCapTruncates(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, map[string][]int{"w1": {10, 20}, "w2": {5, 5}})
	// Each worktree has 4 entries (itself, sub, two files); a cap of 5 stops inside w2.
	st := scanWorktreeDirs(context.Background(), []string{root}, 5)
	if !st.truncated || st.count != 2 {
		t.Errorf("got %+v, want truncated with both worktrees counted", st)
	}
	if st.bytes >= 40 {
		t.Errorf("bytes = %d, want a lower bound under 40", st.bytes)
	}
}

func TestScanWorktreeDirs_expiredContextTruncates(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, map[string][]int{"w1": {10}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := scanWorktreeDirs(ctx, []string{root}, censusMaxEntries)
	if !st.truncated || st.count != 1 || st.bytes != 0 {
		t.Errorf("got %+v, want truncated, count 1, bytes 0", st)
	}
}

func TestCensusBudget(t *testing.T) {
	for _, c := range []struct{ since, want time.Duration }{
		{0, 180 * time.Second},
		{360 * time.Second, 180 * time.Second},
		{400 * time.Second, 140 * time.Second},
		{540 * time.Second, 0},
		{600 * time.Second, -60 * time.Second},
	} {
		if got := censusBudget(censusMaxBudget, c.since); got != c.want {
			t.Errorf("censusBudget(since=%v) = %v, want %v", c.since, got, c.want)
		}
	}
}

// censusFixture is a janitor fixture whose launch config names worktreeDir, with
// every gauge captured.
func censusFixture(t *testing.T, wtDir string, roleNames ...string) (*janitorFixture, *[]gaugePoint) {
	t.Helper()
	var specs [][2]string
	for _, n := range roleNames {
		specs = append(specs, [2]string{n, ""})
	}
	f := newJanitorFixture(t, specs...)
	cfg, _ := json.Marshal(map[string]any{"originProbe": map[string]any{"stateDir": f.stateDir}, "worktreeDir": wtDir})
	if err := os.WriteFile(f.cfgPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	return f, new([]gaugePoint)
}

func (f *janitorFixture) censusPasses(pts *[]gaugePoint) janitorPasses {
	p := (&recorder{}).passes()
	p.emit = spyEmit(pts)
	p.ccpoolVersion = func(context.Context) (string, error) { return "0.0.0-abcd1234\n", nil }
	return p
}

func TestJanitor_censusEmitsGaugesOncePerDistinctDir(t *testing.T) {
	wt := t.TempDir()
	makeTree(t, wt, map[string][]int{"w1": {10, 20}, "w2": {5}})
	f, pts := censusFixture(t, wt, "a", "b", "c") // three roles share one dir
	if code, out := f.run(f.censusPasses(pts)); code != 0 || !strings.Contains(out, "outcome=completed") {
		t.Fatalf("exit %d out %q", code, out)
	}
	for name, want := range map[string]float64{gaugeWorktreeCount: 2, gaugeWorktreeBytes: 35, gaugeWorktreeTruncated: 0} {
		var n int
		for _, p := range *pts {
			if p.name == name {
				n++
				if p.value != want || len(p.labels) != 0 {
					t.Errorf("%s = %v labels %v, want %v with no labels", name, p.value, p.labels, want)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s emitted %d times, want once", name, n)
		}
	}
	if p, ok := find(*pts, gaugeWorktreeCensusSec, ""); !ok || len(p.labels) != 0 || p.value < 0 {
		t.Errorf("census seconds = %+v ok=%v", p, ok)
	}
}

func TestJanitor_censusSkippedUnderDryRun(t *testing.T) {
	wt := t.TempDir()
	makeTree(t, wt, map[string][]int{"w1": {10}})
	f, pts := censusFixture(t, wt, "a")
	f.opts.dryRun = true
	mk := func(config.Config, roles.Role) executor.Deps {
		return executor.Deps{CC: &fakeCC{}, BD: fakeBR{}, LockDir: sessionlock.Dir(f.stateDir)}
	}
	var out bytes.Buffer
	if code := serveJanitor(context.Background(), &out, f.opts, mk, f.censusPasses(pts)); code != 0 || !strings.Contains(out.String(), "dry run") {
		t.Fatalf("exit %d out %q", code, out.String())
	}
	if len(*pts) != 0 {
		t.Errorf("dry run emitted %v, want nothing", *pts)
	}
}

// A census whose budget has run out (the process is past 540 s by the injected
// clock) reports a truncated scan, yet the run's outcome stays completed:
// deadline_hit 0 and last_success advances.
func TestJanitor_censusTimeoutCannotFlipOutcome(t *testing.T) {
	wt := t.TempDir()
	makeTree(t, wt, map[string][]int{"w1": {10, 20}})
	f, pts := censusFixture(t, wt, "a")
	p := f.censusPasses(pts)
	calls := 0
	base := time.Unix(1700000000, 0)
	p.now = func() time.Time { // first call: process start; later: 10 minutes on
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(10 * time.Minute)
	}
	code, out := f.run(p)
	if code != 0 || !strings.Contains(out, "outcome=completed") {
		t.Fatalf("exit %d out %q", code, out)
	}
	if g, ok := find(*pts, gaugeWorktreeTruncated, ""); !ok || g.value != 1 {
		t.Errorf("scan_truncated = %+v ok=%v, want 1", g, ok)
	}
	if g, ok := find(*pts, gaugeJanitorDeadlineHit, ""); !ok || g.value != 0 {
		t.Errorf("deadline_hit = %+v ok=%v, want 0", g, ok)
	}
	if _, ok := find(*pts, gaugeJanitorLastSuccess, ""); !ok {
		t.Error("last_success was not emitted by a completed run")
	}
	if ev := f.events(); len(ev) != 1 || ev[0]["outcome"] != janitorCompleted {
		t.Errorf("events = %v, want one completed row", ev)
	}
}

// The census seconds are not part of last_run_seconds: the heartbeat gauges are
// emitted before the census, so a slow scan cannot raise them.
func TestJanitor_censusEmittedAfterHeartbeat(t *testing.T) {
	wt := t.TempDir()
	makeTree(t, wt, map[string][]int{"w1": {1}})
	f, pts := censusFixture(t, wt, "a")
	f.run(f.censusPasses(pts))
	idx := func(name string) int {
		for i, p := range *pts {
			if p.name == name {
				return i
			}
		}
		return -1
	}
	if lr, c := idx(gaugeJanitorLastRun), idx(gaugeWorktreeCount); lr < 0 || c < 0 || lr > c {
		t.Errorf("last_run at %d, census count at %d: heartbeat must precede the census", lr, c)
	}
	if g, _ := find(*pts, gaugeJanitorLastRun, ""); g.value >= 5 {
		t.Errorf("last_run_seconds = %v, want the passes' time only", g.value)
	}
}

func buildInfos(pts []gaugePoint) map[string]string {
	out := map[string]string{}
	for _, p := range pts {
		if p.name == gaugeBuildInfo {
			if p.value != 1 {
				out["bad-value"] = "x"
			}
			out[p.labels["component"]] = p.labels["version"]
		}
	}
	return out
}

func TestJanitor_buildInfoSeries(t *testing.T) {
	f, pts := censusFixture(t, "", "a") // no worktree dir: build_info still emitted
	if code, _ := f.run(f.censusPasses(pts)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	got := buildInfos(*pts)
	if got[buildInfoComponentHandler] != "dev" || got[buildInfoComponentCCPool] != "0.0.0-abcd1234" || len(got) != 2 {
		t.Errorf("build_info = %v, want handler dev (unstamped) and ccpool 0.0.0-abcd1234", got)
	}
	if _, ok := find(*pts, gaugeWorktreeCount, ""); ok {
		t.Error("census gauges emitted with no worktree dir configured")
	}
}

func TestJanitor_buildInfoStampedHandlerVersion(t *testing.T) {
	old := version
	version = "0.0.0-deadbeef"
	t.Cleanup(func() { version = old })
	f, pts := censusFixture(t, "", "a")
	f.run(f.censusPasses(pts))
	if got := buildInfos(*pts)[buildInfoComponentHandler]; got != "0.0.0-deadbeef" {
		t.Errorf("handler version = %q", got)
	}
}

func TestJanitor_buildInfoUnknownWhenCCPoolVersionFails(t *testing.T) {
	f, pts := censusFixture(t, "", "a")
	p := f.censusPasses(pts)
	p.ccpoolVersion = func(context.Context) (string, error) { return "", errors.New("boom") }
	if code, out := f.run(p); code != 0 || !strings.Contains(out, "outcome=completed") {
		t.Fatalf("exit %d out %q: a failed ccpool version must not fail the pass", code, out)
	}
	if got := buildInfos(*pts); got[buildInfoComponentCCPool] != "unknown" || got[buildInfoComponentHandler] != "dev" {
		t.Errorf("build_info = %v", got)
	}
}
