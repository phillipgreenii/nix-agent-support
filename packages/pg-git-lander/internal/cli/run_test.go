package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/cli"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
)

const thisHost = "laptop.local"

type fx struct {
	t    *testing.T
	repo *gitfixture.Repo
	g    *gitexec.Git
	xdg  string
	main string
	wt   string
	env  map[string]string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	var genv []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(k, "GIT_AUTHOR_") && !strings.HasPrefix(k, "GIT_COMMITTER_") && k != "EMAIL" {
			genv = append(genv, kv)
		}
	}
	genv = append(genv, "HOME="+filepath.Join(root, "home"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"))
	f := &fx{
		t: t, repo: repo, xdg: t.TempDir(), env: map[string]string{},
		g: &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(genv)},
	}
	f.main = f.git(repo.Dir, "symbolic-ref", "--short", "HEAD")
	if err := os.WriteFile(filepath.Join(f.xdg, "config.toml"), []byte("[defaults.lock]\nwait = \"3s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fx) git(dir string, args ...string) string {
	f.t.Helper()
	r := f.g.Run(f.t.Context(), dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v in %s: %s", args, dir, r.Stderr)
	}
	return strings.TrimSpace(string(r.Stdout))
}

func (f *fx) commit(dir, file, content string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(dir, "add", "-A")
	f.git(dir, "commit", "-q", "-m", "add "+file)
	return f.git(dir, "rev-parse", "HEAD")
}

func (f *fx) feature() {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "wt-x")
	f.git(f.repo.Dir, "worktree", "add", "-q", "-b", "feature/x", p)
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		f.t.Fatal(err)
	}
	f.wt = r
	f.commit(f.wt, "x.txt", "x\n")
}

type result struct {
	code        int
	stdout, err string
}

func (f *fx) cli(cwd string, args ...string) result {
	f.t.Helper()
	var out, errb bytes.Buffer
	env := &cli.Env{
		Stdout: &out, Stderr: &errb, Cwd: cwd, Version: "test",
		Getenv: func(k string) string { return f.env[k] },
		Deps: pipeline.Deps{
			Git: f.g, Clock: seamstest.NewClock(time.Now()), Liveness: seamstest.NewLiveness(),
			Host: seamstest.Host(thisHost), Paths: seamstest.ConfigPaths(f.xdg),
			Chdir: func(string) error { return nil },
		},
		Interrupter: pipeline.Interrupter{Signals: make(chan os.Signal, 2), Exit: func(int) {}},
	}
	code := cli.Run(args, env)
	return result{code, out.String(), errb.String()}
}

func (f *fx) jsonOutcome(r result) map[string]any {
	f.t.Helper()
	if strings.Count(strings.TrimRight(r.stdout, "\n"), "\n") != 0 {
		f.t.Fatalf("want exactly one JSON line, got %q", r.stdout)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		f.t.Fatalf("%q: %v", r.stdout, err)
	}
	if int(m["exit"].(float64)) != r.code {
		f.t.Fatalf("exit field %v, process exit %d", m["exit"], r.code)
	}
	return m
}

func TestLandPrintsOneHumanLineAndExitsZero(t *testing.T) {
	f := newFx(t)
	f.feature()
	r := f.cli(f.repo.Dir, "land", "feature/x", "--into", f.main)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "landed feature/x into "+f.main) || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("%+v", r)
	}
	if f.git(f.repo.Dir, "ls-tree", "--name-only", f.main, "x.txt") != "x.txt" {
		t.Fatal("x.txt was not landed")
	}
}

func TestLandDefaultsTargetToThePrimaryBranchAndPrintsJSON(t *testing.T) {
	f := newFx(t)
	f.feature()
	r := f.cli(f.repo.Dir, "land", "feature/x", "--json")
	m := f.jsonOutcome(r)
	if r.code != 0 || m["outcome"] != "landed" || m["target"] != f.main {
		t.Fatalf("%+v %v", r, m)
	}
}

