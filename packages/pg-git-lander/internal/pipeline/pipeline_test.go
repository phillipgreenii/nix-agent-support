package pipeline_test

import (
	"context"
	"encoding/json"
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
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/oplog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/rebase"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/seams/seamstest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

const thisHost = "laptop.local"

type fx struct {
	t     *testing.T
	repo  *gitfixture.Repo
	g     *gitexec.Git
	clock *seamstest.Clock
	live  *seamstest.Liveness
	xdg   string
	main  string
	wt    string // linked worktree of feature/x
}

func newFx(t *testing.T) *fx {
	t.Helper()
	repo := gittest.New(t, gitfixture.RepoOptions{InitialCommit: true})
	root := repo.Root()
	var env []string
	for _, kv := range os.Environ() { // an inherited identity would hide a missing one
		if k, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(k, "GIT_AUTHOR_") && !strings.HasPrefix(k, "GIT_COMMITTER_") && k != "EMAIL" {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+filepath.Join(root, "home"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(root, "home", ".gitconfig"))
	f := &fx{
		t: t, repo: repo, clock: seamstest.NewClock(time.Now()), live: seamstest.NewLiveness(),
		g:   &gitexec.Git{Runner: cmdrun.Exec, Env: seams.FilteredEnv(env)},
		xdg: t.TempDir(),
	}
	f.main = f.git(repo.Dir, "symbolic-ref", "--short", "HEAD")
	f.writeXDG("[defaults.lock]\nwait = \"3s\"\n")
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

func (f *fx) writeXDG(s string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.xdg, "config.toml"), []byte(s), 0o600); err != nil {
		f.t.Fatal(err)
	}
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

// feature adds the linked worktree of feature/x with one commit of its own.
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

func owner(pid int, token string) locks.Owner {
	return locks.Owner{Host: thisHost, PID: pid, PIDStart: 1794132898, Token: token, ToolVersion: "26.11.10"}
}

func (f *fx) deps(o locks.Owner) pipeline.Deps {
	return pipeline.Deps{
		Git: f.g, Clock: f.clock, Liveness: f.live, Host: seamstest.Host(thisHost),
		Paths: seamstest.ConfigPaths(f.xdg), Owner: o,
		Sleep:  func(_ context.Context, d time.Duration) error { f.clock.Advance(d); return nil },
		Jitter: func(max time.Duration) time.Duration { return max / 2 },
	}
}

func (f *fx) land() pipeline.Request {
	return pipeline.Request{Operation: worktrees.Land, Branch: "feature/x", Target: f.main, Dir: f.repo.Dir}
}

func (f *fx) start(req pipeline.Request) *pipeline.Result {
	f.t.Helper()
	res, err := pipeline.Start(f.t.Context(), f.deps(owner(41721, "t1")), req)
	if err != nil {
		f.t.Fatalf("Start: %v", err)
	}
	return res
}

func (f *fx) prepared(req pipeline.Request) *pipeline.Prepared {
	f.t.Helper()
	res := f.start(req)
	if res.Prepared == nil {
		f.t.Fatalf("no prepared operation: %+v", res.Outcome)
	}
	f.t.Cleanup(func() {
		_ = res.Prepared.Release(context.Background())
		_ = res.Prepared.Close()
	})
	return res.Prepared
}

func (f *fx) lockRefs() string { return f.git(f.repo.Dir, "for-each-ref", "refs/pg-git-lander/") }
func (f *fx) workBranches() string {
	return f.git(f.repo.Dir, "branch", "--list", "pg-git-lander/*")
}

// untouched asserts that nothing the tool owns is left: no lock ref and no
// work branch.
func (f *fx) untouched() {
	f.t.Helper()
	if s := f.lockRefs(); s != "" {
		f.t.Errorf("lock refs left: %s", s)
	}
	if s := f.workBranches(); s != "" {
		f.t.Errorf("work branches left: %s", s)
	}
}

func (f *fx) events() []map[string]any {
	f.t.Helper()
	b, err := os.ReadFile(oplog.EventsPath(f.git(f.repo.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		f.t.Fatal(err)
	}
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			f.t.Fatalf("event line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func eventNames(evs []map[string]any) []string {
	var n []string
	for _, e := range evs {
		n = append(n, e["event"].(string))
	}
	return n
}

func (f *fx) wantRefused(res *pipeline.Result, reason outcome.RefusalReason) *outcome.Outcome {
	f.t.Helper()
	if res.Prepared != nil || res.Outcome == nil || res.Outcome.Outcome != outcome.Refused || res.Outcome.Reason != string(reason) {
		f.t.Fatalf("want refused %s, got prepared=%v outcome=%+v", reason, res.Prepared != nil, res.Outcome)
	}
	if res.Outcome.Exit != outcome.ExitRefused {
		f.t.Errorf("exit = %d", res.Outcome.Exit)
	}
	return res.Outcome
}

func TestLandFastForwardPreparesWorkBranchAndRecords(t *testing.T) {
	f := newFx(t)
	f.feature()
	branchTip := f.git(f.repo.Dir, "rev-parse", "feature/x")
	targetTip := f.git(f.repo.Dir, "rev-parse", f.main)

	p := f.prepared(f.land())
	if p.Path != rebase.FastForward || p.Done() {
		t.Fatalf("path = %s", p.Path)
	}
	if p.Tips.Branch != branchTip || p.Tips.Target != targetTip {
		t.Fatalf("tips = %+v", p.Tips)
	}
	// The work branch is at tips.branch and checked out in the work worktree.
	if got := f.git(f.wt, "branch", "--show-current"); got != p.WorkBranch {
		t.Fatalf("work worktree on %q, want %q", got, p.WorkBranch)
	}
	if got := f.git(f.wt, "rev-parse", "HEAD"); got != branchTip {
		t.Fatalf("work branch at %s", got)
	}
	// The record carries the snapshot, the digest and the stage.
	rec := p.Op.Record()
	if rec.Stage != "preparing" || rec.Tips.Branch == nil || *rec.Tips.Branch != branchTip ||
		rec.Tips.Target == nil || *rec.Tips.Target != targetTip || rec.ConfigDigest == "" ||
		rec.ConfigDigest != p.Config.Digest || rec.Tips.Result == nil || *rec.Tips.Result != branchTip {
		t.Fatalf("record = %+v", rec)
	}
	if rec.WorkBranch != p.WorkBranch || len(rec.Worktrees) != 2 {
		t.Fatalf("record worktrees/work branch: %+v", rec)
	}
	// Locks: TARGET, BRANCH and both worktrees.
	if n := strings.Count(f.lockRefs(), "\n") + 1; n != 4 {
		t.Fatalf("%d lock refs:\n%s", n, f.lockRefs())
	}
	if p.Stage != "preparing" {
		t.Fatalf("stage = %s", p.Stage)
	}
}

func TestEventsAndLogOfSteps1To7(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.git(f.repo.Dir, "config", "merge.zdriver.driver", "true")
	f.writeXDG("[defaults.lock]\nwait = \"3s\"\n[defaults.hooks]\nland.before = [{ run = [\"true\"] }]\n")
	p := f.prepared(f.land())

	evs := f.events()
	names := eventNames(evs)
	want := []string{"op_start", "stage", "hook", "stage"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", names, want)
	}
	start := evs[0]
	if start["op_id"] != p.OpID || start["operation"] != "land" || start["branch"] != "feature/x" ||
		start["target"] != f.main || start["work_branch"] != p.WorkBranch || start["config_digest"] != p.Config.Digest {
		t.Fatalf("op_start = %v", start)
	}
	if wts, _ := start["worktrees"].([]any); len(wts) != 2 {
		t.Fatalf("op_start worktrees = %v", start["worktrees"])
	}
	if evs[1]["stage"] != "locked" || evs[1]["previous_stage"] != "" {
		t.Fatalf("first stage = %v", evs[1])
	}
	if evs[2]["point"] != "before" || evs[2]["result"] != "pass" || evs[2]["argv0"] != "true" {
		t.Fatalf("hook = %v", evs[2])
	}
	if evs[3]["stage"] != "preparing" || evs[3]["previous_stage"] != "locked" {
		t.Fatalf("second stage = %v", evs[3])
	}

	// The log names BRANCH, TARGET and the op_id on its first line, and holds
	// the settings step 4 logs.
	b, err := os.ReadFile(p.Log.Path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	for _, w := range []string{"op_id=" + p.OpID, "branch=feature/x", "target=" + f.main} {
		if !strings.Contains(lines[0], w) {
			t.Errorf("first log line %q lacks %q", lines[0], w)
		}
	}
	for _, w := range []string{"core.attributesFile=", "core.hooksPath=", "merge.zdriver.driver=true"} {
		if !strings.Contains(string(b), w) {
			t.Errorf("log lacks %q", w)
		}
	}
}

func TestLandPaths(t *testing.T) {
	t.Run("rebase when both sides moved", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.commit(f.repo.Dir, "m.txt", "m\n")
		p := f.prepared(f.land())
		if p.Path != rebase.NeedsRebase {
			t.Fatalf("path = %s", p.Path)
		}
	})
}

func TestNothingToLandAndUpToDateKeepLocksForCleanup(t *testing.T) {
	f := newFx(t)
	f.git(f.repo.Dir, "worktree", "add", "-q", "-b", "feature/x", filepath.Join(f.repo.Root(), "wt-x"))
	f.wt, _ = filepath.EvalSymlinks(filepath.Join(f.repo.Root(), "wt-x"))

	res := f.start(f.land())
	if res.Prepared == nil || res.Outcome == nil || res.Outcome.Outcome != outcome.NothingToLand ||
		res.Prepared.Path != rebase.NothingToLand {
		t.Fatalf("land: %+v", res)
	}
	if f.lockRefs() == "" {
		t.Fatal("locks must still be held for clean-up")
	}
	_ = res.Prepared.Release(t.Context())
	_ = res.Prepared.Close()

	res = f.start(pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.wt})
	if res.Prepared == nil || res.Outcome == nil || res.Outcome.Outcome != outcome.UpToDate ||
		res.Prepared.Path != rebase.UpToDate {
		t.Fatalf("rebase: %+v", res)
	}
	_ = res.Prepared.Release(t.Context())
	_ = res.Prepared.Close()
	if f.lockRefs() != "" {
		t.Fatalf("locks after release: %s", f.lockRefs())
	}
}

func TestRebasePathAndOntoResolvedOnce(t *testing.T) {
	f := newFx(t)
	f.feature()
	oldMain := f.git(f.repo.Dir, "rev-parse", f.main)
	f.commit(f.repo.Dir, "m.txt", "m\n")
	newMain := f.git(f.repo.Dir, "rev-parse", f.main)

	// Rebase onto a REF that moves while the operation runs: ONTO stays.
	p := f.prepared(pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.wt})
	if p.Path != rebase.NeedsRebase || p.Tips.Onto != newMain || p.Tips.Onto == oldMain {
		t.Fatalf("path=%s tips=%+v", p.Path, p.Tips)
	}
	f.commit(f.repo.Dir, "m2.txt", "m2\n")
	if err := p.Resnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.Tips.Onto != newMain || p.Facts.Onto != newMain {
		t.Fatalf("ONTO re-resolved: %+v / %s", p.Tips, p.Facts.Onto)
	}
	if rec := p.Op.Record(); rec.Tips.Onto == nil || *rec.Tips.Onto != newMain {
		t.Fatalf("record onto = %v", rec.Tips.Onto)
	}
	// A rebase takes BRANCH and the worktree lock, not TARGET's.
	if got := f.lockRefs(); strings.Contains(got, "heads/"+f.main) {
		t.Fatalf("rebase locked the primary branch:\n%s", got)
	}
}

func TestUsageErrorsTakeNoLock(t *testing.T) {
	f := newFx(t)
	f.feature()
	cases := map[string]pipeline.Request{
		"branch equals target":  {Operation: worktrees.Land, Branch: "feature/x", Target: "feature/x", Dir: f.repo.Dir},
		"missing target":        {Operation: worktrees.Land, Branch: "feature/x", Dir: f.repo.Dir},
		"target does not exist": {Operation: worktrees.Land, Branch: "feature/x", Target: "nope", Dir: f.repo.Dir},
		"branch does not exist": {Operation: worktrees.Land, Branch: "nope", Target: f.main, Dir: f.repo.Dir},
		"missing onto":          {Operation: worktrees.Rebase, Branch: "feature/x", Dir: f.repo.Dir},
		"onto not a commit":     {Operation: worktrees.Rebase, Branch: "feature/x", Onto: "nope", Dir: f.repo.Dir},
		"not a repository":      {Operation: worktrees.Land, Branch: "a", Target: "b", Dir: t.TempDir()},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			res := f.start(req)
			if res.Prepared != nil || res.Outcome == nil || res.Outcome.Outcome != outcome.UsageError || res.Outcome.Exit != 2 {
				t.Fatalf("got %+v", res.Outcome)
			}
			f.untouched()
		})
	}
	if evs := f.events(); len(evs) != 0 {
		t.Fatalf("usage errors wrote events: %v", eventNames(evs))
	}
}

func TestNamespaceCollisionNeedsNoLock(t *testing.T) {
	f := newFx(t)
	f.feature()
	for _, req := range []pipeline.Request{
		{Operation: worktrees.Land, Branch: "pg-git-lander/idle/x", Target: f.main, Dir: f.repo.Dir},
		{Operation: worktrees.Land, Branch: "feature/x", Target: "pg-git-lander/y", Dir: f.repo.Dir},
		{Operation: worktrees.Rebase, Branch: "pg-git-lander", Onto: f.main, Dir: f.repo.Dir},
	} {
		f.wantRefused(f.start(req), outcome.RefNamespaceCollision)
		f.untouched()
	}
	if evs := f.events(); len(evs) != 0 {
		t.Fatalf("events: %v", eventNames(evs))
	}
}

func TestDirtyWorktreeRefusedBeforeAnyLockOrEvent(t *testing.T) {
	f := newFx(t)
	f.feature()
	if err := os.WriteFile(filepath.Join(f.wt, "x.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := f.wantRefused(f.start(f.land()), outcome.RefDirty)
	if o.Role == "" || o.Path == "" {
		t.Errorf("dirty refusal lacks role or path: %+v", o)
	}
	f.untouched()
	if evs := f.events(); len(evs) != 0 {
		t.Fatalf("events: %v", eventNames(evs))
	}
}

func TestNoGitIdentityRefusedAfterSnapshotAndReleasesLocks(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.git(f.repo.Dir, "config", "--unset", "user.email")
	f.git(f.repo.Dir, "config", "user.useConfigOnly", "true")
	o := f.wantRefused(f.start(f.land()), outcome.RefNoGitIdentity)
	f.untouched()
	if got := f.git(f.wt, "branch", "--show-current"); got != "feature/x" {
		t.Fatalf("worktree switched to %s", got)
	}
	names := eventNames(f.events())
	if strings.Join(names, ",") != "op_start,stage,op_end" {
		t.Fatalf("events = %v", names)
	}
	if o.OpID == "" || o.Log == "" {
		t.Errorf("outcome lacks op id or log: %+v", o)
	}
	end := f.events()[2]
	if end["outcome"] != "refused" || end["reason"] != "no-git-identity" {
		t.Fatalf("op_end = %v", end)
	}
}

func TestRepoFileRefusals(t *testing.T) {
	t.Run("untrusted", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.commit(f.repo.Dir, config.RepoFileName, "[hooks.land]\nbefore = [{ run = [\"true\"] }]\n")
		f.wantRefused(f.start(f.land()), outcome.RefUntrustedConfig)
		f.untouched()
	})
	t.Run("does not parse", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.commit(f.repo.Dir, config.RepoFileName, "max_race_retries = \"x\"\n")
		o := f.wantRefused(f.start(f.land()), outcome.RefRepoConfigError)
		if o.ConfigError == nil || o.ConfigError.Key == "" {
			t.Errorf("no location: %+v", o)
		}
		f.untouched()
	})
	t.Run("land reads TARGET's tip, rebase the primary branch", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.commit(f.repo.Dir, config.RepoFileName, "max_race_retries = 5\n")
		p := f.prepared(f.land())
		if p.Config.MaxRaceRetries != 5 {
			t.Fatalf("land config: %d", p.Config.MaxRaceRetries)
		}
		_ = p.Release(t.Context())
		_ = p.Close()
		f.git(f.wt, "switch", "-q", "feature/x")
		r := f.prepared(pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.wt})
		if r.Config.MaxRaceRetries != 5 {
			t.Fatalf("rebase config: %d", r.Config.MaxRaceRetries)
		}
	})
}

