package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "pg-git-lander ") {
		t.Fatalf("unexpected output %q", out.String())
	}
}

func TestRunNoArgsIsUsageError(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// buildBinary builds the real pg-git-lander into a temporary directory.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pg-git-lander")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestBuiltBinarySmokeRebaseAndLand runs the built binary against a throwaway
// fixture repository: a rebase of a trivial branch, then its land.
func TestBuiltBinarySmokeRebaseAndLand(t *testing.T) {
	bin := buildBinary(t)
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	g := &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(os.Environ())}
	git := func(dir string, args ...string) string {
		t.Helper()
		r := g.Run(t.Context(), dir, args...)
		if !r.OK() {
			t.Fatalf("git %v: %s", args, r.Stderr)
		}
		return strings.TrimSpace(string(r.Stdout))
	}
	mainBranch := git(repo.Dir, "symbolic-ref", "--short", "HEAD")
	wt := filepath.Join(root, "wt-x")
	git(repo.Dir, "worktree", "add", "-q", "-b", "feature/x", wt)
	write := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(dir, "add", "-A")
		git(dir, "commit", "-q", "-m", "add "+name)
	}
	write(wt, "x.txt")
	write(repo.Dir, "m.txt")

	xdg := t.TempDir()
	home := filepath.Join(root, "home")
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + xdg,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	}
	run := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir, cmd.Env = repo.Dir, env
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), code
	}

	out, code := run("rebase", "feature/x", "--onto", mainBranch, "--json")
	if code != 0 || !strings.Contains(out, `"outcome":"rebased"`) {
		t.Fatalf("rebase: exit %d: %s", code, out)
	}
	out, code = run("land", "feature/x", "--into", mainBranch)
	if code != 0 || !strings.HasPrefix(out, "landed feature/x into "+mainBranch) {
		t.Fatalf("land: exit %d: %s", code, out)
	}
	if got := git(repo.Dir, "ls-tree", "--name-only", mainBranch, "x.txt"); got != "x.txt" {
		t.Fatalf("x.txt not on %s: %q", mainBranch, got)
	}
}
