package pipeline_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// Tests of steps 8 to 13: real git, real hook scripts, real lock refs.

func (f *fx) rebaseReq() pipeline.Request {
	return pipeline.Request{Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.repo.Dir}
}

// run starts the operation and completes it.
func (f *fx) run(req pipeline.Request) *outcome.Outcome {
	f.t.Helper()
	p := f.prepared(req)
	return f.complete(p)
}

func (f *fx) complete(p *pipeline.Prepared) *outcome.Outcome {
	f.t.Helper()
	out, err := p.Run(f.t.Context())
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	if out == nil {
		f.t.Fatal("Run returned no outcome")
	}
	return out
}

func (f *fx) cfgXDG(extra string) { f.writeXDG("[defaults.lock]\nwait = \"3s\"\n" + extra) }

// hooksXDG configures land hooks (point to script path).
func (f *fx) hooksXDG(op string, points map[string]string, extra string) {
	var b strings.Builder
	b.WriteString("[defaults.hooks]\n")
	for pt, script := range points {
		b.WriteString(op + "." + pt + " = [{ run = ['" + script + "'] }]\n")
	}
	f.cfgXDG(b.String() + extra)
}

func (f *fx) tip(ref string) string { return f.git(f.repo.Dir, "rev-parse", ref) }

func (f *fx) refExists(ref string) bool {
	return f.g.Run(f.t.Context(), f.repo.Dir, "rev-parse", "--verify", "-q", ref).OK()
}

func (f *fx) record(ref string) map[string]any {
	f.t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(f.git(f.repo.Dir, "cat-file", "-p", ref)), &m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fx) mainFile(name string) string {
	f.t.Helper()
	return f.git(f.repo.Dir, "show", f.main+":"+name)
}

func (f *fx) logSubjects(ref string) []string {
	return strings.Fields(strings.ReplaceAll(f.git(f.repo.Dir, "log", "--format=%s", ref), " ", "_"))
}

func want(t *testing.T, o *outcome.Outcome, w outcome.Word, exit int) {
	t.Helper()
	if o.Outcome != w || o.Exit != exit {
		t.Fatalf("outcome = %s exit %d (reason %q: %s), want %s exit %d", o.Outcome, o.Exit, o.Reason, o.Message, w, exit)
	}
}

func (f *fx) wantStopped(o *outcome.Outcome, reason outcome.StopReason) {
	f.t.Helper()
	want(f.t, o, outcome.Stopped, 4)
	if o.Reason != string(reason) {
		f.t.Fatalf("reason = %s (%s), want %s", o.Reason, o.Message, reason)
	}
	if len(o.Next) == 0 {
		f.t.Errorf("a stop carries valid next commands: %+v", o)
	}
	if o.MovedAlready == nil || *o.MovedAlready {
		f.t.Errorf("moved_already = %v", o.MovedAlready)
	}
	// The TARGET record is gone; the BRANCH record is stopped with a null lease.
	if f.refExists(locks.BranchRef(f.main)) {
		f.t.Errorf("the TARGET record survived a stop")
	}
	r := f.record(locks.BranchRef("feature/x"))
	if r["state"] != "stopped" || r["lease"] != nil {
		f.t.Errorf("BRANCH record = %v", r)
	}
	if st, _ := r["stop"].(map[string]any); st == nil || st["reason"] != string(reason) {
		f.t.Errorf("stop object = %v", r["stop"])
	}
}

func lastEvent(evs []map[string]any, name string) map[string]any {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i]["event"] == name {
			return evs[i]
		}
	}
	return nil
}

func countEvents(evs []map[string]any, name string) int {
	n := 0
	for _, e := range evs {
		if e["event"] == name {
			n++
		}
	}
	return n
}

func TestRunLandFastForwardNeverMakesAMergeCommit(t *testing.T) {
	f := newFx(t)
	f.feature()
	featTip := f.tip("feature/x")
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if out.PathTaken != outcome.PathFastForward || out.LandedCommit != featTip {
		t.Fatalf("path %s landed %s", out.PathTaken, out.LandedCommit)
	}
	if f.tip(f.main) != featTip {
		t.Fatal("TARGET is not BRANCH's own tip")
	}
	if m := f.git(f.repo.Dir, "rev-list", "--merges", f.main); m != "" {
		t.Fatalf("merge commits: %s", m)
	}
	if f.refExists("refs/heads/feature/x") {
		t.Error("BRANCH was not retired")
	}
	if out.BranchFate != outcome.BranchDeleted || out.Stats == nil || out.Stats.RebasedCommits != 0 {
		t.Errorf("outcome = %+v", out)
	}
	f.untouched()
}

