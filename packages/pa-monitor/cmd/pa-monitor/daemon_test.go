package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/daemon"
)

// TestBuildRunOptions_WiringNotNil is a smoke test that catches the class
// of regression where runDaemon assembles a daemon.RunOptions but forgets
// to set one of the required fields the daemon depends on at runtime.
//
// History: this exact bug shipped twice.
//   - cae7449 fix(pa-monitor): wire WriteService + DB into production
//     buildPoller — Task 17 forgot to construct WriteService at all, so
//     contribution rows were silently dropped.
//   - 26f3c71 fix(pa-monitor): wire ReadService into production daemon —
//     Task 20/21 added ReadService to lifecycle.go but never set
//     opts.ReadService, so every gRPC client saw an empty DaemonState.
//
// Both regressions were invisible to the type system because RunOptions
// fields are nilable. This test exercises buildRunOptions with a known-
// good setup and asserts each required field is wired. If a future
// refactor drops one of the assignments — opts.WriteService = ws,
// opts.ReadService = rs, opts.SessionsDir = p.SessionsDir — this test
// fails with a clear message instead of users discovering the gap at
// runtime.
func TestBuildRunOptions_WiringNotNil(t *testing.T) {
	// Isolate paths so the DB lands in a tempdir, the pidfile + socket
	// don't collide with a real daemon, and nothing leaks between runs.
	dir := t.TempDir()
	paths := daemon.Paths{
		Dir:     dir,
		PIDFile: filepath.Join(dir, "daemon.pid"),
		Socket:  filepath.Join(dir, "daemon.sock"),
	}

	// Short-lived ctx scopes the background ccusage refresh goroutine
	// (started inside buildPoller) so it exits cleanly when the test
	// completes — no goroutine leaks.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Minimal config — buildRunOptions only consumes fields used during
	// assembly; behavior of the daemon loop is out of scope here.
	cfg := config.Config{
		PlanTier:        "max_5x",
		CaffeinateGrace: 60 * time.Second,
	}

	opts, cleanup, err := buildRunOptions(ctx, cfg, paths, "test",
		5*time.Second, false /* disablePoller */)
	if err != nil {
		t.Fatalf("buildRunOptions returned error: %v", err)
	}
	t.Cleanup(cleanup)

	// The three fields that have historically gone missing. Each check
	// emits a message pointing the future maintainer at what production
	// breaks if the wiring is dropped.
	if opts.WriteService == nil {
		t.Error("opts.WriteService is nil — production code path skipped DB / WriteService setup; contribution rows would not be persisted")
	}
	if opts.DB == nil {
		t.Error("opts.DB is nil — lifecycle.go requires opts.WriteService AND opts.DB to build the nudgeRecorder; without DB the dispatcher's NudgeRecorder stays nil and nudge_history rows (sent/failed/suppressed) are never persisted")
	}
	if opts.ReadService == nil {
		t.Error("opts.ReadService is nil — sharedState.snapshot() would return nil and all gRPC clients (CLI / TUI) would see an empty DaemonState")
	}
	if opts.SessionsDir == "" {
		t.Error("opts.SessionsDir is empty — the hourly GC sweeper goroutine cannot reconcile session .json files with the DB")
	}
	// The corpus Monitor is now mandatory: Snapshot requires it, so a buildPoller
	// that dropped the wiring would crash on the first tick (exercised by the
	// daemon tick integration tests). No separate flag guard is needed post-pg2-66h9g.
}