func (f *fx) hookScript(body string) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), "hook.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fx) beforeHook(script, extra string) {
	f.writeXDG("[defaults.lock]\nwait = \"3s\"\n[defaults.hooks]\nland.before = [{ run = ['" + script + "']" + extra + " }]\n")
}

func TestBeforeHookRunsOnceWithTipsAndWorktreesBeforeAnySwitch(t *testing.T) {
	f := newFx(t)
	f.feature()
	out := filepath.Join(t.TempDir(), "hook.out")
	f.beforeHook(f.hookScript(`{
echo "branch_start=$PG_GIT_LANDER_BRANCH_START"
echo "target_tip=$PG_GIT_LANDER_TARGET_TIP"
echo "work_worktree=$PG_GIT_LANDER_WORK_WORKTREE"
echo "source_worktree=$PG_GIT_LANDER_SOURCE_WORKTREE"
echo "destination_worktree=$PG_GIT_LANDER_DESTINATION_WORKTREE"
echo "work_branch=$PG_GIT_LANDER_WORK_BRANCH"
echo "op_id=$PG_GIT_LANDER_OP_ID"
echo "hook=$PG_GIT_LANDER_HOOK"
echo "ctx=$PG_GIT_LANDER_CONTEXT_REQUEST_ID"
echo "on=$(git -C "$PG_GIT_LANDER_WORK_WORKTREE" branch --show-current)"
} >> `+out), "")
	req := f.land()
	req.Context = []hooks.ContextPair{{Key: "request-id", Value: "R-17"}}
	p := f.prepared(req)

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Count(got, "hook=before") != 1 {
		t.Fatalf("hook ran %d times:\n%s", strings.Count(got, "hook=before"), got)
	}
	for _, w := range []string{
		"branch_start=" + p.Tips.Branch, "target_tip=" + p.Tips.Target,
		"work_worktree=" + f.wt, "source_worktree=" + f.wt, "destination_worktree=" + f.repo.Dir,
		"work_branch=\n", "op_id=" + p.OpID, "ctx=R-17", "on=feature/x",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("hook output lacks %q:\n%s", w, got)
		}
	}
	// A return to step 3 repeats neither the hook nor the preparation.
	f.commit(f.repo.Dir, "m.txt", "m\n")
	before := f.workBranches()
	if err := p.Resnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(out)
	if string(b2) != got {
		t.Fatalf("hook ran again:\n%s", b2)
	}
	if f.workBranches() != before {
		t.Fatal("work branch changed by Resnapshot")
	}
}