func TestRunLandRebasesOntoAMovedTarget(t *testing.T) {
	f := newFx(t)
	f.feature()
	featTip := f.tip("feature/x")
	f.commit(f.repo.Dir, "m.txt", "m\n")
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if out.PathTaken != outcome.PathRebase || out.Stats.RebasedCommits != 1 {
		t.Fatalf("path %s stats %+v", out.PathTaken, out.Stats)
	}
	if out.LandedCommit == featTip || f.tip(f.main) != out.LandedCommit {
		t.Fatalf("landed %s (BRANCH was %s, TARGET is %s)", out.LandedCommit, featTip, f.tip(f.main))
	}
	if m := f.git(f.repo.Dir, "rev-list", "--merges", f.main); m != "" {
		t.Fatalf("merge commits: %s", m)
	}
	if f.mainFile("x.txt") != "x" || f.mainFile("m.txt") != "m" {
		t.Fatal("the landed history lacks a file")
	}
	f.untouched()
}

func TestRunRebaseMovesBranchToTheResult(t *testing.T) {
	f := newFx(t)
	f.feature()
	old := f.tip("feature/x")
	mainTip := f.commit(f.repo.Dir, "m.txt", "m\n")
	out := f.run(f.rebaseReq())
	want(t, out, outcome.Rebased, 0)
	now := f.tip("feature/x")
	if now == old || out.Tips.Result != now {
		t.Fatalf("BRANCH %s (was %s) result %s", now, old, out.Tips.Result)
	}
	if !f.g.Run(t.Context(), f.repo.Dir, "merge-base", "--is-ancestor", mainTip, now).OK() {
		t.Fatal("BRANCH does not contain ONTO")
	}
	if f.tip(f.main) != mainTip {
		t.Fatal("a rebase moved TARGET")
	}
	if got := f.git(f.wt, "branch", "--show-current"); got != "feature/x" {
		t.Fatalf("source worktree on %q", got)
	}
	if f.git(f.wt, "rev-parse", "HEAD") != now || f.git(f.wt, "status", "--porcelain") != "" {
		t.Fatal("source worktree does not show the result")
	}
	f.untouched()
}

func TestRunNothingToLandAndUpToDate(t *testing.T) {
	t.Run("nothing-to-land", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.git(f.repo.Dir, "merge", "--ff-only", "feature/x")
		before := f.tip(f.main)
		out := f.run(f.land())
		want(t, out, outcome.NothingToLand, 0)
		if f.tip(f.main) != before || out.PathTaken != outcome.PathNothing || out.LandedCommit != "" {
			t.Fatalf("outcome = %+v", out)
		}
		f.untouched()
	})
	t.Run("up-to-date", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		old := f.tip("feature/x")
		out := f.run(f.rebaseReq())
		want(t, out, outcome.UpToDate, 0)
		if f.tip("feature/x") != old {
			t.Fatal("BRANCH moved")
		}
		f.untouched()
	})
}

func TestRunRebaseDroppingEverythingIsNothingToLand(t *testing.T) {
	f := newFx(t)
	f.feature()
	featCommit := f.tip("feature/x")
	f.commit(f.repo.Dir, "m.txt", "m\n")
	f.git(f.repo.Dir, "cherry-pick", featCommit) // same patch, new commit
	mainTip := f.tip(f.main)
	out := f.run(f.land())
	want(t, out, outcome.NothingToLand, 0)
	if len(out.Dropped) != 1 || out.Dropped[0] != featCommit {
		t.Fatalf("dropped = %v", out.Dropped)
	}
	if f.tip(f.main) != mainTip || out.LandedCommit != "" || out.PathTaken != outcome.PathNothing {
		t.Fatalf("outcome = %+v", out)
	}
	if out.BranchFate != outcome.BranchDeleted || f.refExists("refs/heads/feature/x") {
		t.Fatalf("BRANCH not retired: %+v", out)
	}
	f.untouched()
}

