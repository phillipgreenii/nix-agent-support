package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// writeFile creates path's parent dirs and writes an arbitrary byte, for
// building test .beads fixtures.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestScanBeadsDirs_FindsRootAndRepoLevel is the pg2-zjopv shape: a
// workspace root's OWN .beads (root level) plus each repo's .beads
// (one level under the root) are both scanned; an already-defused
// `issues.jsonl.disabled-*` rename and a `.beads` dir with no issues.jsonl
// at all must NOT match.
func TestScanBeadsDirs_FindsRootAndRepoLevel(t *testing.T) {
	root := t.TempDir()
	rootStale := filepath.Join(root, ".beads", "issues.jsonl")
	writeFile(t, rootStale)

	repoAStale := filepath.Join(root, "repoA", ".beads", "issues.jsonl")
	writeFile(t, repoAStale)

	// Defused sibling: must not match.
	writeFile(t, filepath.Join(root, "repoB", ".beads", "issues.jsonl.disabled-20260911"))
	// .beads dir present but the file itself absent: must not match.
	if err := os.MkdirAll(filepath.Join(root, "repoC", ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir repoC/.beads: %v", err)
	}

	got := scanBeadsDirs([]string{root})
	want := []string{repoAStale, rootStale}
	if len(got) != len(want) {
		t.Fatalf("scanBeadsDirs = %v, want %v", got, want)
	}
	// scanBeadsDirs sorts its result; sort `want` the same way for comparison.
	if got[0] > got[1] {
		t.Fatalf("expected sorted output, got %v", got)
	}
	gotSet := map[string]bool{got[0]: true, got[1]: true}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("scanBeadsDirs missing %s, got %v", w, got)
		}
	}
}

// TestScanBeadsDirs_SkipsDotDirs proves the scan does NOT descend into a
// dot-prefixed child (.worktrees, .git, .claude, ...) even when it holds a
// same-shaped .beads/issues.jsonl — a worktree's .beads is typically a
// symlink back to its parent repo's own, so re-matching it would double-count
// (or, worse, alert on a path a human already resolved at the real location).
func TestScanBeadsDirs_SkipsDotDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".worktrees", "feature-x", ".beads", "issues.jsonl"))

	got := scanBeadsDirs([]string{root})
	if len(got) != 0 {
		t.Errorf("scanBeadsDirs should skip dot-prefixed children, got %v", got)
	}
}

// TestScanBeadsDirs_MissingRootSkipped: a nonexistent or empty root must not
// panic or error — RunOnce has to survive a transiently-unmounted workspace.
func TestScanBeadsDirs_MissingRootSkipped(t *testing.T) {
	got := scanBeadsDirs([]string{filepath.Join(t.TempDir(), "does-not-exist"), ""})
	if len(got) != 0 {
		t.Errorf("scanBeadsDirs(missing root) = %v, want empty", got)
	}
}

// fakeAlertClock is an injectable, manually-advanced clock, mirroring
// internal/otel's fakeClock for the export-health tracker tests.
type fakeAlertClock struct{ t time.Time }

func (c *fakeAlertClock) now() time.Time          { return c.t }
func (c *fakeAlertClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// TestStaleFileAlerts_FirstSightingAlwaysAlerts_ThenThrottled is the
// health-tracker-mirrored contract (pg2-zjopv): a path's first-ever sighting
// always alerts; a repeat sighting within the throttle window does not; after
// the window elapses it alerts again.
func TestStaleFileAlerts_FirstSightingAlwaysAlerts_ThenThrottled(t *testing.T) {
	clk := &fakeAlertClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	a := newStaleFileAlerts(clk.now, time.Hour)

	if !a.due("/ws/.beads/issues.jsonl") {
		t.Fatal("first sighting of a path must alert")
	}
	clk.advance(30 * time.Minute)
	if a.due("/ws/.beads/issues.jsonl") {
		t.Error("repeat sighting within the throttle window must not alert")
	}
	clk.advance(31 * time.Minute) // crosses the 1h window
	if !a.due("/ws/.beads/issues.jsonl") {
		t.Error("sighting after the throttle window elapses must alert again")
	}
}

// TestStaleFileAlerts_PruneResetsAbsentPaths: once a path is pruned (because
// a scan no longer finds it — a human renamed it away), it must alert again
// immediately on reappearing, not stay throttled against the stale memory of
// the earlier incident.
func TestStaleFileAlerts_PruneResetsAbsentPaths(t *testing.T) {
	clk := &fakeAlertClock{t: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	a := newStaleFileAlerts(clk.now, time.Hour)

	path := "/ws/.beads/issues.jsonl"
	if !a.due(path) {
		t.Fatal("first sighting must alert")
	}
	clk.advance(time.Minute) // well within the throttle window
	a.prune(nil)             // the file is gone from this scan
	if !a.due(path) {
		t.Error("a path forgotten by prune() must alert again immediately, not stay throttled")
	}
}

// beadsWatchCounterTotal collects the pa_monitor.beads.stale_export_found_total
// counter from a ManualReader (built via the package's existing
// newReaderEmitter helper, tick_metrics_test.go) and returns its summed value
// plus the number of distinct data points (one per distinct `path` attr).
func beadsWatchCounterTotal(t *testing.T, reader interface {
	Collect(ctx context.Context, rm *metricdata.ResourceMetrics) error
},
) (int64, int) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "pa_monitor.beads.stale_export_found_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("stale_export_found_total is %T, want Sum[int64]", m.Data)
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total, len(sum.DataPoints)
		}
	}
	return 0, 0
}

