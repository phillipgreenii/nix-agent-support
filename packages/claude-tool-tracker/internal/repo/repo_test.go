package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cleanGitEnv(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@localhost", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@localhost")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestWorktreeMapsToMainCheckoutName(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "my-repo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "-q", "-b", "main")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(root, "elsewhere", "feature-wt")
	git(t, main, "worktree", "add", "-q", "-b", "feat", wt)
	sub := filepath.Join(wt, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, cwd, want string }{
		{"main checkout", main, "my-repo"},
		{"worktree root", wt, "my-repo"},
		{"directory inside worktree", sub, "my-repo"},
		{"not a repo", root, ""},
		{"relative path", "my-repo", ""},
		{"empty", "", ""},
	} {
		if got := Name(c.cwd); got != c.want {
			t.Errorf("%s: Name(%q) = %q, want %q", c.name, c.cwd, got, c.want)
		}
	}
}

func TestRemovedWorktreeUsesPathLayout(t *testing.T) {
	for cwd, want := range map[string]string{
		"/nonexistent-tracker-test/alpha/.worktrees/t2/pkg":  "alpha",
		"/nonexistent-tracker-test/beta/.claude/worktrees/x": "beta",
		"/nonexistent-tracker-test/gamma/plain/dir":          "",
		"/nonexistent-tracker-test/delta/.worktrees":         "",
	} {
		if got := Name(cwd); got != want {
			t.Errorf("Name(%q) = %q, want %q", cwd, got, want)
		}
	}
}

// cleanGitEnv is the environment without any GIT_* variable. A test that runs
// inside a git hook inherits GIT_DIR and GIT_INDEX_FILE, which would point its
// fixture git commands at the enclosing repository.
func cleanGitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}