func (f *fx) conflicting() (mainTip, featTip string) {
	f.feature()
	featTip = f.tip("feature/x")
	mainTip = f.commit(f.repo.Dir, "x.txt", "main\n")
	return
}

func TestRunConflictWithNoRuleStopsAndRecordsStopped(t *testing.T) {
	f := newFx(t)
	mainTip, featTip := f.conflicting()
	req := f.land()
	req.RepoFlag = f.repo.Dir
	out := f.run(req)
	f.wantStopped(out, outcome.StopConflict)
	if len(out.Paths) != 1 || out.Paths[0] != "x.txt" {
		t.Fatalf("paths = %v", out.Paths)
	}
	for _, n := range out.Next {
		if !strings.HasSuffix(n, "--repo "+f.repo.Dir) {
			t.Errorf("next %q lacks --repo", n)
		}
	}
	if !strings.Contains(strings.Join(out.Next, "\n"), "pg-git-lander land feature/x --abort") {
		t.Errorf("next = %v", out.Next)
	}
	if f.tip(f.main) != mainTip || f.tip("feature/x") != featTip {
		t.Fatal("BRANCH or TARGET moved")
	}
	evs := f.events()
	if c := lastEvent(evs, "conflict"); c == nil || c["result"] != "stopped" {
		t.Fatalf("conflict event = %v", c)
	}
	if e := lastEvent(evs, "op_end"); e == nil || e["outcome"] != "stopped" || e["reason"] != "conflict" {
		t.Fatalf("op_end = %v", e)
	}
}

func (f *fx) resolver() {
	f.t.Helper()
	s := f.hookScript(`printf 'merged\n' > "$PG_GIT_LANDER_PATH"`)
	f.cfgXDG("[[defaults.conflict]]\nglob = \"x.txt\"\nrun_per = \"file\"\nresolve = ['" + s + "']\n")
}

func TestRunConflictResolvedByAConfiguredResolver(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	f.resolver()
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if len(out.Resolved) != 1 || out.Resolved[0].Path != "x.txt" || out.Resolved[0].Rule == "" || out.Stats.ConflictRounds != 1 {
		t.Fatalf("resolved = %+v stats %+v", out.Resolved, out.Stats)
	}
	if f.mainFile("x.txt") != "merged" {
		t.Fatalf("x.txt = %q", f.mainFile("x.txt"))
	}
	if c := lastEvent(f.events(), "conflict"); c == nil || c["result"] != "resolved" {
		t.Fatalf("conflict event = %v", c)
	}
}

func TestRunBeforeMoveHookFailureLeavesBranchAndTargetUntouched(t *testing.T) {
	f := newFx(t)
	f.feature()
	mainTip, featTip := f.tip(f.main), f.tip("feature/x")
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript("echo nope >&2\nexit 1")}, "")
	out := f.run(f.land())
	f.wantStopped(out, outcome.StopHookFailed)
	if out.Stage != "before_move" {
		t.Errorf("stage = %s", out.Stage)
	}
	if f.tip(f.main) != mainTip || f.tip("feature/x") != featTip {
		t.Fatal("BRANCH or TARGET moved")
	}
	if strings.Contains(strings.Join(out.LogTail, "\n"), "nope") == false {
		t.Errorf("log tail = %v", out.LogTail)
	}
	if h := lastEvent(f.events(), "hook"); h == nil || h["point"] != "before_move" {
		t.Fatalf("hook event = %v", h)
	}
}

func TestRunTransientHookIsAStop(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript("exit 75")}, "")
	f.wantStopped(f.run(f.land()), outcome.StopTransient)
}

