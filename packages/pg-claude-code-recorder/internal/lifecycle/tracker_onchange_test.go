package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/proc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

func newOnChangeTracker(t *testing.T, sys proc.System, calls *int) (*Tracker, string) {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(TrackerConfig{Store: st, Sys: sys, ClaudeDir: dir, OnChange: func() { *calls++ }})
	t.Cleanup(tr.Close)
	return tr, dir
}

func writeReg(t *testing.T, dir string, pid, start string, session string) string {
	t.Helper()
	p := filepath.Join(dir, "sessions", pid+".json")
	body := `{"pid":` + pid + `,"procStart":"` + start + `","sessionId":"` + session + `"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A Process found already dead, a live one discovered, and a watched exit each
// change the live set, so each runs OnChange; a liveness refresh does not.
func TestTrackerRunsOnChangeWhenLiveSetChanges(t *testing.T) {
	sys := proc.NewFake()
	var calls int
	tr, dir := newOnChangeTracker(t, sys, &calls)

	// Dead at discovery: one discover+exit commit.
	tr.Observe(writeReg(t, dir, "4001", "x", "s-dead"))
	if calls != 1 {
		t.Fatalf("stale discovery ran OnChange %d times, want 1", calls)
	}

	// A live Process: discovery commits discover+seen.
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sys.Add(4002, start, nil)
	ps, err := sys.StartTime(4002)
	if err != nil {
		t.Fatal(err)
	}
	tr.Observe(writeReg(t, dir, "4002", proc.FormatStart(ps), "s-live"))
	if calls != 2 {
		t.Fatalf("live discovery ran OnChange %d times total, want 2", calls)
	}

	// Its exit.
	sys.Exit(4002, start.Add(time.Hour))
	tr.Close()
	if calls != 3 {
		t.Fatalf("exit ran OnChange %d times total, want 3", calls)
	}

	// A seen-only commit does not.
	tr.commit(context.Background(), store.ProcRec("seen", 4002, "x", 1))
	if calls != 3 {
		t.Fatalf("a seen-only commit ran OnChange (%d)", calls)
	}
}