func TestLandOfThePrimaryBranchWithoutIntoIsAUsageError(t *testing.T) {
	f := newFx(t)
	r := f.cli(f.repo.Dir, "land", f.main, "--json")
	if r.code != 2 || f.jsonOutcome(r)["outcome"] != "usage-error" {
		t.Fatalf("%+v", r)
	}
}

func TestRebaseRunsAndPrintsRebased(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.commit(f.repo.Dir, "m.txt", "m\n")
	r := f.cli(f.repo.Dir, "rebase", "feature/x", "--onto", f.main)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "rebased feature/x onto") {
		t.Fatalf("%+v", r)
	}
	if f.git(f.repo.Dir, "merge-base", "--is-ancestor", f.main, "feature/x") != "" {
		t.Fatal("unexpected output")
	}
}

func TestUsageErrorsExitTwoWithOneOutcome(t *testing.T) {
	f := newFx(t)
	r := f.cli(f.repo.Dir, "land", "a", "--into", "a")
	if r.code != 2 || !strings.HasPrefix(r.stdout, "usage-error") || strings.Count(r.stdout, "\n") != 1 || !strings.Contains(r.err, "usage:") {
		t.Fatalf("%+v", r)
	}
	r = f.cli(f.repo.Dir, "rebase", "b", "--onto", "main", "--keep-source-branch", "--json")
	if r.code != 2 || f.jsonOutcome(r)["outcome"] != "usage-error" || r.err != "" {
		t.Fatalf("%+v", r)
	}
}

func TestNotARepositoryIsAUsageError(t *testing.T) {
	f := newFx(t)
	r := f.cli(t.TempDir(), "land", "b", "--json")
	if r.code != 2 || f.jsonOutcome(r)["outcome"] != "usage-error" {
		t.Fatalf("%+v", r)
	}
}