func TestRunHookPointsRunOnlyWhereTheyApply(t *testing.T) {
	marks := func(f *fx) (string, map[string]string) {
		dir := t.TempDir()
		pts := map[string]string{}
		for _, p := range []string{"after_rebase", "before_move", "after"} {
			pts[p] = f.hookScript("touch " + filepath.Join(dir, p))
		}
		return dir, pts
	}
	exists := func(dir, p string) bool { _, err := os.Stat(filepath.Join(dir, p)); return err == nil }

	t.Run("fast-forward skips after_rebase", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		dir, pts := marks(f)
		f.hooksXDG("land", pts, "")
		want(t, f.run(f.land()), outcome.Landed, 0)
		if exists(dir, "after_rebase") || !exists(dir, "before_move") || !exists(dir, "after") {
			t.Fatal("wrong hook points ran")
		}
	})
	t.Run("a moved base runs all of them", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.commit(f.repo.Dir, "m.txt", "m\n")
		dir, pts := marks(f)
		f.hooksXDG("land", pts, "")
		want(t, f.run(f.land()), outcome.Landed, 0)
		for _, p := range []string{"after_rebase", "before_move", "after"} {
			if !exists(dir, p) {
				t.Errorf("%s did not run", p)
			}
		}
	})
	t.Run("after_rebase failure stops there", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.commit(f.repo.Dir, "m.txt", "m\n")
		f.hooksXDG("land", map[string]string{"after_rebase": f.hookScript("exit 1")}, "")
		o := f.run(f.land())
		f.wantStopped(o, outcome.StopHookFailed)
		if o.Stage != "after_rebase" {
			t.Errorf("stage = %s", o.Stage)
		}
	})
	t.Run("an after hook failure is only a warning", func(t *testing.T) {
		f := newFx(t)
		f.feature()
		f.hooksXDG("land", map[string]string{"after": f.hookScript("exit 1")}, "")
		o := f.run(f.land())
		want(t, o, outcome.Landed, 0)
		if len(o.Warnings) != 1 || o.Warnings[0].Warning != outcome.WarnAfterHookFailed {
			t.Fatalf("warnings = %+v", o.Warnings)
		}
		if f.tip(f.main) != o.LandedCommit {
			t.Fatal("the move did not happen")
		}
		f.untouched()
	})
}

func TestRunAutofixCommitIsInTheResult(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript("echo fixed > x.txt")}, "")
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if f.mainFile("x.txt") != "fixed" || f.tip(f.main) != out.LandedCommit {
		t.Fatalf("x.txt = %q", f.mainFile("x.txt"))
	}
	if len(out.Autofix) != 1 || len(out.Autofix[0].Files) != 1 || out.Autofix[0].Files[0] != "x.txt" || out.Stats.AutofixCommits != 1 {
		t.Fatalf("autofix = %+v stats %+v", out.Autofix, out.Stats)
	}
	if got := f.git(f.repo.Dir, "log", "-1", "--format=%s", f.main); got != "style: apply before_move fixes made during land" {
		t.Fatalf("subject = %q", got)
	}
}

// raceHook commits to TARGET in the destination worktree: once, or on every run.
func (f *fx) raceHook(always bool) string {
	marker := filepath.Join(f.t.TempDir(), "raced")
	guard := `if [ ! -e ` + marker + ` ]; then touch ` + marker + `; else exit 0; fi`
	if always {
		guard = ""
	}
	return f.hookScript(guard + `
git -c user.name=t -c user.email=t@example.invalid -C "$PG_GIT_LANDER_DESTINATION_WORKTREE" commit --allow-empty -qm race`)
}

func TestRunTargetMovedOnceRetriesAndKeepsResolutions(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	res := f.hookScript(`printf 'merged\n' > "$PG_GIT_LANDER_PATH"`)
	hook := f.raceHook(false)
	f.cfgXDG("[defaults.hooks]\nland.before_move = [{ run = ['" + hook + "'] }]\n[[defaults.conflict]]\nglob = \"x.txt\"\nresolve = ['" + res + "']\n")
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if out.Stats.RaceRetries != 1 || out.Stats.ConflictRounds != 1 || len(out.Resolved) != 1 {
		t.Fatalf("stats %+v resolved %+v", out.Stats, out.Resolved)
	}
	if f.mainFile("x.txt") != "merged" {
		t.Fatal("the resolution was lost")
	}
	subj := f.logSubjects(f.main)
	if subj[0] != "add_x.txt" || subj[1] != "race" {
		t.Fatalf("history = %v", subj)
	}
	evs := f.events()
	rr := lastEvent(evs, "race_retry")
	if rr == nil || rr["attempt"].(float64) != 1 || rr["old_target_tip"] == rr["new_target_tip"] {
		t.Fatalf("race_retry = %v", rr)
	}
	hooks := 0
	for _, e := range evs {
		if e["event"] == "hook" && e["point"] == "before_move" {
			hooks++
		}
	}
	if hooks != 2 {
		t.Errorf("before_move ran %d times, want 2", hooks)
	}
	if e := lastEvent(evs, "op_end"); e == nil || e["outcome"] != "landed" {
		t.Fatalf("op_end = %v", e)
	}
}

