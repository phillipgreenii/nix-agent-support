package worktree

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type recGit struct {
	calls [][]string
	// existsAt: a path the fake reports as an existing worktree (rev-parse ok).
	existsAt string
}

func (g *recGit) Run(_ context.Context, dir string, args ...string) error {
	g.calls = append(g.calls, append([]string{dir}, args...))
	// Simulate "worktree already present": `git -C <path> rev-parse` succeeds only
	// for existsAt.
	if len(args) > 0 && args[0] == "rev-parse" {
		if dir == g.existsAt {
			return nil
		}
		return errNotARepo
	}
	return nil
}

func TestEnsure_createsFreshPerBeadWorktree(t *testing.T) {
	g := &recGit{}
	wtDir := t.TempDir()
	got, err := Ensure(context.Background(), g, wtDir, "/repo", "zr-6bq.3")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	want := filepath.Join(wtDir, "zr-6bq.3")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// Must have run `git -C /repo worktree add -B pr-pool/zr-6bq.3 <path>`.
	var added bool
	for _, c := range g.calls {
		joined := strings.Join(c, " ")
		if strings.Contains(joined, "/repo worktree add") &&
			strings.Contains(joined, "pr-pool/zr-6bq.3") &&
			strings.Contains(joined, want) {
			added = true
		}
	}
	if !added {
		t.Errorf("expected a `git -C /repo worktree add -B pr-pool/zr-6bq.3 %s`; calls=%v", want, g.calls)
	}
}

func TestEnsure_reusesExistingWorktree(t *testing.T) {
	wtDir := t.TempDir()
	path := filepath.Join(wtDir, "zr-1")
	g := &recGit{existsAt: path}
	got, err := Ensure(context.Background(), g, wtDir, "/repo", "zr-1")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	// Reuse path must NOT run `worktree add`.
	for _, c := range g.calls {
		if strings.Contains(strings.Join(c, " "), "worktree add") {
			t.Errorf("existing worktree must be reused, not re-added; calls=%v", g.calls)
		}
	}
}
