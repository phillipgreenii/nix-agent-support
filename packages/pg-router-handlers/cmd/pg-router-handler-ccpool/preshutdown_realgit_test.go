package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/gitclient"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// closeSinglePhase calls RemoveWorktree(s.CWD, force) for any session it purges,
// whatever the role's isolation type (bead pg2-96ogzy, R5). A role with `path`
// isolation runs in a directory that is not a git repository, so the teardown
// MUST find no worktree to remove and MUST NOT touch the repository a
// worktree-isolation role would have used. Proven here against real git: the
// opener is the production shape (gitclient.New over the fixture's hermetic
// environment, ceilinged at the fixture root so the search for a repository
// cannot leave it).
func TestCloseSinglePhase_pathIsolationNonGitCwdRemovesNothing(t *testing.T) {
	h := newSweepHarness(t)
	// A linked worktree and its branch that must survive: another role's work.
	other := filepath.Join(h.wtDir, "other-bead")
	if out, err := h.client(h.repo).Run(t.Context(), "worktree", "add", "-b", "pg-router/other-bead", other); err != nil {
		t.Fatalf("fixture worktree: %v\n%s", err, out)
	}

	cwd := filepath.Join(h.fixture, "workspace-root") // the non-git path-isolation directory
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	open := func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
		c, err := gitclient.New(
			ctx, dir,
			gitclient.WithHome(h.homeDir),
			gitclient.WithoutInherited("SSH_AUTH_SOCK"),
			gitclient.WithCeiling(h.fixture),
			gitclient.WithEnv("GIT_CONFIG_NOSYSTEM", "1"),
		)
		if err != nil {
			return nil, err
		}
		return c, nil
	}

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	cc := &fakeCC{}
	s := ccpool.Session{ExternalID: "pg2-split-triager-pg2-x", State: ccpool.StateIdle, CWD: cwd}
	if !closeSinglePhase(t.Context(), cc, open, h.repo, s, false) {
		t.Fatal("closeSinglePhase = false, want true: the row was purged")
	}

	if len(cc.Closed) != 1 {
		t.Errorf("session must be closed; closed=%v", cc.Closed)
	}
	if _, err := os.Stat(cwd); err != nil {
		t.Errorf("the path-isolation directory must be left in place: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another worktree must be left in place: %v", err)
	}
	if out, err := h.client(h.repo).Run(t.Context(), "worktree", "list", "--porcelain"); err != nil || !strings.Contains(string(out), "worktree "+other) {
		t.Errorf("git must still list %s; err=%v\n%s", other, err, out)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("a non-git cwd must tear down without a warning; log:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "no worktree to remove") {
		t.Errorf("teardown must report there is no worktree to remove; log:\n%s", buf.String())
	}
}