func TestRunTargetMovingOnEveryTryStopsWithRace(t *testing.T) {
	f := newFx(t)
	f.feature()
	featTip := f.tip("feature/x")
	f.hooksXDG("land", map[string]string{"before_move": f.raceHook(true)}, "")
	out := f.run(f.land())
	f.wantStopped(out, outcome.StopRace)
	if out.Stats.RaceRetries != config.DefaultMaxRaceRetries {
		t.Fatalf("race_retries = %d", out.Stats.RaceRetries)
	}
	if f.tip("feature/x") != featTip {
		t.Fatal("BRANCH moved")
	}
	for _, s := range f.logSubjects(f.main) {
		if s == "add_x.txt" {
			t.Fatal("the result was landed")
		}
	}
	if n := countEvents(f.events(), "race_retry"); n != config.DefaultMaxRaceRetries {
		t.Errorf("%d race_retry events", n)
	}
}

func TestRunKeepFlagsKeepTheSourceWorktreeAndMoveTheBranch(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.commit(f.repo.Dir, "m.txt", "m\n")
	req := f.land()
	req.KeepSourceWorktree, req.KeepSourceBranch = true, true
	out := f.run(req)
	want(t, out, outcome.Landed, 0)
	if out.BranchFate != outcome.BranchMoved || out.WorktreeFate == nil || out.WorktreeFate.Fate != outcome.WorktreeKept {
		t.Fatalf("fates = %s %+v", out.BranchFate, out.WorktreeFate)
	}
	if f.tip("feature/x") != out.LandedCommit {
		t.Fatal("BRANCH is not at the result")
	}
	if _, err := os.Stat(f.wt); err != nil {
		t.Fatal("the source worktree is gone")
	}
	if got := f.git(f.wt, "branch", "--show-current"); got != "feature/x" {
		t.Fatalf("source worktree on %q", got)
	}
	f.untouched()
}

func TestRunRemovesTheSourceWorktreeByDefault(t *testing.T) {
	f := newFx(t)
	f.feature()
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if _, err := os.Stat(f.wt); err == nil {
		t.Fatal("the source worktree was kept")
	}
	if out.WorktreeFate == nil || out.WorktreeFate.Fate != outcome.WorktreeRemoved {
		t.Fatalf("fate = %+v", out.WorktreeFate)
	}
}

func TestRunWorkInDestinationWarningIsAttached(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.git(f.repo.Dir, "worktree", "remove", f.wt) // BRANCH is checked out nowhere
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	found := false
	for _, w := range out.Warnings {
		found = found || w.Warning == outcome.WarnWorkInDestination
	}
	if !found {
		t.Fatalf("warnings = %+v", out.Warnings)
	}
	if f.mainFile("x.txt") != "x" {
		t.Fatal("not landed")
	}
	f.untouched()
}

func TestRunGitErrorOfTheRebaseIsAStop(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.commit(f.repo.Dir, "m.txt", "m\n")
	script := "#!/bin/sh\necho 'no rebasing today' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(f.repo.Root(), "hooks", "pre-rebase"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f.wantStopped(f.run(f.land()), outcome.StopGitError)
}

func TestRunRebaseBranchMovedDuringTheOperationIsAStop(t *testing.T) {
	f := newFx(t)
	f.feature()
	mainTip := f.commit(f.repo.Dir, "m.txt", "m\n")
	hook := f.hookScript(`git update-ref refs/heads/feature/x ` + mainTip)
	f.hooksXDG("rebase", map[string]string{"before_move": hook}, "")
	o := f.run(f.rebaseReq())
	f.wantStopped(o, outcome.StopBranchMoved)
	if f.tip("feature/x") != mainTip {
		t.Fatal("the tool overwrote a BRANCH that moved")
	}
}

