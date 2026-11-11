package gitexec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/cmdruntest"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

// fixtureGit builds a Git over real git whose environment is the fixture's
// hermetic one (fixture HOME, no system config) plus extra entries.
func fixtureGit(t *testing.T, repo *gitfixture.Repo, extra ...string) *gitexec.Git {
	t.Helper()
	root := repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
		"GIT_CEILING_DIRECTORIES="+root, // must be removed by the layer
	)
	env = append(env, extra...)
	return &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)}
}

func TestEveryCommandCarriesPinnedOptions(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.Any).Return(cmdruntest.Out(""))
	g := &gitexec.Git{Runner: fake.Runner(), Env: seams.FilteredEnv([]string{"PATH=/bin"})}
	g.Run(context.Background(), "/d", "status")
	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	c := calls[0]
	want := append(append([]string{}, gitexec.PinnedOptions...), "status")
	if strings.Join(c.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v", c.Args)
	}
	if c.Name != "git" || c.Dir != "/d" || !c.OwnProcGroup || c.EnvMode != cmdrun.EnvExact {
		t.Fatalf("call = %+v", c)
	}
}

func TestPinnedOptionsListMatchesDesign(t *testing.T) {
	got := strings.Join(gitexec.PinnedOptions, " ")
	for _, s := range []string{
		"rerere.enabled=false", "rerere.autoUpdate=false", "rebase.backend=merge",
		"rebase.autoStash=false", "rebase.updateRefs=false", "rebase.autoSquash=false",
		"core.editor=true", "sequence.editor=true", "gc.auto=0", "maintenance.auto=false",
	} {
		if !strings.Contains(got, "-c "+s) {
			t.Errorf("missing pinned option %s", s)
		}
	}
}

func TestPinnedOptionsWinAgainstHostileConfig(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	ctx := t.Context()
	for _, kv := range [][2]string{
		{"rerere.enabled", "true"},
		{"rerere.autoUpdate", "true"},
		{"rebase.autoStash", "true"},
		{"gc.auto", "999"},
	} {
		if _, err := repo.Client.Run(ctx, "config", kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	g := fixtureGit(t, repo)
	for key, want := range map[string]string{
		"rerere.enabled":    "false",
		"rerere.autoupdate": "false",
		"rebase.autostash":  "false",
		"rebase.backend":    "merge",
		"gc.auto":           "0",
		"core.editor":       "true",
	} {
		r := g.Run(ctx, repo.Dir, "config", "--get", key)
		if got := strings.TrimSpace(string(r.Stdout)); got != want {
			t.Errorf("%s = %q, want %q (stderr %q)", key, got, want, r.Stderr)
		}
	}
	// Without the layer the hostile value is what git sees (control).
	out, err := repo.Client.Run(ctx, "config", "--get", "rerere.enabled")
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("control: hostile config not in effect: %q %v", out, err)
	}
}

func TestChildKeepsEnvironmentExceptRepoPointingVars(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	hooks := filepath.Join(repo.Root(), "hooks")
	envOut := filepath.Join(repo.Root(), "hook-env.txt")
	script := "#!/bin/sh\nenv > '" + envOut + "'\n"
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	extra := []string{
		"XDG_CONFIG_HOME=/xdg-keep", "TMPDIR=/tmp-keep", "GNUPGHOME=/gnupg-keep",
	}
	for _, v := range seams.RepoPointingVars {
		if v == "GIT_CEILING_DIRECTORIES" {
			continue // set by fixtureGit
		}
		extra = append(extra, v+"=/elsewhere")
	}
	// A repository-pointing GIT_DIR would break git itself, so leave the
	// ones git acts on out of the live environment and check the hook's view
	// of the rest.
	g := fixtureGit(t, repo, extra...)
	r := g.Run(t.Context(), repo.Dir, "commit", "--allow-empty", "-m", "x")
	if !r.OK() {
		t.Fatalf("commit failed: %+v %s", r.Result, r.Stderr)
	}
	b, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatalf("hook did not run: %v", err)
	}
	seen := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(l, "=")
		seen[k] = v
	}
	for k, v := range map[string]string{
		"XDG_CONFIG_HOME": "/xdg-keep", "TMPDIR": "/tmp-keep", "GNUPGHOME": "/gnupg-keep",
		"GIT_CONFIG_GLOBAL": filepath.Join(repo.Root(), "home", ".gitconfig"),
	} {
		if seen[k] != v {
			t.Errorf("hook env %s = %q, want %q", k, seen[k], v)
		}
	}
	for _, k := range seams.RepoPointingVars {
		if v, ok := seen[k]; ok && v == "/elsewhere" || k == "GIT_CEILING_DIRECTORIES" && seen[k] == repo.Root() {
			t.Errorf("hook env still has %s=%s", k, v)
		}
	}
}