func TestBeforeHookRefusals(t *testing.T) {
	cases := []struct {
		name, body, extra, detail string
	}{
		{"non-zero", "echo nope >&2; exit 3", "", "exit-3"},
		{"exit 75", "exit 75", "", "transient"},
		{"timeout", "sleep 30", ", timeout = \"300ms\"", "timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			f.feature()
			f.beforeHook(f.hookScript(c.body), c.extra)
			o := f.wantRefused(f.start(f.land()), outcome.RefBeforeHookRefused)
			if o.Detail != c.detail {
				t.Errorf("detail = %q, want %q", o.Detail, c.detail)
			}
			f.untouched()
			if got := f.git(f.wt, "branch", "--show-current"); got != "feature/x" {
				t.Fatalf("worktree switched to %s", got)
			}
			names := eventNames(f.events())
			if strings.Join(names, ",") != "op_start,stage,hook,op_end" {
				t.Fatalf("events = %v", names)
			}
		})
	}
}

func TestSwitchRefusedWhenUntrackedFileCollides(t *testing.T) {
	f := newFx(t)
	f.feature()
	// BRANCH is checked out in wt; make the work worktree a different one
	// with an untracked file that collides with a tracked file of BRANCH.
	other := filepath.Join(f.repo.Root(), "wt-w")
	f.git(f.repo.Dir, "worktree", "add", "-q", "--detach", other)
	other, _ = filepath.EvalSymlinks(other)
	if err := os.WriteFile(filepath.Join(other, "x.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := f.land()
	req.WorkWorktree = other
	f.wantRefused(f.start(req), outcome.RefSwitchRefused)
	f.untouched()
}

func TestBusyNamesTheHolderAndChangesNothing(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.live.SetAlive(41721, "1794132898")
	f.prepared(f.land()) // holds every lock

	d := f.deps(owner(7, "t2"))
	res, err := pipeline.Start(t.Context(), d, pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.wt})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Outcome
	if res.Prepared != nil || o == nil || o.Outcome != outcome.Busy || o.Exit != outcome.ExitBusy {
		t.Fatalf("got %+v", o)
	}
	if o.Holder == nil || o.Holder.Operation != "land" || o.Holder.Branch != "feature/x" || o.Holder.PID != 41721 {
		t.Fatalf("holder = %+v", o.Holder)
	}
	// Only the first operation's records remain; no op_start for the loser.
	for _, e := range f.events() {
		if e["op_id"] == o.OpID && e["event"] != "lock_wait" {
			t.Fatalf("busy operation wrote %v", e)
		}
	}
}

func TestDeadHolderIsConvertedAndRefusedAsHolderDied(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.prepared(f.land()) // pid 41721 is not alive in the fake

	d := f.deps(owner(7, "t2"))
	res, err := pipeline.Start(t.Context(), d, pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.wt})
	if err != nil {
		t.Fatal(err)
	}
	o := f.wantRefused(res, outcome.RefHolderDied)
	if len(o.BlockingOp) != 1 || o.BlockingOp[0].Branch != "feature/x" || len(o.Next) != 2 {
		t.Fatalf("outcome = %+v", o)
	}
	st, err := (&locks.Layer{Git: f.g, Clock: f.clock, Dir: f.repo.Dir}).Read(t.Context(), locks.BranchRef("feature/x"))
	if err != nil || st.Rec == nil || st.Rec.State != locks.StateStopped {
		t.Fatalf("record not converted to stopped: %+v %v", st, err)
	}
	if strings.Contains(strings.Join(o.Next, " "), "--repo") {
		t.Errorf("next carries --repo without the flag: %v", o.Next)
	}
}