func TestRunUnexpectedFailureAfterLockingIsAnInternalErrorStop(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript(`rm -rf "$PG_GIT_LANDER_WORK_WORKTREE"`)}, "")
	o := f.run(f.land())
	f.wantStopped(o, outcome.StopInternalError)
	if len(o.Next) != 1 || o.Next[0] != "pg-git-lander land feature/x --abort" {
		t.Fatalf("next = %v", o.Next)
	}
}

func TestRunSwappedLockRecordGivesLockLost(t *testing.T) {
	f := newFx(t)
	f.feature()
	mainTip, featTip := f.tip(f.main), f.tip("feature/x")
	hook := f.hookScript(`b=$(printf '{}' | git hash-object -w --stdin)
git update-ref ` + locks.BranchRef(f.main) + ` "$b"`)
	f.hooksXDG("land", map[string]string{"before_move": hook}, "")
	o := f.run(f.land())
	want(t, o, outcome.Stopped, 4)
	if o.Reason != string(outcome.StopLockLost) {
		t.Fatalf("reason = %s (%s)", o.Reason, o.Message)
	}
	if f.tip(f.main) != mainTip || f.tip("feature/x") != featTip {
		t.Fatal("a move went ahead on a swapped lock record")
	}
	if !strings.Contains(o.Message, locks.BranchRef(f.main)) {
		t.Errorf("message = %q", o.Message)
	}
}

func TestRunStaleRenewalGivesLockLost(t *testing.T) {
	f := newFx(t)
	f.feature()
	mainTip := f.tip(f.main)
	p := f.prepared(f.land())
	f.clock.Advance(config.DefaultLeaseTTL) // no renewal has happened since
	o := f.complete(p)
	want(t, o, outcome.Stopped, 4)
	if o.Reason != string(outcome.StopLockLost) {
		t.Fatalf("reason = %s (%s)", o.Reason, o.Message)
	}
	if f.tip(f.main) != mainTip || !f.refExists("refs/heads/feature/x") {
		t.Fatal("a move went ahead on a stale renewal")
	}
	if r := f.record(locks.BranchRef("feature/x")); r["state"] != "stopped" {
		t.Fatalf("BRANCH record = %v", r)
	}
}

func TestRunEventsOfALandedRun(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.commit(f.repo.Dir, "m.txt", "m\n")
	dir := t.TempDir()
	f.hooksXDG("land", map[string]string{
		"after_rebase": f.hookScript("touch " + filepath.Join(dir, "a")),
		"before_move":  f.hookScript("true"),
		"after":        f.hookScript("true"),
	}, "")
	want(t, f.run(f.land()), outcome.Landed, 0)
	evs := f.events()
	var stages []string
	hookPoints := map[string]bool{}
	for _, e := range evs {
		switch e["event"] {
		case "stage":
			stages = append(stages, e["stage"].(string))
		case "hook":
			hookPoints[e["point"].(string)] = true
		}
	}
	if got := strings.Join(stages, ","); got != "locked,preparing,rebasing,after_rebase,before_move,moving,after,cleanup" {
		t.Fatalf("stages = %s", got)
	}
	for _, p := range []string{"after_rebase", "before_move", "after"} {
		if !hookPoints[p] {
			t.Errorf("no hook event for %s", p)
		}
	}
	last := evs[len(evs)-1]
	if last["event"] != "op_end" || last["outcome"] != "landed" || last["path_taken"] != "rebase" {
		t.Fatalf("last event = %v", last)
	}
	if st, _ := last["stats"].(map[string]any); st == nil || st["rebased_commits"].(float64) != 1 {
		t.Fatalf("op_end stats = %v", last["stats"])
	}
}

func TestRunDoneOutcomeSurvivesJSON(t *testing.T) {
	f := newFx(t)
	f.feature()
	out := f.run(f.land())
	b, err := out.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"path_taken", "stats", "dropped_commits", "resolved", "autofix", "skipped_hooks", "landed_commit", "warnings"} {
		if _, ok := m[k]; !ok {
			t.Errorf("outcome lacks %s", k)
		}
	}
	_ = context.Background
	_ = time.Second
}
