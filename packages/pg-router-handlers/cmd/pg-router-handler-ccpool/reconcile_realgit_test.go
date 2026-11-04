package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// Role-pool closed-bead reconcile against REAL git: a session whose bead has
// closed keeps its worktree (and its session) while the worktree is dirty or holds
// a commit beyond the canonical clone's HEAD, and is torn down once clean and
// pushed. Hermetic fixture repository and linked worktrees from newSweepHarness.
func TestReconcileClosedBeadSessions_realGitKeepsUnpushedOrDirtyWork(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(h *sweepHarness, path string)
		wantKept bool
	}{
		{"clean and pushed", func(*sweepHarness, string) {}, false},
		{"dirty tracked-or-untracked file", func(h *sweepHarness, path string) {
			if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("wip\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"unpushed commit", func(h *sweepHarness, path string) {
			if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			h.git(path, "add", "work.txt")
			h.git(path, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-m", "work")
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newSweepHarness(t)
			path := h.leak("zr-done")
			c.prepare(h, path)
			cc := &fakeCC{ListSeq: [][]ccpool.Session{{{
				ExternalID: "pg-router-worker-zr-done",
				State:      ccpool.StateIdle,
				CWD:        path,
				Meta:       map[string]string{ccpool.MetaKeyBead: "zr-done"},
			}}}}
			br := fakeBR{out: map[string]string{"show zr-done --json": `{"status":"closed"}`}}
			n := reconcileClosedBeadSessions(context.Background(), cc, h.opener, br, "pg-router-", h.repo, "", nil)
			if c.wantKept {
				if n != 0 || len(cc.Closed) != 0 {
					t.Fatalf("session holding work must be kept; n=%d closed=%v", n, cc.Closed)
				}
				if !h.exists(path) {
					t.Fatalf("worktree %s must be kept", path)
				}
				return
			}
			if n != 1 || len(cc.Closed) != 1 {
				t.Fatalf("clean pushed session must be torn down; n=%d closed=%v", n, cc.Closed)
			}
		})
	}
}
