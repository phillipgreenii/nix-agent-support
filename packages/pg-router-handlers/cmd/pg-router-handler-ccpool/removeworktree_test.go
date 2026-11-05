package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-handlers/internal/worktree"
	"github.com/phillipgreenii/pg-router/conformance"
)

// pacedOpener is the production opener's shape (worktree.ThrottledOpener) with
// a tiny batch, recorded pauses and a recorded fsmonitor stop, over the
// harness's hermetic git environment.
func (h *sweepHarness) pacedOpener(batch int, pauses *[]time.Duration, stopped *[]string) worktree.Opener {
	return worktree.ThrottledOpener(
		worktree.Removal{
			BatchSize: batch,
			Pause:     time.Second,
			Sleep: func(_ context.Context, d time.Duration) error {
				*pauses = append(*pauses, d)
				return nil
			},
			StopFsmonitor: func(_ context.Context, p string) { *stopped = append(*stopped, p) },
		},
		gitclient.WithHome(h.homeDir),
		gitclient.WithoutInherited("SSH_AUTH_SOCK"),
		gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
	)
}

// addWorktree adds a linked worktree <wtDir>/<bead> and fills it with
// (clean until a caller adds files).
func (h *sweepHarness) addWorktree(bead string) string {
	h.t.Helper()
	path := filepath.Join(h.wtDir, bead)
	if out, err := h.client(h.repo).Run(h.t.Context(), "worktree", "add", "-b", "pg-router/"+bead, path); err != nil {
		h.t.Fatalf("fixture worktree: %v\n%s", err, out)
	}
	return path
}

// The nix sweep's removal path: a clean worktree is removed with real git
// through the paced trash drain. No unpaced bulk delete: the tree leaves its
// path by one rename, and every file in it is unlinked in batches separated by
// a pause. Cleanup is complete: path, trash and registration are gone.
func TestRemoveWorktree_cleanWorktreeIsRemovedPaced(t *testing.T) {
	h := newSweepHarness(t)
	wt := h.addWorktree("sweep-clean")
	pauses, stopped := []time.Duration{}, []string{}
	var out, errb bytes.Buffer
	code := serveRemoveWorktree(t.Context(), &out, &errb, []string{wt}, h.pacedOpener(1, &pauses, &stopped))
	if code != conformance.ExitOK {
		t.Fatalf("exit %d, stderr: %s", code, errb.String())
	}
	if _, err := os.Lstat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree path still exists: %v", err)
	}
	if es, _ := os.ReadDir(filepath.Join(h.wtDir, worktree.DefaultTrashDirName)); len(es) != 0 {
		t.Errorf("trash not drained: %d entries", len(es))
	}
	list, err := h.client(h.repo).Run(t.Context(), "worktree", "list", "--porcelain")
	if err != nil || strings.Contains(string(list), wt) {
		t.Errorf("git still registers the worktree (err=%v):\n%s", err, list)
	}
	if len(stopped) != 1 || stopped[0] != wt {
		t.Errorf("fsmonitor daemon stop = %v, want exactly [%s]", stopped, wt)
	}
	if len(pauses) == 0 {
		t.Error("no pause was taken: the delete was not paced")
	}
	// The branch is unpinned, so the shell sweep's `branch -D` can follow.
	if out, err := h.client(h.repo).Run(t.Context(), "branch", "-D", "pg-router/sweep-clean"); err != nil {
		t.Errorf("branch -D after removal: %v\n%s", err, out)
	}
}

// Teardown safety properties kept: a dirty, a locked and an unregistered tree
// are refused and left intact, with nothing moved into the trash.
func TestRemoveWorktree_refusesDirtyLockedAndUnregistered(t *testing.T) {
	h := newSweepHarness(t)
	dirty := h.addWorktree("sweep-dirty")
	if err := os.WriteFile(filepath.Join(dirty, "scratch.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := h.addWorktree("sweep-locked")
	if out, err := h.client(h.repo).Run(t.Context(), "worktree", "lock", locked); err != nil {
		t.Fatalf("lock: %v\n%s", err, out)
	}
	plain := filepath.Join(h.wtDir, "sweep-plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	pauses, stopped := []time.Duration{}, []string{}
	for _, p := range []string{dirty, locked, plain} {
		var out, errb bytes.Buffer
		if code := serveRemoveWorktree(t.Context(), &out, &errb, []string{p}, h.pacedOpener(1, &pauses, &stopped)); code != 1 {
			t.Errorf("%s: exit %d, want 1 (refused); stderr: %s", filepath.Base(p), code, errb.String())
		}
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed despite the refusal: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dirty, "scratch.txt")); err != nil {
		t.Errorf("dirty worktree's uncommitted file is gone: %v", err)
	}
	if es, _ := os.ReadDir(filepath.Join(h.wtDir, worktree.DefaultTrashDirName)); len(es) != 0 {
		t.Errorf("a refused worktree reached the trash: %d entries", len(es))
	}
	if len(pauses) != 0 || len(stopped) != 0 {
		t.Errorf("a refusal drained or stopped a daemon: pauses=%v stopped=%v", pauses, stopped)
	}
}

func TestRemoveWorktree_usage(t *testing.T) {
	for _, args := range [][]string{nil, {}, {""}, {"-x"}, {"a", "b"}} {
		var out, errb bytes.Buffer
		if got := serveRemoveWorktree(t.Context(), &out, &errb, args, nil); got != conformance.ExitUsage {
			t.Errorf("args %q: exit %d, want %d", args, got, conformance.ExitUsage)
		}
	}
	if got := run([]string{"remove-worktree"}); got != conformance.ExitUsage {
		t.Errorf("run remove-worktree with no path: exit %d, want %d", got, conformance.ExitUsage)
	}
}
