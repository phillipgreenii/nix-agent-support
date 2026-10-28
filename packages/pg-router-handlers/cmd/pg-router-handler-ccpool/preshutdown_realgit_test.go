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

// A "bead-worktree" session runs in a worktree a drain worker made on
// drain/<bead> (bead pg2-b49ydt, R3). Every teardown path runs for sessions of
// every role without the role in hand, and closeSinglePhase force-removes
// whatever linked worktree the session's cwd is, so the type travels on the row
// (ccpool.MetaKeyKeepWorktree) and the teardown must purge the row and remove
// NOTHING: not the worktree (dirty or not), not its branch. Proven against real
// git for both teardown shapes: the single-phase purge (a cwd outside the
// handler's worktree directory) and the two-phase purge (a cwd under it), through
// closeSession, finishPurge (the purge_pending retry) and the shutdown sweep.
func TestTeardown_beadWorktreeSessionNeverRemovesTheFoundWorktree(t *testing.T) {
	for _, where := range []string{"outside the handler's worktree dir", "inside the handler's worktree dir"} {
		for _, keep := range []bool{true, false} {
			name := where + "/row marked bead-worktree"
			if !keep {
				name = where + "/unmarked row (negative control: the removal is real)"
			}
			t.Run(name, func(t *testing.T) {
				h := newSweepHarness(t)
				dir := filepath.Join(h.fixture, "drain-wt")
				if where[0] == 'i' {
					dir = filepath.Join(h.wtDir, "bead-1")
				}
				const branch = "drain/bead-1"
				if out, err := h.client(h.repo).Run(t.Context(), "worktree", "add", "-b", branch, dir); err != nil {
					t.Fatalf("fixture worktree: %v\n%s", err, out)
				}
				if err := os.WriteFile(filepath.Join(dir, "uncommitted.txt"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}

				meta := map[string]string{ccpool.MetaKeyBead: "bead-1"}
				if keep {
					meta[ccpool.MetaKeyKeepWorktree] = "1"
				}
				s := ccpool.Session{ExternalID: "pg-router-triager-bead-1", State: ccpool.StateIdle, CWD: dir, Meta: meta}
				cc := &fakeCC{ListSeq: [][]ccpool.Session{{s}}}

				if !closeSession(t.Context(), cc, h.opener, h.repo, h.wtDir, s, false) {
					t.Fatal("closeSession = false, want true: the row was purged")
				}
				if len(cc.ClosedPurge) == 0 || !cc.ClosedPurge[len(cc.ClosedPurge)-1] {
					t.Errorf("the row must end purged; ClosedPurge=%v", cc.ClosedPurge)
				}

				list, err := h.client(h.repo).Run(t.Context(), "worktree", "list", "--porcelain")
				if err != nil {
					t.Fatal(err)
				}
				_, statErr := os.Stat(filepath.Join(dir, "uncommitted.txt"))
				listed := strings.Contains(string(list), "worktree "+dir+"\n")
				if keep {
					if statErr != nil || !listed {
						t.Errorf("the found worktree must be left untouched: stat=%v listed=%v\n%s", statErr, listed, list)
					}
					if ok, err := h.client(h.repo).RefExists(t.Context(), "refs/heads/"+branch); err != nil || !ok {
						t.Errorf("branch %s must survive: %v", branch, err)
					}
				} else if statErr == nil || listed {
					t.Errorf("negative control: without the marker the worktree must be force-removed (single-phase) or removed (two-phase); stat=%v listed=%v", statErr, listed)
				}
			})
		}
	}
}

// The purge_pending retry (finishPurge) and the shutdown sweep honour the same
// marker.
func TestTeardown_beadWorktreeMarkerHonouredByFinishPurgeAndShutdownSweep(t *testing.T) {
	h := newSweepHarness(t)
	dir := filepath.Join(h.wtDir, "bead-2") // under the worktree dir: the shape that is eligible for removal
	if out, err := h.client(h.repo).Run(t.Context(), "worktree", "add", "-b", "drain/bead-2", dir); err != nil {
		t.Fatalf("fixture worktree: %v\n%s", err, out)
	}
	s := ccpool.Session{
		ExternalID: "pg-router-triager-bead-2", State: ccpool.StateIdle, CWD: dir,
		Meta: map[string]string{
			ccpool.MetaKeyBead: "bead-2", ccpool.MetaKeyKeepWorktree: "1", ccpool.MetaKeyPurgePending: purgeMarkerValue,
		},
	}
	cc := &fakeCC{}
	if !finishPurge(t.Context(), cc, h.opener, h.repo, h.wtDir, s, false) {
		t.Fatal("finishPurge = false, want the row purged")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("finishPurge must leave the worktree: %v", err)
	}

	s.Meta = map[string]string{ccpool.MetaKeyBead: "bead-2", ccpool.MetaKeyKeepWorktree: "1"}
	cc = &fakeCC{ListSeq: [][]ccpool.Session{{s}}}
	if n := teardownAllSessions(t.Context(), cc, h.opener, &orphanBD{}, "pg-router-", h.repo, h.wtDir); n != 1 {
		t.Fatalf("shutdown sweep closed %d, want 1", n)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the shutdown sweep must leave the worktree: %v", err)
	}
	if ok, err := h.client(h.repo).RefExists(t.Context(), "refs/heads/drain/bead-2"); err != nil || !ok {
		t.Errorf("branch drain/bead-2 must survive: %v", err)
	}
}
