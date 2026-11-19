package hygiene

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router/internal/handlers/worktree"
)

// A worktree removal runs under its own deadline (worktree.RemoveWithin), against
// REAL git through the sweep harness. The slow and the killed removal are
// injected: a real `git worktree remove` of a fixture finishes in milliseconds, so
// the wrapper stands in for a very large checkout's removal and delegates to the
// real git client where the removal is meant to succeed.

// slowRemover wraps a real WorktreeManager. A removal sleeps for delay and then
// either delegates to git (kill=false) or simulates git being killed at the
// context deadline half-way through (kill=true): it deletes the `.git` file and
// leaves the rest, as `git worktree remove` does while it deletes the work tree
// and before it drops the registration, and reports the deadline error.
type slowRemover struct {
	gitclient.WorktreeManager
	delay time.Duration
	kill  bool
}

func (s slowRemover) RemoveWorktree(ctx context.Context, path string, force bool) error {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		if s.kill {
			_ = os.Remove(filepath.Join(path, ".git"))
		}
		return fmt.Errorf("git worktree remove: %w: signal: killed", ctx.Err())
	}
	return s.WorktreeManager.RemoveWorktree(ctx, path, force)
}

func (h *sweepHarness) registeredWorktrees() string {
	return h.git(h.repo, "worktree", "list", "--porcelain")
}

// The removal outlives the surrounding pass's deadline but finishes inside its own.
func TestWorktreeRemove_ownDeadlineOutlivesTheSurroundingPass(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-slow")
	wm, err := h.opener(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	slow := slowRemover{WorktreeManager: wm, delay: 300 * time.Millisecond}

	// Control: under the surrounding pass's own (short) deadline the same removal is killed.
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := slow.RemoveWorktree(short, path, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("control: removal under the short deadline = %v, want a deadline error", err)
	}
	if !h.exists(path) {
		t.Fatal("control: a killed slow removal must not have removed anything")
	}

	parent, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if err := worktree.RemoveWithin(parent, slow, path, false, 10*time.Second, nil); err != nil {
		t.Fatalf("RemoveWithin under its own deadline: %v", err)
	}
	if h.exists(path) {
		t.Errorf("worktree %s must be removed", path)
	}
	if got := h.registeredWorktrees(); containsPath(got, path) {
		t.Errorf("git must no longer register %s:\n%s", path, got)
	}
}

// The surrounding pass being cancelled (not merely out of time) still stops the removal.
func TestWorktreeRemove_parentCancellationStillStopsIt(t *testing.T) {
	h := newSweepHarness(t)
	path := h.leak("zr-cancel")
	wm, err := h.opener(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	slow := slowRemover{WorktreeManager: wm, delay: time.Minute}
	parent, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	err = worktree.RemoveWithin(parent, slow, path, false, time.Hour, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoveWithin = %v, want context.Canceled", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("a cancelled removal ran %v; it must stop promptly", time.Since(start))
	}
}

func containsPath(porcelain, path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	for _, p := range []string{path, resolved} {
		if p != "" && strings.Contains(porcelain, "worktree "+p+"\n") {
			return true
		}
	}
	return false
}
