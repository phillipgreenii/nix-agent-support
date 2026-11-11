package conflict_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"
	"github.com/phillipgreenii/x/gitfixture"
	"github.com/phillipgreenii/x/gittest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/conflict"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/rebase"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

type fx struct {
	t       *testing.T
	repo    *gitfixture.Repo
	g       *gitexec.Git
	rb      *rebase.Rebaser
	scripts string
	work    string
	home    string
	cfg     *config.Config
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	env := append(
		os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"),
	)
	g := &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)}
	f := &fx{t: t, repo: repo, g: g, rb: &rebase.Rebaser{Git: g}, scripts: t.TempDir(), home: filepath.Join(root, "home")}
	f.cfg = &config.Config{MaxConflictRounds: 10}
	wt, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.work = filepath.Join(wt, "work")
	return f
}

func (f *fx) git(dir string, args ...string) string {
	f.t.Helper()
	r := f.g.Run(f.t.Context(), dir, args...)
	if !r.OK() {
		f.t.Fatalf("git %v: %s", args, r.Stderr)
	}
	return strings.TrimSpace(string(r.Stdout))
}

func (f *fx) write(dir, rel, body string) {
	f.t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) read(dir, rel string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// commitMain commits files on main in the primary worktree.
func (f *fx) commitMain(msg string, files map[string]string) {
	f.t.Helper()
	if _, err := f.repo.Commit(f.t.Context(), msg, files); err != nil {
		f.t.Fatal(err)
	}
}

// branchOff creates the work worktree and branch at main's tip.
func (f *fx) branchOff() {
	f.t.Helper()
	f.git(f.repo.Dir, "worktree", "add", "-b", "work", f.work)
}

// commitWork commits files on the work branch.
func (f *fx) commitWork(msg string, files map[string]string) {
	f.t.Helper()
	for p, b := range files {
		f.write(f.work, p, b)
	}
	f.git(f.work, "add", "-A")
	f.git(f.work, "commit", "-m", msg)
}

// conflicting builds base file(s), then diverging main and work commits that
// conflict on every given path, and starts the rebase.
func (f *fx) conflicting(paths ...string) *rebase.Result {
	f.t.Helper()
	base := map[string]string{}
	for _, p := range paths {
		base[p] = "base\n"
	}
	f.commitMain("base", base)
	f.branchOff()
	work := map[string]string{}
	main := map[string]string{}
	for _, p := range paths {
		work[p] = "branch\n"
		main[p] = "onto\n"
	}
	f.commitWork("work commit", work)
	f.commitMain("main change", main)
	return f.startRebase()
}

func (f *fx) startRebase() *rebase.Result {
	f.t.Helper()
	res, err := f.rb.Rebase(f.t.Context(), f.work, "main")
	if err != nil {
		f.t.Fatal(err)
	}
	if len(res.Conflict) == 0 {
		f.t.Fatalf("expected a conflict stop: %+v", res)
	}
	return res
}

func (f *fx) script(name, body string) string {
	f.t.Helper()
	p := filepath.Join(f.scripts, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) resolver(runHooks bool) *conflict.Resolver {
	return &conflict.Resolver{
		Git:    f.g,
		Hooks:  &hooks.Runner{Git: f.g, AutofixAttempts: 1, RunGitHooks: runHooks},
		Rebase: f.rb,
		Config: f.cfg,
		Facts: hooks.Facts{
			Operation: "rebase", OpID: "op1", Repo: f.repo.Dir, WorkWorktree: f.work,
			SourceWorktree: f.work, WorkBranch: "work", Branch: "feature", Onto: "main",
		},
	}
}

func (f *fx) resolve(r *conflict.Resolver, rounds int, auto []string, diag *rebase.Diagnostics) *conflict.Result {
	f.t.Helper()
	res, err := r.Resolve(f.t.Context(), conflict.Input{Dir: f.work, Rounds: rounds, AutoMerged: auto, Diag: diag})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func rule(id, glob, runPer string, resolve ...string) config.Rule {
	return config.Rule{ID: id, Glob: glob, RunPer: runPer, Resolve: resolve, Timeout: time.Minute}
}

func TestNoMatchingRuleRunsNothingAndLeavesGitsConflict(t *testing.T) {
	f := newFx(t)
	marker := filepath.Join(f.scripts, "ran")
	s := f.script("r.sh", "touch "+marker)
	f.cfg.Rules = []config.Rule{rule("r1", "*.lock", "file", s)}
	f.conflicting("f.txt", "a.lock")

	res := f.resolve(f.resolver(true), 0, nil, nil)
	if res.Stop == nil || res.Stop.Reason != outcome.StopConflict {
		t.Fatalf("stop = %+v", res.Stop)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a resolver ran although a path had no command")
	}
	if got := f.read(f.work, "a.lock"); !strings.Contains(got, "<<<<<<<") {
		t.Fatalf("a.lock was touched: %q", got)
	}
	if got := strings.Join(res.Stop.Paths, ","); got != "a.lock,f.txt" {
		t.Fatalf("paths = %s", got)
	}
	if res.Rounds != 1 {
		t.Fatalf("rounds = %d", res.Rounds)
	}
}

func TestFileModeRunsOncePerPathInRootWithContractVariables(t *testing.T) {
	f := newFx(t)
	log := filepath.Join(f.scripts, "log")
	s := f.script("r.sh", `{
echo "cwd=$(pwd -P) path=$PG_GIT_LANDER_PATH dir=${PG_GIT_LANDER_DIR-unset} rule=$PG_GIT_LANDER_RULE commit=$PG_GIT_LANDER_REPLAYED_COMMIT op=$PG_GIT_LANDER_OPERATION"
echo "base=$(cat "$PG_GIT_LANDER_BASE_FILE") onto=$(cat "$PG_GIT_LANDER_ONTO_FILE") branch=$(cat "$PG_GIT_LANDER_BRANCH_FILE")"
} >> `+log+`
echo merged > "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("xdg-repo:1:d/*.txt", "d/*.txt", "file", s)}
	f.conflicting("d/b.txt", "d/a.txt")
	commit := f.git(f.work, "rev-parse", "REBASE_HEAD")

	res := f.resolve(f.resolver(true), 0, nil, nil)
	if !res.Finished || res.Stop != nil {
		t.Fatalf("res = %+v", res)
	}
	want := "cwd=" + f.work + " path=d/a.txt dir=unset rule=xdg-repo:1:d/*.txt commit=" + commit + " op=rebase\n" +
		"base=base onto=onto branch=branch\n" +
		"cwd=" + f.work + " path=d/b.txt dir=unset rule=xdg-repo:1:d/*.txt commit=" + commit + " op=rebase\n" +
		"base=base onto=onto branch=branch\n"
	if got := f.read("/", log); got != want {
		t.Fatalf("log:\n%s\nwant:\n%s", got, want)
	}
	if got := f.read(f.work, "d/a.txt"); got != "merged\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if len(res.Resolved) != 2 || res.Resolved[0].Path != "d/a.txt" || res.Resolved[0].Rule != "xdg-repo:1:d/*.txt" || res.Resolved[0].Commit != commit {
		t.Fatalf("resolved = %+v", res.Resolved)
	}
	if res.Rounds != 1 {
		t.Fatalf("rounds = %d", res.Rounds)
	}
	if st := f.git(f.work, "status", "--porcelain"); st != "" {
		t.Fatalf("dirty: %s", st)
	}
}

func TestDirModeRunsOncePerDirectoryInThatDirectory(t *testing.T) {
	f := newFx(t)
	log := filepath.Join(f.scripts, "log")
	s := f.script("r.sh", `echo "cwd=$(pwd -P) dir=$PG_GIT_LANDER_DIR path=${PG_GIT_LANDER_PATH-unset}" >> `+log+`
for f in *.txt; do echo merged > "$f"; done`)
	f.cfg.Rules = []config.Rule{rule("r", "**/*.txt", "dir", s)}
	f.conflicting("b/y.txt", "a/x.txt", "a/w.txt")

	res := f.resolve(f.resolver(true), 0, nil, nil)
	if !res.Finished {
		t.Fatalf("res = %+v", res)
	}
	want := "cwd=" + f.work + "/a dir=a path=unset\ncwd=" + f.work + "/b dir=b path=unset\n"
	if got := f.read("/", log); got != want {
		t.Fatalf("log:\n%s\nwant:\n%s", got, want)
	}
	if len(res.Resolved) != 3 {
		t.Fatalf("resolved = %+v", res.Resolved)
	}
}

func TestFirstMatchingRuleWinsAndConflictDefaultCatchesTheRest(t *testing.T) {
	f := newFx(t)
	first := f.script("first.sh", `echo first > "$PG_GIT_LANDER_PATH"`)
	second := f.script("second.sh", `echo second > "$PG_GIT_LANDER_PATH"`)
	dflt := f.script("default.sh", `echo default > "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("l1", "*.lock", "file", first), rule("l2", "*", "file", second)}
	d := rule("conflict_default", "", "file", dflt)
	f.cfg.ConflictDefault = &d
	f.conflicting("a.lock", "b.txt")
	// A path in a subdirectory matches neither "*" rule: the default takes it.
	res := f.resolve(f.resolver(true), 0, nil, nil)
	if !res.Finished {
		t.Fatalf("res = %+v", res)
	}
	if f.read(f.work, "a.lock") != "first\n" || f.read(f.work, "b.txt") != "second\n" {
		t.Fatal("wrong rule won")
	}
}

func TestResolverExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		reason outcome.StopReason
		detail string
	}{
		{"failed", "echo boom >&2; exit 3", outcome.StopResolveFailed, conflict.DetailResolverFailed},
		{"transient", "echo later >&2; exit 75", outcome.StopTransient, ""},
		{"not found", "", outcome.StopResolveFailed, conflict.DetailResolverFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			s := f.script("r.sh", c.body)
			if c.name == "not found" {
				s = filepath.Join(f.scripts, "missing")
			}
			f.cfg.Rules = []config.Rule{rule("r", "*", "file", s)}
			f.conflicting("f.txt")
			res := f.resolve(f.resolver(true), 0, nil, nil)
			if res.Stop == nil || res.Stop.Reason != c.reason || res.Stop.Detail != c.detail {
				t.Fatalf("stop = %+v", res.Stop)
			}
			if c.body != "" && !strings.Contains(res.Stop.LogTail, map[string]string{"failed": "boom", "transient": "later"}[c.name]) {
				t.Fatalf("log tail = %q", res.Stop.LogTail)
			}
			if res.Stop.Rule != "r" || len(res.Stop.Paths) != 1 || res.Stop.Paths[0] != "f.txt" {
				t.Fatalf("stop = %+v", res.Stop)
			}
			if in, _ := f.rb.InProgress(t.Context(), f.work); !in {
				t.Fatal("rebase should still be in progress")
			}
		})
	}
}

func TestMarkersLeftStopsAndMarkdownUnderlineIsNotAMarker(t *testing.T) {
	f := newFx(t)
	bad := f.script("bad.sh", `printf 'a\n<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> b\n' > "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("r", "*", "file", bad)}
	f.conflicting("f.txt")
	res := f.resolve(f.resolver(true), 0, nil, nil)
	if res.Stop == nil || res.Stop.Reason != outcome.StopResolveFailed || res.Stop.Detail != conflict.DetailMarkersLeft {
		t.Fatalf("stop = %+v", res.Stop)
	}

	g := newFx(t)
	ok := g.script("ok.sh", `printf 'Title\n=======\ntext\n' > "$PG_GIT_LANDER_PATH"`)
	g.cfg.Rules = []config.Rule{rule("r", "*", "file", ok)}
	g.conflicting("f.md")
	res = g.resolve(g.resolver(true), 0, nil, nil)
	if !res.Finished {
		t.Fatalf("underline treated as a marker: %+v", res.Stop)
	}
	if got := g.read(g.work, "f.md"); got != "Title\n=======\ntext\n" {
		t.Fatalf("f.md = %q", got)
	}
}

func TestVerifyFailureAndSuccess(t *testing.T) {
	f := newFx(t)
	log := filepath.Join(f.scripts, "verify.log")
	res1 := f.script("r.sh", `echo merged > "$PG_GIT_LANDER_PATH"`)
	ver := f.script("v.sh", `echo "cwd=$(pwd -P) path=$PG_GIT_LANDER_PATH staged=$(git diff --cached --name-only)" >> `+log+`; echo verify-said-no >&2; exit 1`)
	f.cfg.Rules = []config.Rule{{ID: "r", Glob: "*", RunPer: "file", Resolve: []string{res1}, Verify: []string{ver}, Timeout: time.Minute}}
	f.conflicting("f.txt")
	res := f.resolve(f.resolver(true), 0, nil, nil)
	if res.Stop == nil || res.Stop.Reason != outcome.StopResolveFailed || res.Stop.Detail != conflict.DetailVerifyFailed {
		t.Fatalf("stop = %+v", res.Stop)
	}
	if !strings.Contains(res.Stop.LogTail, "verify-said-no") {
		t.Fatalf("log tail = %q", res.Stop.LogTail)
	}
	if got := f.read("/", log); got != "cwd="+f.work+" path=f.txt staged=f.txt\n" {
		t.Fatalf("verify saw %q (it must run after staging, in the same directory)", got)
	}

	// Verify exit 75 is still verify-failed, not transient.
	g := newFx(t)
	r2 := g.script("r.sh", `echo merged > "$PG_GIT_LANDER_PATH"`)
	v2 := g.script("v.sh", `exit 75`)
	g.cfg.Rules = []config.Rule{{ID: "r", Glob: "*", RunPer: "file", Resolve: []string{r2}, Verify: []string{v2}, Timeout: time.Minute}}
	g.conflicting("f.txt")
	res = g.resolve(g.resolver(true), 0, nil, nil)
	if res.Stop == nil || res.Stop.Detail != conflict.DetailVerifyFailed {
		t.Fatalf("stop = %+v", res.Stop)
	}

	// A passing verify lets the loop finish.
	h := newFx(t)
	r3 := h.script("r.sh", `echo merged > "$PG_GIT_LANDER_PATH"`)
	v3 := h.script("v.sh", `exit 0`)
	h.cfg.Rules = []config.Rule{{ID: "r", Glob: "*", RunPer: "file", Resolve: []string{r3}, Verify: []string{v3}, Timeout: time.Minute}}
	h.conflicting("f.txt")
	if res = h.resolve(h.resolver(true), 0, nil, nil); !res.Finished {
		t.Fatalf("res = %+v", res.Stop)
	}
}

func TestTimeoutKillsTheProcessGroup(t *testing.T) {
	f := newFx(t)
	pidf := filepath.Join(f.scripts, "pid")
	s := f.script("r.sh", `sleep 30 &
echo $! > `+pidf+`.tmp
mv `+pidf+`.tmp `+pidf+`
wait`)
	f.cfg.Rules = []config.Rule{{ID: "r", Glob: "*", RunPer: "file", Resolve: []string{s}, Timeout: 5 * time.Second}}
	f.conflicting("f.txt")
	start := time.Now()
	res := f.resolve(f.resolver(true), 0, nil, nil)
	if res.Stop == nil || res.Stop.Reason != outcome.StopTimeout {
		t.Fatalf("stop = %+v", res.Stop)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("did not return promptly")
	}
	// The script publishes the pid atomically; wait (bounded) for it.
	var pid string
	for pd := time.Now().Add(30 * time.Second); pid == "" && time.Now().Before(pd); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(pidf); err == nil {
			pid = strings.TrimSpace(string(b))
		}
	}
	if pid == "" {
		t.Fatal("the resolver never published its child pid")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c := cmdrun.Exec(t.Context(), cmdrun.Call{Name: "kill", Args: []string{"-0", pid}})
		if !c.OK() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %s of the resolver survived its timeout", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (f *fx) plantPreCommit(body string) {
	f.t.Helper()
	p := filepath.Join(f.repo.Root(), "hooks", "pre-commit")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func TestResolutionCommitKeepsMessageAuthorAndRunsGitHooks(t *testing.T) {
	for _, runHooks := range []bool{true, false} {
		f := newFx(t)
		hookLog := filepath.Join(f.scripts, "hook.log")
		s := f.script("r.sh", `echo merged > "$PG_GIT_LANDER_PATH"`)
		f.cfg.Rules = []config.Rule{rule("r", "*", "file", s)}
		f.conflicting("f.txt")
		f.plantPreCommit(`echo ran >> ` + hookLog)
		before := f.git(f.work, "rev-parse", "REBASE_HEAD")
		author := f.git(f.work, "log", "-1", "--format=%an <%ae>", before)

		res := f.resolve(f.resolver(runHooks), 0, nil, nil)
		if !res.Finished {
			t.Fatalf("runHooks=%v: %+v", runHooks, res.Stop)
		}
		_, err := os.Stat(hookLog)
		if runHooks != (err == nil) {
			t.Fatalf("runHooks=%v but hook ran=%v", runHooks, err == nil)
		}
		if got := f.git(f.work, "log", "-1", "--format=%s"); got != "work commit" {
			t.Fatalf("subject = %q", got)
		}
		if got := f.git(f.work, "log", "-1", "--format=%an <%ae>"); got != author {
			t.Fatalf("author = %q want %q", got, author)
		}
		if got := f.git(f.work, "rev-parse", "HEAD~1"); got != f.git(f.repo.Dir, "rev-parse", "main") {
			t.Fatal("work commit is not on top of main")
		}
	}
}

func TestRejectedResolutionCommitStopsCommitRejected(t *testing.T) {
	f := newFx(t)
	s := f.script("r.sh", `echo merged > "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("r", "*", "file", s)}
	f.conflicting("f.txt")
	f.plantPreCommit(`echo "no commits today" >&2; exit 1`)
	res := f.resolve(f.resolver(true), 0, nil, nil)
	if res.Stop == nil || res.Stop.Reason != outcome.StopCommitRejected || !strings.Contains(res.Stop.LogTail, "no commits today") {
		t.Fatalf("stop = %+v", res.Stop)
	}
}

func TestEmptyResolutionIsSkippedAndListedAsDropped(t *testing.T) {
	f := newFx(t)
	s := f.script("keep-onto.sh", `cp "$PG_GIT_LANDER_ONTO_FILE" "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("r", "*", "file", s)}
	f.conflicting("f.txt")
	dropped := f.git(f.work, "rev-parse", "REBASE_HEAD")
	res := f.resolve(f.resolver(true), 0, nil, nil)
	if !res.Finished {
		t.Fatalf("stop = %+v", res.Stop)
	}
	if len(res.Dropped) != 1 || res.Dropped[0] != dropped {
		t.Fatalf("dropped = %v want [%s]", res.Dropped, dropped)
	}
	if f.git(f.work, "rev-parse", "HEAD") != f.git(f.repo.Dir, "rev-parse", "main") {
		t.Fatal("the empty commit was kept")
	}
	if in, _ := f.rb.InProgress(t.Context(), f.work); in {
		t.Fatal("rebase still in progress")
	}
}

// twoRounds makes two work commits that each conflict with main.
func (f *fx) twoRounds() {
	f.t.Helper()
	f.commitMain("base", map[string]string{"f.txt": "base\n"})
	f.branchOff()
	f.commitWork("w1", map[string]string{"f.txt": "w1\n"})
	f.commitWork("w2", map[string]string{"f.txt": "w2\n"})
	f.commitMain("main change", map[string]string{"f.txt": "onto\n"})
	f.startRebase()
}

func TestRoundsAreCountedAndKeptAcrossCalls(t *testing.T) {
	f := newFx(t)
	s := f.script("r.sh", `echo merged-$PG_GIT_LANDER_REPLAYED_COMMIT > "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("r", "*", "file", s)}
	f.twoRounds()
	res := f.resolve(f.resolver(true), 3, nil, nil)
	if !res.Finished || res.Rounds != 5 || len(res.Resolved) != 2 {
		t.Fatalf("res = %+v rounds=%d", res.Stop, res.Rounds)
	}
	if got := f.git(f.work, "log", "--format=%s", "main..work"); got != "w2\nw1" {
		t.Fatalf("log = %q", got)
	}
}

func TestMaxConflictRoundsStopsAfterTheBound(t *testing.T) {
	f := newFx(t)
	s := f.script("r.sh", `echo merged-$PG_GIT_LANDER_REPLAYED_COMMIT > "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("r", "*", "file", s)}
	f.cfg.MaxConflictRounds = 1
	f.twoRounds()
	res := f.resolve(f.resolver(true), 0, nil, nil)
	if res.Stop == nil || res.Stop.Reason != outcome.StopMaxConflictRounds {
		t.Fatalf("stop = %+v", res.Stop)
	}
	if res.Rounds != 2 || len(res.Resolved) != 1 {
		t.Fatalf("rounds = %d resolved = %+v", res.Rounds, res.Resolved)
	}
	if in, _ := f.rb.InProgress(t.Context(), f.work); !in {
		t.Fatal("rebase should be left in progress")
	}
	// A count already at the bound stops at once, before any resolver runs.
	g := newFx(t)
	marker := filepath.Join(g.scripts, "ran")
	g.cfg.Rules = []config.Rule{rule("r", "*", "file", g.script("r.sh", "touch "+marker))}
	g.cfg.MaxConflictRounds = 2
	g.twoRounds()
	res = g.resolve(g.resolver(true), 2, nil, nil)
	if res.Stop == nil || res.Stop.Reason != outcome.StopMaxConflictRounds {
		t.Fatalf("stop = %+v", res.Stop)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("resolver ran past the bound")
	}
}

func TestRealMergeDriverRunsInsideTheRebaseAndIsNamedInResolved(t *testing.T) {
	f := newFx(t)
	driverLog := filepath.Join(f.scripts, "driver.log")
	drv := f.script("driver.sh", `echo "home=$HOME" >> `+driverLog+`
cp "$2" "$1.tmp" 2>/dev/null; cat "$3" >> "$2"; exit 0`)
	f.write(f.repo.Dir, ".gitattributes", "*.dat merge=cat\n")
	f.git(f.repo.Dir, "config", "merge.cat.driver", drv+" %O %A %B")
	f.commitMain("base", map[string]string{"x.dat": "base\n", "f.txt": "base\n", ".gitattributes": "*.dat merge=cat\n"})
	f.branchOff()
	f.commitWork("work commit", map[string]string{"x.dat": "branch\n", "f.txt": "branch\n"})
	f.commitMain("main change", map[string]string{"x.dat": "onto\n", "f.txt": "onto\n"})
	rb := f.startRebase()
	if len(rb.Conflict) != 1 || rb.Conflict[0] != "f.txt" {
		t.Fatalf("conflict = %v (the driver should have merged x.dat)", rb.Conflict)
	}
	if got := f.read("/", driverLog); got != "home="+f.home+"\n" {
		t.Fatalf("driver env: %q", got)
	}
	if !contains(rb.AutoMerged, "x.dat") {
		t.Fatalf("auto merged = %v", rb.AutoMerged)
	}

	diag, err := f.rb.Settings(t.Context(), f.work)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rb.CheckAttrs(t.Context(), f.work, diag, rb.AutoMerged); err != nil {
		t.Fatal(err)
	}
	s := f.script("r.sh", `echo merged > "$PG_GIT_LANDER_PATH"`)
	f.cfg.Rules = []config.Rule{rule("r", "f.txt", "file", s)}
	res := f.resolve(f.resolver(true), 0, rb.AutoMerged, diag)
	if !res.Finished {
		t.Fatalf("stop = %+v", res.Stop)
	}
	var drivers, rules int
	for _, e := range res.Resolved {
		switch {
		case e.Driver == "cat" && e.Path == "x.dat" && e.Rule == "":
			drivers++
		case e.Rule == "r" && e.Path == "f.txt" && e.Driver == "":
			rules++
		}
	}
	if drivers != 1 || rules != 1 {
		t.Fatalf("resolved = %+v", res.Resolved)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
