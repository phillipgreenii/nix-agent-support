package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/x/gitclient"
)

// testCfg builds a minimal config.Config carrying only what the isolation
// strategies read (RepoRoot, WorktreeDir).
func testCfg(repoRoot, worktreeDir string) config.Config {
	return config.Config{RepoRoot: repoRoot, WorktreeDir: worktreeDir}
}

// fakeWTM records every CreateWorktree call newIsolation's worktree strategy
// issues via the fake Opener below.
type fakeWTM struct {
	calls [][]string
}

func (m *fakeWTM) CreateWorktree(_ context.Context, path, branch string, opts gitclient.CreateWorktreeOptions) error {
	m.calls = append(m.calls, []string{"add", path, branch})
	return nil
}
func (m *fakeWTM) RemoveWorktree(context.Context, string, bool) error { return nil }
func (m *fakeWTM) PruneWorktrees(context.Context) error               { return nil }

// fakeGitOpener is a fake worktree.Opener: it reports gitclient.ErrNotARepository
// for any dir in missingAt (i.e. not yet a worktree) and otherwise succeeds,
// returning a shared fakeWTM so CreateWorktree calls are observable — mirroring
// worktree_test.go's recOpener, reimplemented here (unexported) since that fake
// is internal to package worktree and this file lives in package executor.
type fakeGitOpener struct {
	missingAt map[string]bool
	wtm       fakeWTM
	calls     []string
}

func (g *fakeGitOpener) Open(_ context.Context, dir string) (gitclient.WorktreeManager, error) {
	g.calls = append(g.calls, dir)
	if g.missingAt[dir] {
		return nil, gitclient.ErrNotARepository
	}
	return &g.wtm, nil
}

// fakeCmd is a scripted query.Commander for the workforest strategy's `pn
// workspace ...` shellouts: each call's argv (joined by spaces) is looked up
// in outputs; a missing key is a test-authoring bug (fails loudly), not a
// runtime fallback.
type fakeCmd struct {
	outputs map[string]string
	calls   [][]string
}

func (c *fakeCmd) Run(_ context.Context, argv []string) ([]byte, error) {
	c.calls = append(c.calls, argv)
	key := strings.Join(argv, " ")
	out, ok := c.outputs[key]
	if !ok {
		return nil, errors.New("fakeCmd: unscripted call: " + key)
	}
	return []byte(out), nil
}

func TestNewIsolation_worktreeIsDefault(t *testing.T) {
	dir := t.TempDir()
	worktreeDir := filepath.Join(dir, "worktrees")
	want := filepath.Join(worktreeDir, "bead-1")
	g := &fakeGitOpener{missingAt: map[string]bool{want: true}}
	deps := Deps{Cfg: testCfg(dir, worktreeDir), GitOpener: g.Open}

	for _, typ := range []string{"", "worktree"} {
		got, err := newIsolation(roles.IsolationConfig{Type: typ}, deps).Ensure(context.Background(), "bead-1")
		if err != nil {
			t.Fatalf("Type=%q: unexpected error: %v", typ, err)
		}
		if got != want {
			t.Fatalf("Type=%q: workspaceRoot = %q, want %q", typ, got, want)
		}
	}
	// Both calls must have gone through the real worktree.Ensure — proven by
	// the CreateWorktree invocation appearing in the fake's call log.
	if len(g.wtm.calls) == 0 {
		t.Fatalf("expected a CreateWorktree call, got none; opens=%v", g.calls)
	}
}

func TestNewIsolation_none(t *testing.T) {
	deps := Deps{Cfg: testCfg("/repo-root", "/worktrees")}
	got, err := newIsolation(roles.IsolationConfig{Type: "none"}, deps).Ensure(context.Background(), "bead-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/repo-root" {
		t.Fatalf("workspaceRoot = %q, want RepoRoot unchanged", got)
	}
}

func TestNewIsolation_path(t *testing.T) {
	dir := t.TempDir()
	scratch := filepath.Join(dir, "scratch", "nested")
	deps := Deps{Cfg: testCfg("/repo-root", "/worktrees")}
	got, err := newIsolation(roles.IsolationConfig{Type: "path", Path: scratch}, deps).Ensure(context.Background(), "bead-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != scratch {
		t.Fatalf("workspaceRoot = %q, want %q", got, scratch)
	}
	if info, err := os.Stat(scratch); err != nil || !info.IsDir() {
		t.Fatalf("path isolation must create the directory: stat error %v", err)
	}
	// A second Ensure for a DIFFERENT item id must reuse the same fixed path,
	// not derive a per-item one (unlike worktree/workforest).
	got2, err := newIsolation(roles.IsolationConfig{Type: "path", Path: scratch}, deps).Ensure(context.Background(), "bead-2")
	if err != nil {
		t.Fatal(err)
	}
	if got2 != scratch {
		t.Fatalf("workspaceRoot for a second item = %q, want the same fixed path %q", got2, scratch)
	}
}

func TestNewIsolation_workforestCreatesWhenAbsent(t *testing.T) {
	deps := Deps{Cfg: testCfg("/repo-root", "/worktrees")}
	cmd := &fakeCmd{outputs: map[string]string{
		"pn workspace info --json":           `{"root":"/home/tcadmin/workspace"}`,
		"pn workspace workforest list":       "no coordinated workforest sets\n",
		"pn workspace workforest add bead-1": "created bead-1\n",
	}}
	deps.Cmd = cmd
	got, err := newIsolation(roles.IsolationConfig{Type: "workforest"}, deps).Ensure(context.Background(), "bead-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "/home/tcadmin/workspace/.workforests/bead-1"
	if got != want {
		t.Fatalf("workspaceRoot = %q, want %q", got, want)
	}
	addCalled := false
	for _, c := range cmd.calls {
		if strings.Join(c, " ") == "pn workspace workforest add bead-1" {
			addCalled = true
		}
	}
	if !addCalled {
		t.Fatalf("expected `pn workspace workforest add bead-1`, got calls: %+v", cmd.calls)
	}
}

func TestNewIsolation_workforestReusesWhenPresent(t *testing.T) {
	deps := Deps{Cfg: testCfg("/repo-root", "/worktrees")}
	cmd := &fakeCmd{outputs: map[string]string{
		"pn workspace info --json":     `{"root":"/home/tcadmin/workspace"}`,
		"pn workspace workforest list": "bead-1\thomelab,nix-agent-support\n",
	}}
	deps.Cmd = cmd
	got, err := newIsolation(roles.IsolationConfig{Type: "workforest"}, deps).Ensure(context.Background(), "bead-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "/home/tcadmin/workspace/.workforests/bead-1"
	if got != want {
		t.Fatalf("workspaceRoot = %q, want %q", got, want)
	}
	for _, c := range cmd.calls {
		if strings.Join(c, " ") == "pn workspace workforest add bead-1" {
			t.Fatalf("must NOT call `add` when list already shows the set: calls = %+v", cmd.calls)
		}
	}
}

func TestNewIsolation_unknownTypeFails(t *testing.T) {
	deps := Deps{Cfg: testCfg("/repo-root", "/worktrees")}
	_, err := newIsolation(roles.IsolationConfig{Type: "bogus"}, deps).Ensure(context.Background(), "bead-1")
	if err == nil {
		t.Fatal("unknown isolation type must fail Ensure rather than silently pick a strategy")
	}
}
