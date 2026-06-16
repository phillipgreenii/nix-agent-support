package watchdog

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/usage"
)

// recGit records reset/clean calls without touching disk.
type recGit struct{ ran [][]string }

func (g *recGit) Run(_ context.Context, dir string, args ...string) error {
	g.ran = append(g.ran, append([]string{dir}, args...))
	return nil
}

func TestSafeToReset_guard(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	// path == repoRoot -> never
	if safeToReset(ctx, repo, repo, repo) {
		t.Error("must refuse to reset repoRoot")
	}
	// outside worktreeDir -> never
	if safeToReset(ctx, "/somewhere/else", repo, filepath.Join(repo, "wt")) {
		t.Error("must refuse a path outside WorktreeDir")
	}
	// non-existent / not-a-worktree -> never (safe no-op)
	if safeToReset(ctx, filepath.Join(repo, "wt", "ghost"), repo, filepath.Join(repo, "wt")) {
		t.Error("must refuse a non-worktree path")
	}
}

func TestTerminal_unclaimsNotesNoHuman(t *testing.T) {
	cc := &fakeCC{}
	bd := &recBD{}
	wd := newWD(&fakeReader{seq: []usage.Snapshot{{}}}, cc, bd, tokBudget(1000))
	wd.Git = &recGit{}
	// session cwd == repoRoot -> reset is a guarded no-op (the v1 reality)
	cc.list = []ccpool.Session{{ExternalID: "s", CWD: "/repo"}}
	wd.terminal(context.Background(), "s", "zr-1")
	if !bd.has("update zr-1 --status=open --assignee=") {
		t.Errorf("must unclaim; calls=%v", bd.calls)
	}
	for _, c := range bd.calls {
		if c == "update zr-1 --add-label human" {
			t.Error("must NOT add human")
		}
	}
}