func TestTimeoutKillsWholeProcessGroup(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	pidFile := filepath.Join(repo.Root(), "grandchild.pid")
	script := "#!/bin/sh\nsleep 300 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(filepath.Join(repo.Root(), "hooks", "pre-commit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g := fixtureGit(t, repo)
	g.Timeout = 1500 * time.Millisecond

	start := time.Now()
	r := g.Run(t.Context(), repo.Dir, "commit", "--allow-empty", "-m", "x")
	if !r.TimedOut {
		t.Fatalf("not reported as timeout: %+v", r.Result)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("timeout took %v", time.Since(start))
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("hook never started its grandchild: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		// Signal 0 succeeds on a zombie, so also accept a reaped-by-init gone pid.
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestParentCancelIsNotReportedAsTimeout(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	g := fixtureGit(t, repo)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if r := g.Run(ctx, repo.Dir, "status"); r.TimedOut {
		t.Fatal("cancel reported as timeout")
	}
}

func TestCommonDirAndWorktrees(t *testing.T) {
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	g := fixtureGit(t, repo)
	wt := filepath.Join(repo.Root(), "wt")
	if r := g.Run(t.Context(), repo.Dir, "worktree", "add", "-b", "feat", wt); !r.OK() {
		t.Fatalf("worktree add: %s", r.Stderr)
	}
	dir, err := g.CommonDir(t.Context(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repo.Dir, ".git"); dir != want {
		t.Fatalf("common dir = %q, want %q", dir, want)
	}
	wts, err := g.Worktrees(t.Context(), repo.Dir)
	if err != nil || len(wts) != 2 {
		t.Fatalf("worktrees = %+v, %v", wts, err)
	}
	if wts[0].Path != repo.Dir || wts[0].Branch != "refs/heads/main" {
		t.Errorf("main wt = %+v", wts[0])
	}
	if wts[1].Branch != "refs/heads/feat" || wts[1].Head == "" {
		t.Errorf("linked wt = %+v", wts[1])
	}
}

func TestParseWorktreesFlags(t *testing.T) {
	out := "worktree /a\nHEAD abc\nbare\n\nworktree /b\nHEAD def\ndetached\nlocked reason\nprunable gone\n"
	w := gitexec.ParseWorktrees(out)
	if len(w) != 2 || !w[0].Bare || !w[1].Detached || !w[1].Locked || !w[1].Prunable || w[1].Head != "def" {
		t.Fatalf("parsed = %+v", w)
	}
}

func TestHelperErrors(t *testing.T) {
	fake := cmdruntest.New(t)
	fake.On(cmdruntest.MatchName("git")).Return(cmdruntest.Fail(128, "fatal"))
	g := &gitexec.Git{Runner: fake.Runner(), Env: seams.FilteredEnv(nil)}
	if _, err := g.CommonDir(t.Context(), "/d"); err == nil || !strings.Contains(err.Error(), "fatal") {
		t.Fatalf("err = %v", err)
	}
	fake2 := cmdruntest.New(t)
	fake2.On(cmdruntest.Any).Return(cmdrun.Result{Code: -1, Signal: "killed", CtxErr: context.DeadlineExceeded})
	g2 := &gitexec.Git{Runner: fake2.Runner(), Env: seams.FilteredEnv(nil)}
	if _, err := g2.Worktrees(t.Context(), "/d"); !errors.Is(err, gitexec.ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}
