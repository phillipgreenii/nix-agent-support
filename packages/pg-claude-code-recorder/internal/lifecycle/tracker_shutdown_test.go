package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/clock"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func newGraceTracker(t *testing.T, ctx context.Context, sys proc.System) (*Tracker, string) {
	t.Helper()
	st, err := store.OpenCtx(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.CloseCtx(ctx) })
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(TrackerConfig{
		Store: st, Sys: sys, ClaudeDir: dir, Clock: clock.Real{}, Ctx: ctx,
		GraceEnable: true, GraceWindow: time.Hour,
	})
	return tr, dir
}

func closesWithin(t *testing.T, tr *Tracker, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { tr.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("Tracker.Close still blocked after %v: a grace-window wait ignores the context", d)
	}
}

// An exit whose registry file is still present waits out the grace window; a
// cancelled root context ends that wait, so Close does not hold for an hour.
func TestTrackerOnExitGraceWaitStopsOnCancel(t *testing.T) {
	sys := proc.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	tr, dir := newGraceTracker(t, ctx, sys)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sys.Add(4101, start, nil)
	ps, _ := sys.StartTime(4101)
	tr.Observe(writeReg(t, dir, "4101", proc.FormatStart(ps), "s-grace"))
	sys.Exit(4101, start.Add(time.Hour)) // file stays: grace wait begins
	cancel()
	closesWithin(t, tr, 3*time.Second)
}

// A /clear (the registry names a new session while the Process lives) counts
// the old end after the grace window, on a goroutine Close waits for.
func TestTrackerCountEndAsyncGraceWaitStopsOnCancel(t *testing.T) {
	sys := proc.NewFake()
	ctx, cancel := context.WithCancel(context.Background())
	tr, dir := newGraceTracker(t, ctx, sys)
	sys.Add(4102, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil)
	ps, _ := sys.StartTime(4102)
	tr.Observe(writeReg(t, dir, "4102", proc.FormatStart(ps), "s-old"))
	tr.Observe(writeReg(t, dir, "4102", proc.FormatStart(ps), "s-new"))
	cancel()
	closesWithin(t, tr, 3*time.Second)
}

// A session change seen after Close starts no goroutine for Close to miss.
func TestTrackerCountEndAsyncAfterCloseStartsNothing(t *testing.T) {
	tr, _ := newGraceTracker(t, context.Background(), proc.NewFake())
	tr.Close()
	tr.countEndAsync("s-old")
	done := make(chan struct{})
	go func() { tr.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("countEndAsync after Close left a goroutine in the wait group")
	}
}
