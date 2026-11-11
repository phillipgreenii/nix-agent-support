package hooks_test

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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/gitexec"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/hooks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
)

type fx struct {
	t       *testing.T
	repo    *gitfixture.Repo
	g       *gitexec.Git
	r       *hooks.Runner
	scripts string
	work    string // the work worktree (a linked worktree of repo)
}

func newFx(t *testing.T, attempts int) *fx {
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
	f := &fx{t: t, repo: repo, g: g, scripts: t.TempDir()}
	f.r = &hooks.Runner{Git: g, AutofixAttempts: attempts, RunGitHooks: true}
	if _, err := repo.Commit(t.Context(), "add f", map[string]string{"f.txt": "one\n"}); err != nil {
		t.Fatal(err)
	}
	wt, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.work = filepath.Join(wt, "work")
	f.git(repo.Dir, "worktree", "add", "-b", "work", f.work)
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

// script writes an executable shell script and returns its path.
func (f *fx) script(name, body string) string {
	f.t.Helper()
	p := filepath.Join(f.scripts, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) facts() hooks.Facts {
	repoDir, err := filepath.EvalSymlinks(f.repo.Dir)
	if err != nil {
		f.t.Fatal(err)
	}
	return hooks.Facts{
		Operation: "land", OpID: "op1", Repo: repoDir, GitCommonDir: filepath.Join(repoDir, ".git"),
		WorkWorktree: f.work, SourceWorktree: f.work, DestinationWorktree: repoDir,
		WorkBranch: "work", Branch: "feature", BranchTip: "aaa", Target: "main", TargetTip: "bbb",
		Onto: "bbb", Result: "ccc", BaseMoved: true,
	}
}

func (f *fx) run(point string, hs ...config.Hook) hooks.PointResult {
	f.t.Helper()
	res, err := f.r.RunPoint(f.t.Context(), hooks.PointInput{Point: point, Hooks: hs, Facts: f.facts()})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func hk(argv ...string) config.Hook {
	return config.Hook{Run: argv, Timeout: time.Minute, Cwd: "worktree"}
}

func TestEnvironmentGolden(t *testing.T) {
	f := newFx(t, 2)
	facts := f.facts()
	facts.Context = []hooks.ContextPair{{Key: "ticket-id", Value: "X-1"}, {Key: "a.b c", Value: "v"}}
	got := facts.Vars(hooks.PointBeforeMove)
	want := map[string]string{
		"PG_GIT_LANDER_OPERATION":            "land",
		"PG_GIT_LANDER_OP_ID":                "op1",
		"PG_GIT_LANDER_HOOK":                 "before_move",
		"PG_GIT_LANDER_REPO":                 facts.Repo,
		"PG_GIT_LANDER_GIT_COMMON_DIR":       facts.GitCommonDir,
		"PG_GIT_LANDER_WORK_WORKTREE":        f.work,
		"PG_GIT_LANDER_SOURCE_WORKTREE":      f.work,
		"PG_GIT_LANDER_DESTINATION_WORKTREE": facts.Repo,
		"PG_GIT_LANDER_WORK_BRANCH":          "work",
		"PG_GIT_LANDER_BRANCH":               "feature",
		"PG_GIT_LANDER_BRANCH_START":         "aaa",
		"PG_GIT_LANDER_TARGET":               "main",
		"PG_GIT_LANDER_TARGET_TIP":           "bbb",
		"PG_GIT_LANDER_ONTO":                 "bbb",
		"PG_GIT_LANDER_RESULT":               "ccc",
		"PG_GIT_LANDER_BASE_MOVED":           "true",
		"PG_GIT_LANDER_CONTEXT_TICKET_ID":    "X-1",
		"PG_GIT_LANDER_CONTEXT_A_B_C":        "v",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d vars, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	// rebase: no land-only variables; before: no RESULT, WORK_BRANCH empty.
	facts.Operation, facts.WorkBranch = "rebase", ""
	b := facts.Vars(hooks.PointBefore)
	for _, k := range []string{"DESTINATION_WORKTREE", "TARGET", "TARGET_TIP", "RESULT"} {
		if _, ok := b[hooks.EnvPrefix+k]; ok {
			t.Errorf("%s set for a rebase at before", k)
		}
	}
	if v, ok := b["PG_GIT_LANDER_WORK_BRANCH"]; !ok || v != "" {
		t.Errorf("WORK_BRANCH = %q, %v; want set and empty", v, ok)
	}
}

func TestHookSeesEnvironmentAndRunsInListOrder(t *testing.T) {
	f := newFx(t, 2)
	out := filepath.Join(f.scripts, "out")
	first := f.script("first", `echo "first $PG_GIT_LANDER_HOOK $PG_GIT_LANDER_CONTEXT_K $EXTRA $PG_GIT_LANDER_OPERATION" >> `+out)
	second := f.script("second", `echo second >> `+out)
	h1 := hk(first)
	h1.Env = map[string]string{"EXTRA": "e"}
	facts := f.facts()
	facts.Context = []hooks.ContextPair{{Key: "k", Value: "v"}}
	res, err := f.r.RunPoint(t.Context(), hooks.PointInput{Point: hooks.PointAfterRebase, Hooks: []config.Hook{h1, hk(second)}, Facts: facts})
	if err != nil || !res.Pass() {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "first after_rebase v e land\nsecond\n" {
		t.Fatalf("out = %q", b)
	}
	if len(res.Records) != 2 || res.Records[0].Argv0 != first || res.Records[0].Result != hooks.ResultPass {
		t.Fatalf("records = %+v", res.Records)
	}
	names := strings.Join(res.Records[0].AddedVars, ",")
	if !strings.Contains(names, "EXTRA") || !strings.Contains(names, "PG_GIT_LANDER_CONTEXT_K") {
		t.Fatalf("added vars = %s", names)
	}
}

func TestHookEnvValuesNeverReachALogLine(t *testing.T) {
	f := newFx(t, 2)
	h := hk(f.script("ok", "true"))
	h.Env = map[string]string{"API_TOKEN": "s3cr3t-value"}
	facts := f.facts()
	facts.Context = []hooks.ContextPair{{Key: "who", Value: "ctx-secret"}}
	res, err := f.r.RunPoint(t.Context(), hooks.PointInput{Point: hooks.PointAfter, Hooks: []config.Hook{h}, Facts: facts})
	if err != nil {
		t.Fatal(err)
	}
	line := res.Records[0].LogLine()
	if strings.Contains(line, "s3cr3t-value") || strings.Contains(line, "ctx-secret") || strings.Contains(line, "land") {
		t.Fatalf("log line carries a value: %s", line)
	}
	if !strings.Contains(line, "API_TOKEN") {
		t.Fatalf("log line lacks the variable name: %s", line)
	}
}

func TestExitCodeContract(t *testing.T) {
	f := newFx(t, 2)
	noExec := filepath.Join(f.scripts, "noexec")
	if err := os.WriteFile(noExec, []byte("#!/bin/sh\ntrue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	slow := hk(f.script("slow", "sleep 30"))
	slow.Timeout = 300 * time.Millisecond
	skip3 := hk(f.script("skip3", "echo line1 >&2; echo line2 >&2; exit 3"))
	skip3.SkipExitCodes = []int{3}
	skip126 := hk(noExec)
	skip126.SkipExitCodes = []int{126}
	skip127 := hk(filepath.Join(f.scripts, "missing"))
	skip127.SkipExitCodes = []int{127}

	cases := []struct {
		name     string
		h        config.Hook
		failKind hooks.FailKind // 0 = no failure
		code     int
		result   string
	}{
		{"pass", hk(f.script("zero", "exit 0")), 0, 0, hooks.ResultPass},
		{"skipped code", skip3, 0, 3, hooks.ResultSkipped},
		{"transient", hk(f.script("t75", "exit 75")), hooks.Transient, 75, hooks.ResultFailed},
		{"not executable is 126", hk(noExec), hooks.FailedExit, 126, hooks.ResultFailed},
		{"missing is 127", hk(filepath.Join(f.scripts, "missing")), hooks.FailedExit, 127, hooks.ResultFailed},
		{"missing on PATH is 127", hk("pgl-no-such-command-xyz"), hooks.FailedExit, 127, hooks.ResultFailed},
		{"126 can be skipped", skip126, 0, 126, hooks.ResultSkipped},
		{"127 can be skipped", skip127, 0, 127, hooks.ResultSkipped},
		{"other non-zero", hk(f.script("nine", "echo bad >&2; exit 9")), hooks.FailedExit, 9, hooks.ResultFailed},
		{"timeout", slow, hooks.TimedOut, -1, hooks.ResultFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			res := f.run(hooks.PointBeforeMove, c.h)
			if time.Since(start) > 20*time.Second {
				t.Fatal("did not stop at the timeout")
			}
			rec := res.Records[0]
			if rec.Result != c.result {
				t.Fatalf("result = %q, want %q (%+v)", rec.Result, c.result, rec)
			}
			if c.failKind == 0 {
				if res.Failure != nil {
					t.Fatalf("failure = %+v", res.Failure)
				}
				if c.result == hooks.ResultSkipped {
					if len(res.Skipped) != 1 || res.Skipped[0].Code != c.code || res.Skipped[0].Point != hooks.PointBeforeMove || res.Skipped[0].Index != 0 {
						t.Fatalf("skipped = %+v", res.Skipped)
					}
				}
				return
			}
			if res.Failure == nil || res.Failure.Kind != c.failKind {
				t.Fatalf("failure = %+v, want kind %d", res.Failure, c.failKind)
			}
			if c.failKind != hooks.TimedOut && res.Failure.Code != c.code {
				t.Fatalf("code = %d, want %d", res.Failure.Code, c.code)
			}
			if c.failKind == hooks.TimedOut && !rec.TimedOut {
				t.Fatal("record not marked timed out")
			}
		})
	}

	t.Run("skipped carries the stderr tail", func(t *testing.T) {
		res := f.run(hooks.PointAfter, skip3)
		if len(res.Skipped) != 1 || res.Skipped[0].StderrTail != "line1\nline2" {
			t.Fatalf("skipped = %+v", res.Skipped)
		}
	})
}

func TestFailureEffectByPoint(t *testing.T) {
	f := newFx(t, 2)
	fail := hk(f.script("nine", "exit 9"))
	trans := hk(f.script("t75", "exit 75"))
	slow := hk(f.script("slow", "sleep 30"))
	slow.Timeout = 200 * time.Millisecond

	refused := func(e hooks.Effect) bool {
		return e.Refusal != nil && *e.Refusal == outcome.RefBeforeHookRefused && e.Stop == nil
	}
	stop := func(want outcome.StopReason) func(hooks.Effect) bool {
		return func(e hooks.Effect) bool {
			return e.Stop != nil && *e.Stop == want && e.Refusal == nil && e.Warning == nil
		}
	}
	warn := func(e hooks.Effect) bool {
		return e.Warning != nil && *e.Warning == outcome.WarnAfterHookFailed && e.Stop == nil
	}

	cases := []struct {
		point string
		h     config.Hook
		ok    func(hooks.Effect) bool
	}{
		{hooks.PointBefore, fail, refused},
		{hooks.PointBefore, trans, refused},
		{hooks.PointBefore, slow, refused},
		{hooks.PointAfterRebase, fail, stop(outcome.StopHookFailed)},
		{hooks.PointAfterRebase, trans, stop(outcome.StopTransient)},
		{hooks.PointAfterRebase, slow, stop(outcome.StopTimeout)},
		{hooks.PointBeforeMove, fail, stop(outcome.StopHookFailed)},
		{hooks.PointBeforeMove, trans, stop(outcome.StopTransient)},
		{hooks.PointBeforeMove, slow, stop(outcome.StopTimeout)},
		{hooks.PointAfter, fail, warn},
		{hooks.PointAfter, trans, warn},
		{hooks.PointAfter, slow, warn},
	}
	for _, c := range cases {
		res := f.run(c.point, c.h)
		if res.Failure == nil {
			t.Fatalf("%s %v: no failure", c.point, c.h.Run)
		}
		if e := res.Failure.Effect(); !c.ok(e) {
			t.Errorf("%s %v: effect %+v", c.point, c.h.Run, e)
		}
	}
}

func TestUnknownPointIsRejected(t *testing.T) {
	f := newFx(t, 2)
	if _, err := f.r.RunPoint(t.Context(), hooks.PointInput{Point: "during", Facts: f.facts()}); err == nil {
		t.Fatal("want an error")
	}
}

func TestFailureStopsLaterHooks(t *testing.T) {
	f := newFx(t, 2)
	marker := filepath.Join(f.scripts, "later-ran")
	res := f.run(hooks.PointBefore, hk(f.script("bad", "exit 1")), hk(f.script("later", "touch "+marker)))
	if res.Failure == nil || len(res.Records) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a later hook ran after a failure")
	}
}

func TestCwdDefaults(t *testing.T) {
	f := newFx(t, 2)
	pwd := f.script("pwd", `pwd -P > "$1"`)
	check := func(point, cwd, want string) {
		t.Helper()
		out := filepath.Join(f.scripts, "pwd-"+point+cwd)
		h := config.Hook{Run: []string{pwd, out}, Cwd: cwd} // Timeout zero: the 10 minute default
		res := f.run(point, h)
		if !res.Pass() {
			t.Fatalf("%+v", res)
		}
		b, _ := os.ReadFile(out)
		if got := strings.TrimSpace(string(b)); got != want {
			t.Errorf("%s cwd %q ran in %s, want %s", point, cwd, got, want)
		}
	}
	facts := f.facts()
	check(hooks.PointBefore, "", facts.Repo)
	check(hooks.PointAfterRebase, "", f.work)
	check(hooks.PointAfterRebase, "repo", facts.Repo)
	check(hooks.PointBefore, "worktree", f.work)
	if hooks.DefaultTimeout != 10*time.Minute || config.DefaultTimeout != hooks.DefaultTimeout {
		t.Fatalf("default timeout = %v / %v", hooks.DefaultTimeout, config.DefaultTimeout)
	}
}

// formatter appends "fixed" to f.txt in the work worktree unless it is there,
// and fails when it had to.
const formatter = `grep -q fixed f.txt || { echo fixed >> f.txt; exit 1; }`

func (f *fx) log() string { return f.git(f.work, "log", "--format=%s", "main..work") }

func TestAutofixCommitsAndRunsAgain(t *testing.T) {
	f := newFx(t, 2)
	facts := f.facts()
	facts.Context = []hooks.ContextPair{{Key: "ticket", Value: "TICKET-77"}}
	res, err := f.r.RunPoint(t.Context(), hooks.PointInput{Point: hooks.PointAfterRebase, Hooks: []config.Hook{hk(f.script("fmt", formatter))}, Facts: facts})
	if err != nil || !res.Pass() {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(res.Records) != 2 || res.Records[0].Result != hooks.ResultAutofix || res.Records[1].Result != hooks.ResultPass {
		t.Fatalf("records = %+v", res.Records)
	}
	log := f.log()
	if log != "style: apply after_rebase fixes made during land" {
		t.Fatalf("log = %q", log)
	}
	if strings.Contains(f.git(f.work, "log", "-1", "--format=%B"), "TICKET-77") {
		t.Fatal("a --context value reached a commit message")
	}
	if f.git(f.work, "status", "--porcelain") != "" {
		t.Fatal("work worktree not clean")
	}
}

func TestAutofixIsBoundedByAttempts(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		f := newFx(t, n)
		always := f.script("always", `echo more >> f.txt; exit 0`)
		res := f.run(hooks.PointBeforeMove, hk(always))
		if res.Failure == nil || res.Failure.Kind != hooks.AutofixExhausted || len(res.Records) != n {
			t.Fatalf("n=%d: res = %+v", n, res)
		}
		if e := res.Failure.Effect(); e.Stop == nil || *e.Stop != outcome.StopHookFailed {
			t.Fatalf("n=%d: effect %+v", n, e)
		}
		if got := len(strings.Split(f.log(), "\n")); got != n {
			t.Fatalf("n=%d: %d fix commits", n, got)
		}
	}
}

func TestFailingHookThatChangesNothingStopsAtOnce(t *testing.T) {
	f := newFx(t, 5)
	res := f.run(hooks.PointAfterRebase, hk(f.script("bad", "exit 1")))
	if res.Failure == nil || res.Failure.Kind != hooks.FailedExit || len(res.Records) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if f.log() != "" {
		t.Fatal("a commit was made")
	}
}

func TestAutofixOnlyAtAfterRebaseAndBeforeMove(t *testing.T) {
	for _, point := range []string{hooks.PointBefore, hooks.PointAfter} {
		f := newFx(t, 2)
		res := f.run(point, hk(f.script("fmt", formatter)))
		if res.Failure == nil || len(res.Records) != 1 {
			t.Fatalf("%s: res = %+v", point, res)
		}
		if f.log() != "" {
			t.Fatalf("%s: autofix committed", point)
		}
	}
}

func TestAutofixLeavesUntrackedUncommitted(t *testing.T) {
	f := newFx(t, 2)
	h := hk(f.script("mk", `touch new.txt; grep -q fixed f.txt || { echo fixed >> f.txt; }`))
	res := f.run(hooks.PointBeforeMove, h)
	if !res.Pass() || len(res.Untracked) != 1 || res.Untracked[0] != "new.txt" {
		t.Fatalf("res = %+v", res)
	}
	if strings.Contains(f.git(f.work, "show", "--stat", "--format=", "HEAD"), "new.txt") {
		t.Fatal("untracked file committed")
	}
}

func TestAutofixAfterAPassingHookAndASkippedOne(t *testing.T) {
	f := newFx(t, 2)
	skip := hk(f.script("fmt-skip", `grep -q fixed f.txt || { echo fixed >> f.txt; exit 4; }`))
	skip.SkipExitCodes = []int{4}
	res := f.run(hooks.PointAfterRebase, skip)
	if !res.Pass() || len(res.Records) != 2 || len(res.Skipped) != 1 || res.Records[0].Result != hooks.ResultAutofix {
		t.Fatalf("res = %+v", res)
	}
}

func (f *fx) plantCommitHook(body string) {
	f.t.Helper()
	dir := filepath.Join(f.repo.Root(), "hooks")
	if err := os.WriteFile(filepath.Join(dir, "pre-commit"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) stageChange() {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.work, "f.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git(f.work, "add", "f.txt")
}

func TestCommitRetriesWhenAHookRejectsAndLeavesChanges(t *testing.T) {
	f := newFx(t, 2)
	f.plantCommitHook(`grep -q fixed f.txt || { echo fixed >> f.txt; exit 1; }`)
	f.stageChange()
	cr := f.r.Commit(t.Context(), hooks.CommitSpec{Dir: f.work, Message: "resolve"})
	if cr.Err != nil || cr.Stop != nil || cr.Tries != 2 {
		t.Fatalf("cr = %+v", cr)
	}
	if f.log() != "resolve" {
		t.Fatalf("log = %q", f.log())
	}
	if !strings.Contains(f.git(f.work, "show", "HEAD:f.txt"), "fixed") {
		t.Fatal("the hook's change was not part of the retried commit")
	}
}

func TestCommitRejectedAfterAttempts(t *testing.T) {
	for _, n := range []int{1, 2, 3} {
		f := newFx(t, n)
		f.plantCommitHook(`echo again >> f.txt; echo "hint: do the thing" >&2; echo "bad format" >&2; exit 1`)
		f.stageChange()
		cr := f.r.Commit(t.Context(), hooks.CommitSpec{Dir: f.work, Message: "resolve"})
		if cr.Stop == nil || *cr.Stop != outcome.StopCommitRejected || cr.Tries != n {
			t.Fatalf("n=%d: cr = %+v", n, cr)
		}
		if strings.Contains(cr.LogTail, "hint:") || !strings.Contains(cr.LogTail, "bad format") {
			t.Fatalf("log tail = %q", cr.LogTail)
		}
		if f.log() != "" {
			t.Fatalf("n=%d: a commit was made", n)
		}
	}
}

func TestCommitRejectionThatLeavesNothingStopsAtOnce(t *testing.T) {
	f := newFx(t, 5)
	f.plantCommitHook(`exit 1`)
	f.stageChange()
	cr := f.r.Commit(t.Context(), hooks.CommitSpec{Dir: f.work, Message: "resolve"})
	if cr.Stop == nil || cr.Tries != 1 {
		t.Fatalf("cr = %+v", cr)
	}
}

func TestCommitSkipsGitHooksWhenConfiguredOff(t *testing.T) {
	f := newFx(t, 2)
	f.r.RunGitHooks = false
	f.plantCommitHook(`exit 1`)
	f.stageChange()
	cr := f.r.Commit(t.Context(), hooks.CommitSpec{Dir: f.work, Message: "resolve"})
	if cr.Stop != nil || cr.Err != nil || f.log() != "resolve" {
		t.Fatalf("cr = %+v, log %q", cr, f.log())
	}
}

func TestAutofixCommitRejectedByGitHook(t *testing.T) {
	f := newFx(t, 2)
	f.plantCommitHook(`echo again >> f.txt; exit 1`)
	res := f.run(hooks.PointAfterRebase, hk(f.script("always", `echo more >> f.txt`)))
	if res.Failure == nil || res.Failure.Kind != hooks.CommitRejected {
		t.Fatalf("res = %+v", res)
	}
	if e := res.Failure.Effect(); e.Stop == nil || *e.Stop != outcome.StopCommitRejected {
		t.Fatalf("effect %+v", e)
	}
}

func TestLogTailDropsHintsAndKeepsFifty(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 80; i++ {
		b.WriteString("line\n")
		b.WriteString("hint: use git rebase --continue\n")
	}
	got := hooks.LogTail("", b.String())
	if strings.Contains(got, "hint:") || len(strings.Split(got, "\n")) != 50 {
		t.Fatalf("tail = %q", got)
	}
}
