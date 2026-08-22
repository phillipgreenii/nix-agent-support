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
)

// testCfg builds a minimal config.Config carrying only what the isolation
// strategies read (RepoRoot, WorktreeDir).
func testCfg(repoRoot, worktreeDir string) config.Config {
	return config.Config{RepoRoot: repoRoot, WorktreeDir: worktreeDir}
}

// fakeGit records every `git -C dir args...` invocation newIsolation's
// worktree strategy issues, and reports "already a worktree" (rev-parse
// succeeds) for any path in existsAt — mirroring worktree_test.go's recGit,
// reimplemented here (unexported) since that fake is internal to package
// worktree and this file lives in package executor.
type fakeGit struct {
	existsAt map[string]bool
	calls    [][]string
}

func (g *fakeGit) Run(_ context.Context, dir string, args ...string) error {
	g.calls = append(g.calls, append([]string{dir}, args...))
	if len(args) > 0 && args[0] == "rev-parse" {
		if g.existsAt[dir] {
			return nil
		}
		return errors.New("not a git worktree")
	}
	return nil
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
	g := &fakeGit{existsAt: map[string]bool{}}
	deps := Deps{Cfg: testCfg(dir, worktreeDir), Git: g}

	for _, typ := range []string{"", "worktree"} {
		got, err := newIsolation(roles.IsolationConfig{Type: typ}, deps).Ensure(context.Background(), "bead-1")
		if err != nil {
			t.Fatalf("Type=%q: unexpected error: %v", typ, err)
		}
		want := filepath.Join(worktreeDir, "bead-1")
		if got != want {
			t.Fatalf("Type=%q: workspaceRoot = %q, want %q", typ, got, want)
		}
	}
	// Both calls must have gone through the real worktree.Ensure — proven by
	// the `git worktree add` invocation appearing in the fake's call log.
	found := false
	for _, c := range g.calls {
		if len(c) > 1 && c[1] == "worktree" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a `git worktree add` call, got calls: %+v", g.calls)
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