// TestBeadsWatcher_RunOnce_AlertsOnceThenThrottles drives BeadsWatcher end to
// end: a stale file is found, the OTel counter fires exactly once, and a
// second RunOnce (well within the 24h default throttle) does not fire again.
func TestBeadsWatcher_RunOnce_AlertsOnceThenThrottles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".beads", "issues.jsonl"))

	e, reader := newReaderEmitter(t)
	var warnLines []string
	w := &BeadsWatcher{
		Roots:   []string{root},
		Emitter: e,
		Warn:    func(line string) { warnLines = append(warnLines, line) },
	}

	w.RunOnce()
	total, points := beadsWatchCounterTotal(t, reader)
	if points != 1 || total != 1 {
		t.Fatalf("after first RunOnce: total=%d points=%d, want 1/1", total, points)
	}
	if len(warnLines) != 1 {
		t.Fatalf("warnLines = %v, want exactly 1 line", warnLines)
	}

	w.RunOnce() // still well within the default 24h throttle
	total, _ = beadsWatchCounterTotal(t, reader)
	if total != 1 {
		t.Errorf("after second (throttled) RunOnce: total=%d, want still 1", total)
	}
	if len(warnLines) != 1 {
		t.Errorf("warnLines after throttled RunOnce = %v, want still exactly 1", warnLines)
	}
}

// TestBeadsWatcher_RunOnce_RealertsAfterPathDisappears: once the stale file is
// gone (a human resolved it), a scan finding nothing prunes it from the
// throttle memory; if the SAME path reappears later it must alert again
// immediately rather than staying throttled against the earlier incident.
func TestBeadsWatcher_RunOnce_RealertsAfterPathDisappears(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".beads", "issues.jsonl")
	writeFile(t, path)

	e, reader := newReaderEmitter(t)
	w := &BeadsWatcher{Roots: []string{root}, Emitter: e, Warn: func(string) {}}

	w.RunOnce()
	total, _ := beadsWatchCounterTotal(t, reader)
	if total != 1 {
		t.Fatalf("after first RunOnce: total=%d, want 1", total)
	}

	// Human resolves it (rename away): the next scan sees nothing.
	if err := os.Rename(path, path+".disabled-20260911"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	w.RunOnce()
	total, _ = beadsWatchCounterTotal(t, reader)
	if total != 1 {
		t.Fatalf("after resolve scan: total=%d, want still 1 (no new alert for an absent file)", total)
	}

	// It reappears at the SAME path (e.g. a fresh `bd export` mistake): must
	// alert again immediately, not be throttled.
	writeFile(t, path)
	w.RunOnce()
	total, _ = beadsWatchCounterTotal(t, reader)
	if total != 2 {
		t.Errorf("after reappearance: total=%d, want 2 (a fresh sighting of the same path must re-alert)", total)
	}
}

// TestBeadsWatcher_Run_NoopWhenRootsEmpty: Run must return immediately (no
// ticker started, no goroutine leak) when Roots is empty — the watcher is off
// until an operator configures [beads_watch].roots.
func TestBeadsWatcher_Run_NoopWhenRootsEmpty(t *testing.T) {
	w := &BeadsWatcher{}
	done := make(chan struct{})
	go func() {
		w.Run(context.Background()) // must return without ctx ever being cancelled
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run with empty Roots did not return promptly")
	}
}

// TestBeadsWatcher_Run_ScansImmediatelyOnStart proves Run's first scan
// happens on entry (mirroring GCSweeper.Run), not after waiting a full
// Interval — the observed hazard sat unnoticed for ~2 weeks, so a newly
// (re)started daemon must not wait a further full interval before its first
// check.
func TestBeadsWatcher_Run_ScansImmediatelyOnStart(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".beads", "issues.jsonl"))

	e, reader := newReaderEmitter(t)
	ctx, cancel := context.WithCancel(context.Background())
	w := &BeadsWatcher{
		Roots:    []string{root},
		Interval: time.Hour, // long enough that only the immediate scan can have fired
		Emitter:  e,
		Warn:     func(string) {},
	}
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		total, _ := beadsWatchCounterTotal(t, reader)
		if total >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not perform its immediate scan promptly")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// Reuse otel.Emitter's nil-safety even inside this package: a BeadsWatcher
// built with a nil Emitter (OTel disabled) must not panic, since Emitter's
// own Record methods are nil-receiver-safe.
func TestBeadsWatcher_RunOnce_NilEmitterSafe(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".beads", "issues.jsonl"))

	var e *otel.Emitter
	w := &BeadsWatcher{Roots: []string{root}, Emitter: e, Warn: func(string) {}}
	w.RunOnce() // must not panic
}