func TestConfigCheck(t *testing.T) {
	f := newFx(t)
	r := f.cli(f.repo.Dir, "config", "check", "--json")
	if r.code != 0 || f.jsonOutcome(r)["outcome"] != "config-ok" {
		t.Fatalf("%+v", r)
	}
	bad := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(bad, []byte("not valid [[[ toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = f.cli(f.repo.Dir, "config", "check", "--file", bad, "--json")
	if r.code != 2 || f.jsonOutcome(r)["outcome"] != "config-error" {
		t.Fatalf("%+v", r)
	}
}

func TestStatusAndLogReportInternalErrorUntilTheirHandlersExist(t *testing.T) {
	f := newFx(t)
	if r := f.cli(f.repo.Dir, "status", "--json"); r.code != 1 || f.jsonOutcome(r)["outcome"] != "internal-error" {
		t.Fatalf("%+v", r)
	}
}

// conflict stops a land of feature/x on x.txt and returns the work worktree.
func (f *fx) conflict(repoFlag ...string) (work string, stop map[string]any) {
	f.t.Helper()
	f.feature()
	f.commit(f.repo.Dir, "x.txt", "main\n")
	args := append([]string{"land", "feature/x", "--json"}, repoFlag...)
	r := f.cli(f.repo.Dir, args...)
	stop = f.jsonOutcome(r)
	if r.code != 4 || stop["outcome"] != "stopped" {
		f.t.Fatalf("%+v", r)
	}
	ww := stop["work_worktree"].(map[string]any)
	return ww["path"].(string), stop
}

func TestStopRepeatsRepoInEveryNextCommand(t *testing.T) {
	f := newFx(t)
	_, stop := f.conflict("--repo", f.repo.Dir)
	next := stop["next"].([]any)
	if len(next) == 0 {
		t.Fatal("no next commands")
	}
	for _, n := range next {
		if !strings.Contains(n.(string), "--repo "+f.repo.Dir) {
			t.Errorf("next command %q lacks --repo", n)
		}
	}
}

func TestStopWithoutRepoPrintsNoRepoInNextCommands(t *testing.T) {
	f := newFx(t)
	_, stop := f.conflict()
	for _, n := range stop["next"].([]any) {
		if strings.Contains(n.(string), "--repo") {
			t.Errorf("next command %q has --repo", n)
		}
	}
}

func TestHumanStopPrintsNextCommands(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.commit(f.repo.Dir, "x.txt", "main\n")
	r := f.cli(f.repo.Dir, "land", "feature/x", "--repo", f.repo.Dir)
	if r.code != 4 || !strings.HasPrefix(r.stdout, "stopped:") || !strings.Contains(r.stdout, "pg-git-lander land feature/x --abort --repo "+f.repo.Dir) {
		t.Fatalf("%+v", r)
	}
}

func TestResumeWithBranchOmittedFromTheWorkWorktree(t *testing.T) {
	f := newFx(t)
	work, _ := f.conflict()
	// PGL_* variables must not be read on a resume: a bogus value would
	// otherwise be refused.
	f.env[cli.EnvWorkWorktree] = "/does/not/exist"
	f.env[cli.EnvSourceWorktree] = "/does/not/exist"
	r := f.cli(work, "land", "--abort", "--json")
	m := f.jsonOutcome(r)
	if r.code != 0 || m["outcome"] != "aborted" || m["branch"] != "feature/x" {
		t.Fatalf("%+v %v", r, m)
	}
}

func TestResumeWithBranchOmittedOutsideTheWorkWorktreeIsNothingStopped(t *testing.T) {
	f := newFx(t)
	f.conflict()
	r := f.cli(f.repo.Dir, "land", "--abort", "--json")
	m := f.jsonOutcome(r)
	if r.code != 5 || m["reason"] != string(outcome.RefNothingStopped) {
		t.Fatalf("%+v %v", r, m)
	}
}

func TestResumeOperationMismatch(t *testing.T) {
	f := newFx(t)
	work, _ := f.conflict()
	r := f.cli(work, "rebase", "--abort", "--json")
	m := f.jsonOutcome(r)
	if r.code != 5 || m["reason"] != string(outcome.RefOperationMismatch) {
		t.Fatalf("%+v %v", r, m)
	}
}

func TestResumeWithBranchGiven(t *testing.T) {
	f := newFx(t)
	f.conflict()
	r := f.cli(f.repo.Dir, "land", "feature/x", "--quit", "--json")
	if r.code != 0 || f.jsonOutcome(r)["outcome"] != "quit" {
		t.Fatalf("%+v", r)
	}
}

func TestPGLVariablesAreUsedOnAFreshOperation(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.env[cli.EnvWorkWorktree] = "/does/not/exist"
	r := f.cli(f.repo.Dir, "land", "feature/x", "--json")
	m := f.jsonOutcome(r)
	if r.code == 0 || m["outcome"] == "landed" {
		t.Fatalf("PGL_WORK_WORKTREE was not read: %+v", r)
	}
	// The same call with an explicit flag overrides the variable.
	r = f.cli(f.repo.Dir, "land", "feature/x", "--work-worktree", f.wt, "--json")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestLogPruningRunsAtTheStartOfEveryInvocation(t *testing.T) {
	f := newFx(t)
	common := f.git(f.repo.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	old := oplog.LogPath(common, "20200101T000000Z-aaaaaaaa")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(old, long, long); err != nil {
		t.Fatal(err)
	}
	f.cli(f.repo.Dir, "config", "check")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old log survived: %v", err)
	}
}

func TestVersionAndHelp(t *testing.T) {
	f := newFx(t)
	if r := f.cli(f.repo.Dir, "--version"); r.code != 0 || r.stdout != "pg-git-lander test\n" {
		t.Fatalf("%+v", r)
	}
	if r := f.cli(f.repo.Dir, "--help"); r.code != 0 || !strings.Contains(r.stdout, "pg-git-lander land") {
		t.Fatalf("%+v", r)
	}
}