func TestNextCommandsRepeatRepoFlagWhenUsed(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.prepared(f.land())
	d := f.deps(owner(7, "t2"))
	res, err := pipeline.Start(t.Context(), d, pipeline.Request{
		Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.wt, RepoFlag: f.repo.Dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	o := f.wantRefused(res, outcome.RefHolderDied)
	for _, n := range o.Next {
		if !strings.HasSuffix(n, "--repo "+f.repo.Dir) {
			t.Errorf("next %q lacks --repo", n)
		}
	}
}

func TestResnapshotRereadsTargetAndReloadsRepoFile(t *testing.T) {
	f := newFx(t)
	f.feature()
	p := f.prepared(f.land())
	oldDigest, oldTarget := p.Config.Digest, p.Tips.Target
	oldBranch := p.Tips.Branch

	newTarget := f.commit(f.repo.Dir, config.RepoFileName, "max_race_retries = 9\n")
	if err := p.Resnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.Tips.Target != newTarget || p.Tips.Target == oldTarget || p.Tips.Branch != oldBranch {
		t.Fatalf("tips = %+v", p.Tips)
	}
	if p.Config.MaxRaceRetries != 9 || p.Config.Digest == oldDigest {
		t.Fatalf("config not reloaded: %+v", p.Config)
	}
	rec := p.Op.Record()
	if rec.ConfigDigest != p.Config.Digest || *rec.Tips.Target != newTarget || p.Facts.TargetTip != newTarget {
		t.Fatalf("record = %+v", rec)
	}
	// The path is decided again from the new snapshot.
	path, err := p.Decide(t.Context(), p.Tips.Branch)
	if err != nil || path != rebase.NeedsRebase {
		t.Fatalf("path = %s, %v", path, err)
	}
}

func TestLeaseIsRenewedFromTheLockStepOn(t *testing.T) {
	f := newFx(t)
	f.feature()
	d := f.deps(owner(41721, "t1"))
	d.RenewInterval = 10 * time.Millisecond
	res, err := pipeline.Start(t.Context(), d, f.land())
	if err != nil || res.Prepared == nil {
		t.Fatalf("%v %+v", err, res)
	}
	defer func() { _ = res.Prepared.Release(context.Background()); _ = res.Prepared.Close() }()
	first := res.Prepared.Op.Record().Lease.RenewedAt
	f.clock.Advance(time.Minute)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if res.Prepared.Op.Record().Lease.RenewedAt.After(first) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the lease was never renewed")
}
