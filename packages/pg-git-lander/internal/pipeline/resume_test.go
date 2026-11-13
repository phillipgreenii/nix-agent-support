package pipeline_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/worktrees"
)

// Tests of the resume operations (design section 7.3): real git, real hook
// scripts, real lock refs.

func (f *fx) resumeReq(flag string, op worktrees.Operation, branch string) pipeline.ResumeRequest {
	return pipeline.ResumeRequest{Flag: flag, Operation: op, Branch: branch, Dir: f.repo.Dir}
}

func (f *fx) resumeWith(req pipeline.ResumeRequest) *outcome.Outcome {
	f.t.Helper()
	o, err := pipeline.Resume(f.t.Context(), f.deps(owner(50001, "r1")), req)
	if err != nil {
		f.t.Fatalf("Resume: %v", err)
	}
	if o == nil {
		f.t.Fatal("Resume returned no outcome")
	}
	return o
}

func (f *fx) resume(flag string) *outcome.Outcome {
	f.t.Helper()
	return f.resumeWith(f.resumeReq(flag, worktrees.Land, "feature/x"))
}

func (f *fx) wantRefusedOutcome(o *outcome.Outcome, reason outcome.RefusalReason, detail string) {
	f.t.Helper()
	if o.Outcome != outcome.Refused || o.Reason != string(reason) || o.Detail != detail || o.Exit != outcome.ExitRefused {
		f.t.Fatalf("want refused %s (%s), got %s %q detail %q: %s", reason, detail, o.Outcome, o.Reason, o.Detail, o.Message)
	}
}

// branchOID is the blob oid the BRANCH lock ref points at.
func (f *fx) branchOID() string {
	return f.git(f.repo.Dir, "rev-parse", locks.BranchRef("feature/x"))
}

// wantStillStopped asserts the stopped record is exactly as it was.
func (f *fx) wantStillStopped(oid string) {
	f.t.Helper()
	if got := f.branchOID(); got != oid {
		f.t.Fatalf("the stopped record changed: %s -> %s", oid, got)
	}
	if r := f.record(locks.BranchRef("feature/x")); r["state"] != "stopped" {
		f.t.Fatalf("record state = %v", r["state"])
	}
}

// forge rewrites the BRANCH record, standing in for a crash at a given point.
func (f *fx) forge(mutate func(rec map[string]any)) {
	f.t.Helper()
	ref := locks.BranchRef("feature/x")
	rec := f.record(ref)
	mutate(rec)
	b, err := json.Marshal(rec)
	if err != nil {
		f.t.Fatal(err)
	}
	r := f.g.RunInput(f.t.Context(), f.repo.Dir, append(b, '\n'), "hash-object", "-w", "--stdin")
	if !r.OK() {
		f.t.Fatalf("hash-object: %s", r.Stderr)
	}
	f.git(f.repo.Dir, "update-ref", ref, strings.TrimSpace(string(r.Stdout)))
}

// restoredState asserts every worktree is where the operation found it.
func (f *fx) wantStartState() {
	f.t.Helper()
	if got := f.git(f.wt, "symbolic-ref", "--short", "HEAD"); got != "feature/x" {
		f.t.Errorf("source worktree on %s", got)
	}
	if got := f.git(f.repo.Dir, "symbolic-ref", "--short", "HEAD"); got != f.main {
		f.t.Errorf("main worktree on %s", got)
	}
}

func TestResumeAbortBeforeTheMoveRestoresTheStartState(t *testing.T) {
	failing := func(f *fx, point string, body string) {
		f.hooksXDG("land", map[string]string{point: f.hookScript(body)}, "")
	}
	cases := []struct {
		name   string
		reason outcome.StopReason
		setup  func(f *fx)
	}{
		{"conflict", outcome.StopConflict, func(f *fx) { f.conflicting() }},
		{"hook-failed at before_move", outcome.StopHookFailed, func(f *fx) { f.feature(); failing(f, "before_move", "exit 1") }},
		{"hook-failed at after_rebase", outcome.StopHookFailed, func(f *fx) {
			f.feature()
			f.commit(f.repo.Dir, "m.txt", "m\n")
			failing(f, "after_rebase", "exit 1")
		}},
		{"transient", outcome.StopTransient, func(f *fx) { f.feature(); failing(f, "before_move", "exit 75") }},
		{"race", outcome.StopRace, func(f *fx) {
			f.feature()
			f.hooksXDG("land", map[string]string{"before_move": f.raceHook(true)}, "")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			c.setup(f)
			mainTip, featTip := f.tip(f.main), f.tip("feature/x")
			stopped := f.run(f.land())
			f.wantStopped(stopped, c.reason)
			mainBefore := f.tip(f.main) // the race hook moves main on purpose

			o := f.resume(outcome.FlagAbort)
			want(t, o, outcome.Aborted, 0)
			if o.MovedAlready == nil || *o.MovedAlready {
				t.Errorf("moved_already = %v", o.MovedAlready)
			}
			if f.tip("feature/x") != featTip || (c.reason != outcome.StopRace && f.tip(f.main) != mainTip) || f.tip(f.main) != mainBefore {
				t.Fatal("BRANCH or TARGET moved")
			}
			f.untouched()
			f.wantStartState()
			if len(o.Restored) == 0 {
				t.Errorf("restored = %v", o.Restored)
			}
			if e := lastEvent(f.events(), "op_end"); e == nil || e["outcome"] != "aborted" {
				t.Errorf("op_end = %v", e)
			}
		})
	}
}

func TestResumeAbortRepeatsRepoFlagInNothingNext(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	req := f.land()
	req.RepoFlag = f.repo.Dir
	out := f.run(req)
	f.wantStopped(out, outcome.StopConflict)
	// A refusal after a stop names the next commands with --repo when it was used.
	r := f.resumeReq(outcome.FlagContinue, worktrees.Land, "feature/x")
	r.RepoFlag = f.repo.Dir
	o := f.resumeWith(r)
	f.wantRefusedOutcome(o, outcome.RefCannotResume, outcome.DetailUnresolved)
	for _, n := range o.Next {
		if !strings.HasSuffix(n, "--repo "+f.repo.Dir) {
			t.Errorf("next %q lacks --repo", n)
		}
	}
}

// afterTheMove forges a crash right after the fast-forward of TARGET: the
// stage is moving, the intent is written and TARGET already holds the result.
func (f *fx) afterTheMove(stage string) (result string) {
	f.t.Helper()
	out := f.run(f.landWithWork())
	f.wantStopped(out, outcome.StopHookFailed)
	result = f.git(f.repo.Dir, "rev-parse", "refs/heads/"+out.WorkBranch)
	f.git(f.repo.Dir, "merge", "-q", "--ff-only", result)
	f.forge(func(r map[string]any) {
		r["stage"] = stage
		r["move"] = map[string]any{"intent": "moving", "expected_result": result}
		r["stop"].(map[string]any)["reason"] = "interrupted"
	})
	return result
}

// landWithWork lands feature/x with a work worktree of its own, so the source
// worktree can go while the work worktree stays.
func (f *fx) landWithWork() pipeline.Request {
	f.t.Helper()
	ww := filepath.Join(f.repo.Root(), "wt-w")
	f.git(f.repo.Dir, "worktree", "add", "-q", "--detach", ww)
	r, err := filepath.EvalSymlinks(ww)
	if err != nil {
		f.t.Fatal(err)
	}
	req := f.land()
	req.WorkWorktree = r
	return req
}

func (f *fx) failingBeforeMove(marker string) {
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript(
		`if [ ! -e ` + marker + ` ]; then exit 1; fi`,
	)}, "")
}

func TestResumeAbortAfterTheMoveOnlyFinishesTheCleanup(t *testing.T) {
	for _, stage := range []string{"moving", "after", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			f := newFx(t)
			f.feature()
			f.failingBeforeMove(filepath.Join(t.TempDir(), "never"))
			result := f.afterTheMove(stage)

			o := f.resume(outcome.FlagAbort)
			want(t, o, outcome.Aborted, 0)
			if o.MovedAlready == nil || !*o.MovedAlready {
				t.Fatalf("moved_already = %v", o.MovedAlready)
			}
			if f.tip(f.main) != result {
				t.Fatal("the abort undid the move")
			}
			if f.refExists("refs/heads/feature/x") {
				t.Error("BRANCH was not retired by the finished clean-up")
			}
			f.untouched()
		})
	}
}

func TestResumeAbortOfMoveUnverifiedLeavesTheBranch(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.failingBeforeMove(filepath.Join(t.TempDir(), "never"))
	out := f.run(f.landWithWork())
	f.wantStopped(out, outcome.StopHookFailed)
	mainTip, featTip := f.tip(f.main), f.tip("feature/x")
	f.forge(func(r map[string]any) {
		r["stage"] = "moving"
		r["move"] = map[string]any{"intent": "moving", "expected_result": f.git(f.repo.Dir, "rev-parse", "refs/heads/"+out.WorkBranch)}
		r["stop"].(map[string]any)["reason"] = "move-unverified"
	})
	o := f.resume(outcome.FlagAbort)
	want(t, o, outcome.Aborted, 0)
	if o.MovedAlready == nil || *o.MovedAlready {
		t.Errorf("moved_already = %v", o.MovedAlready)
	}
	if f.tip(f.main) != mainTip || f.tip("feature/x") != featTip {
		t.Fatal("BRANCH or TARGET moved")
	}
	f.untouched()
	f.wantStartState()
}

func TestResumeContinueAfterTheMoveRunsTheAfterHookAndCleansUp(t *testing.T) {
	for _, stage := range []string{"moving", "after", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			f := newFx(t)
			f.feature()
			mark := filepath.Join(t.TempDir(), "after-ran")
			f.hooksXDG("land", map[string]string{
				"before_move": f.hookScript("exit 1"),
				"after":       f.hookScript("touch " + mark),
			}, "")
			result := f.afterTheMove(stage)
			o := f.resume(outcome.FlagContinue)
			want(t, o, outcome.Landed, 0)
			if o.LandedCommit != result || f.tip(f.main) != result {
				t.Fatalf("landed %s, main %s, want %s", o.LandedCommit, f.tip(f.main), result)
			}
			_, err := os.Stat(mark)
			if ran := err == nil; ran != (stage != "cleanup") {
				t.Errorf("after hook ran = %v at stage %s", ran, stage)
			}
			f.untouched()
		})
	}
}

func TestResumeContinueWithTheSourceWorktreeAlreadyRemoved(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript("exit 1")}, "")
	result := f.afterTheMove("after")
	f.git(f.repo.Dir, "worktree", "remove", "--force", f.wt) // the source worktree: already gone
	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	if o.LandedCommit != result {
		t.Fatalf("landed %s", o.LandedCommit)
	}
	f.untouched()
}

func TestResumeQuitDeletesTheRecordsAndLeavesEverythingElse(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	work := stopped.WorkWorktree.Path
	mainTip, featTip := f.tip(f.main), f.tip("feature/x")

	o := f.resume(outcome.FlagQuit)
	want(t, o, outcome.Quit, 0)
	if s := f.lockRefs(); s != "" {
		t.Errorf("lock refs left: %s", s)
	}
	if !strings.Contains(f.workBranches(), stopped.WorkBranch) {
		t.Error("the work branch was deleted")
	}
	if _, err := os.Stat(filepath.Join(f.git(work, "rev-parse", "--path-format=absolute", "--git-dir"), "rebase-merge")); err != nil {
		t.Errorf("git's rebase state was dropped: %v", err)
	}
	if f.tip(f.main) != mainTip || f.tip("feature/x") != featTip {
		t.Fatal("BRANCH or TARGET moved")
	}
	for _, want := range []string{work, "git rebase --continue", "git rebase --abort", "git switch -C feature/x " + stopped.WorkBranch} {
		if !strings.Contains(o.Hint, want) {
			t.Errorf("hint lacks %q: %s", want, o.Hint)
		}
	}
	if e := lastEvent(f.events(), "op_end"); e == nil || e["outcome"] != "quit" {
		t.Errorf("op_end = %v", e)
	}
	// The tool no longer tracks it.
	if o2 := f.resume(outcome.FlagAbort); o2.Reason != string(outcome.RefNothingStopped) {
		t.Errorf("second resume = %s %s", o2.Outcome, o2.Reason)
	}
}

// One fixture serves all four flags: a lost compare-and-swap refuses and leaves
// the record exactly as the winner wrote it, so the next flag meets the same
// state. (Four fixtures cost four full land runs for no extra coverage.)
func TestResumeEveryFlagTakesTheRecordByCompareAndSwap(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	f.wantStopped(f.run(f.land()), outcome.StopConflict)
	// Another invocation took the record first: it is running now.
	l := &locks.Layer{Git: f.g, Clock: f.clock, Dir: f.repo.Dir, Config: locks.Config{LeaseTTL: 60e9, Wait: 3e9}}
	if _, _, err := l.TakeBack(t.Context(), locks.BranchRef("feature/x"), func(r *locks.Record) { r.Owner = owner(9, "other") }); err != nil {
		t.Fatal(err)
	}
	before := f.branchOID()
	for _, flag := range []string{outcome.FlagContinue, outcome.FlagSkip, outcome.FlagAbort, outcome.FlagQuit} {
		o := f.resume(flag)
		f.wantRefusedOutcome(o, outcome.RefLostToConcurrent, "")
		if f.branchOID() != before {
			t.Fatalf("%s: the loser changed the record", flag)
		}
	}
}

func TestResumeNothingStoppedAndOperationMismatch(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	o := f.resumeWith(f.resumeReq(outcome.FlagContinue, worktrees.Land, "feature/x"))
	f.wantRefusedOutcome(o, outcome.RefNothingStopped, "")
	if o := f.resumeWith(pipeline.ResumeRequest{Flag: outcome.FlagAbort, Dir: f.repo.Dir}); o.Reason != string(outcome.RefNothingStopped) {
		t.Fatalf("by directory: %+v", o)
	}

	f.wantStopped(f.run(f.land()), outcome.StopConflict)
	oid := f.branchOID()
	o = f.resumeWith(f.resumeReq(outcome.FlagContinue, worktrees.Rebase, "feature/x"))
	f.wantRefusedOutcome(o, outcome.RefOperationMismatch, "")
	if !strings.Contains(strings.Join(o.Next, " "), "pg-git-lander land feature/x --continue") {
		t.Errorf("next = %v", o.Next)
	}
	f.wantStillStopped(oid)
}

func TestFindStoppedByBranchAndByWorkWorktree(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	d := f.deps(owner(50001, "r1"))

	s, err := pipeline.FindStopped(t.Context(), d, f.repo.Dir, "feature/x")
	if err != nil || s == nil || s.Operation != "land" || s.OpID != stopped.OpID || s.StopReason != "conflict" {
		t.Fatalf("by branch: %+v %v", s, err)
	}
	s, err = pipeline.FindStopped(t.Context(), d, stopped.WorkWorktree.Path, "")
	if err != nil || s == nil || s.Branch != "feature/x" {
		t.Fatalf("by work worktree: %+v %v", s, err)
	}
	if s, _ := pipeline.FindStopped(t.Context(), d, f.repo.Dir, ""); s != nil {
		t.Errorf("the main worktree is not a work worktree: %+v", s)
	}
	if s, _ := pipeline.FindStopped(t.Context(), d, f.repo.Dir, "other"); s != nil {
		t.Errorf("other branch: %+v", s)
	}
}

func TestResumeContinueAfterAManualResolution(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	work := stopped.WorkWorktree.Path
	if err := os.WriteFile(filepath.Join(work, "x.txt"), []byte("by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "x.txt")

	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	if f.mainFile("x.txt") != "by hand" {
		t.Fatalf("x.txt = %q", f.mainFile("x.txt"))
	}
	if o.Stats.ConflictRounds != 1 {
		t.Errorf("conflict_rounds = %d: the bound covers the whole operation", o.Stats.ConflictRounds)
	}
	f.untouched()
}

func TestResumeContinueAfterTheCallerRanGitByHand(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	work := stopped.WorkWorktree.Path
	if err := os.WriteFile(filepath.Join(work, "x.txt"), []byte("by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "x.txt")
	f.git(work, "-c", "core.editor=true", "rebase", "--continue") // finishes git's rebase

	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	if f.mainFile("x.txt") != "by hand" {
		t.Fatalf("x.txt = %q", f.mainFile("x.txt"))
	}
	f.untouched()
}

func TestResumeContinueCannotResumeUnresolvedAndMarkersLeft(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	work := stopped.WorkWorktree.Path
	oid := f.branchOID()

	o := f.resume(outcome.FlagContinue)
	f.wantRefusedOutcome(o, outcome.RefCannotResume, outcome.DetailUnresolved)
	if len(o.Paths) != 1 || o.Paths[0] != "x.txt" {
		t.Errorf("paths = %v", o.Paths)
	}
	f.wantStillStopped(oid)

	// Staged with the markers still in it: git rebase --continue would accept it.
	f.git(work, "add", "x.txt")
	o = f.resume(outcome.FlagContinue)
	f.wantRefusedOutcome(o, outcome.RefCannotResume, outcome.DetailMarkersLeft)
	f.wantStillStopped(oid)

	// A stopped record is still resolvable afterwards.
	if err := os.WriteFile(filepath.Join(work, "x.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "x.txt")
	want(t, f.resume(outcome.FlagContinue), outcome.Landed, 0)
}

func TestResumeContinueRebaseStateMissingAndRestartedRebase(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	work := stopped.WorkWorktree.Path
	f.git(work, "rebase", "--abort") // aborted by hand: the tip is tips.branch again

	// The config changed meanwhile: a resolver now exists.
	f.resolver()
	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	if o.ConfigChanged == nil || !*o.ConfigChanged {
		t.Errorf("config_changed = %v", o.ConfigChanged)
	}
	found := false
	for _, w := range o.Warnings {
		found = found || w.Warning == outcome.WarnConfigChanged
	}
	if !found {
		t.Errorf("warnings = %+v", o.Warnings)
	}
	if f.mainFile("x.txt") != "merged" {
		t.Fatalf("x.txt = %q", f.mainFile("x.txt"))
	}
	f.untouched()

	// A rebase that is neither running nor finished in a way the record explains.
	g := newFx(t)
	g.conflicting()
	st := g.run(g.land())
	g.wantStopped(st, outcome.StopConflict)
	g.git(st.WorkWorktree.Path, "rebase", "--abort")
	g.git(st.WorkWorktree.Path, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "--allow-empty", "-qm", "stray")
	oid := g.branchOID()
	g.wantRefusedOutcome(g.resume(outcome.FlagContinue), outcome.RefCannotResume, outcome.DetailRebaseStateMissing)
	g.wantStillStopped(oid)
}

func TestResumeContinueWorktreeMismatchAndAFixOnTop(t *testing.T) {
	f := newFx(t)
	f.feature()
	marker := filepath.Join(t.TempDir(), "fixed")
	f.failingBeforeMove(marker)
	stopped := f.run(f.landWithWork())
	f.wantStopped(stopped, outcome.StopHookFailed)
	work := stopped.WorkWorktree.Path
	oid := f.branchOID()

	// The work branch no longer holds the recorded result.
	f.git(work, "reset", "-q", "--hard", f.tip(f.main))
	f.wantRefusedOutcome(f.resume(outcome.FlagContinue), outcome.RefCannotResume, outcome.DetailWorktreeMismatch)
	f.wantStillStopped(oid)

	// The caller commits a fix on top of the result: the hook point runs again.
	f.git(work, "reset", "-q", "--hard", f.tip("feature/x"))
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fix := f.commit(work, "fix.txt", "fix\n")
	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	if f.tip(f.main) != fix {
		t.Fatalf("main = %s, want the fix %s", f.tip(f.main), fix)
	}
	f.untouched()
}

func TestResumeContinueAfterAHolderDiedAtPreparing(t *testing.T) {
	f := newFx(t)
	f.feature()
	featTip := f.tip("feature/x")
	f.prepared(f.land()) // pid 41721 is not alive: another run converts it
	res, err := pipeline.Start(t.Context(), f.deps(owner(7, "t2")), pipeline.Request{
		Operation: worktrees.Rebase, Branch: "feature/x", Onto: f.main, Dir: f.wt,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.wantRefused(res, outcome.RefHolderDied)
	if r := f.record(locks.BranchRef("feature/x")); r["stage"] != "preparing" {
		t.Fatalf("stage = %v", r["stage"])
	}
	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	if f.tip(f.main) != featTip {
		t.Fatalf("main = %s", f.tip(f.main))
	}
	f.untouched()
}

// A resume at stage moving takes the destination worktree back at the
// resume: a holder of it is a busy exit, and the record stays stopped.
func TestResumeContinueAfterABusyResume(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.writeXDG("[defaults]\nmax_race_retries = 0\n[defaults.lock]\nwait = \"3s\"\n[defaults.hooks]\nland.before_move = [{ run = ['" + f.raceHook(true) + "'] }]\n")
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopRace)
	if stopped.Stage != "moving" {
		t.Fatalf("stage = %s", stopped.Stage)
	}
	oid := f.branchOID()

	// Somebody holds the main worktree (the destination) with a live lease.
	f.foreignLock(locks.WorktreeRef("_main"), "worktree")

	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Busy, 9)
	f.wantStillStopped(oid) // a resume never strands a record in running

	f.git(f.repo.Dir, "update-ref", "-d", locks.WorktreeRef("_main"))
	f.writeXDG("[defaults.lock]\nwait = \"3s\"\n")
	want(t, f.resume(outcome.FlagContinue), outcome.Landed, 0)
	f.untouched()
}

func TestResumeSkipDropsTheReplayedCommit(t *testing.T) {
	f := newFx(t)
	mainTip, _ := f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	o := f.resume(outcome.FlagSkip)
	// The only commit was dropped: nothing is left to land.
	want(t, o, outcome.NothingToLand, 0)
	if len(o.Dropped) != 1 {
		t.Fatalf("dropped = %v", o.Dropped)
	}
	if f.tip(f.main) != mainTip {
		t.Fatal("main moved")
	}
	f.untouched()
}

func TestResumeSkipIsOnlyValidWithGitsRebaseInProgress(t *testing.T) {
	f := newFx(t)
	f.feature()
	f.hooksXDG("land", map[string]string{"before_move": f.hookScript("exit 1")}, "")
	f.wantStopped(f.run(f.land()), outcome.StopHookFailed)
	oid := f.branchOID()
	f.wantRefusedOutcome(f.resume(outcome.FlagSkip), outcome.RefCannotResume, outcome.DetailRebaseStateMissing)
	f.wantStillStopped(oid)
}

func TestResumeAbortWithABrokenRepoFileAndAMissingWorktree(t *testing.T) {
	f := newFx(t)
	_, featTip := f.conflicting()
	stopped := f.run(f.rebaseReq())
	f.wantStopped(stopped, outcome.StopConflict)
	// A rebase reads the repo file from the primary branch's tip, which is
	// broken now: --continue refuses, --abort does not need the file.
	f.commit(f.repo.Dir, ".pg-git-lander.toml", "[[[ not toml\n")
	oid := f.branchOID()
	rebase := func(flag string) *outcome.Outcome {
		return f.resumeWith(f.resumeReq(flag, worktrees.Rebase, "feature/x"))
	}
	if o := rebase(outcome.FlagContinue); o.Outcome != outcome.Refused || o.Reason != string(outcome.RefRepoConfigError) {
		t.Fatalf("continue = %s %s", o.Outcome, o.Reason)
	}
	f.wantStillStopped(oid)

	// And the work worktree (also the source worktree) is gone.
	if err := os.RemoveAll(stopped.WorkWorktree.Path); err != nil {
		t.Fatal(err)
	}
	o := rebase(outcome.FlagAbort)
	want(t, o, outcome.Aborted, 0)
	if o.WorkWorktree == nil || !o.WorkWorktree.Missing {
		t.Fatalf("work worktree = %+v", o.WorkWorktree)
	}
	if f.tip("feature/x") != featTip {
		t.Fatal("BRANCH moved")
	}
	f.untouched()
}

func TestResumeEmitsStageAndOpEndEvents(t *testing.T) {
	f := newFx(t)
	f.conflicting()
	stopped := f.run(f.land())
	f.wantStopped(stopped, outcome.StopConflict)
	work := stopped.WorkWorktree.Path
	if err := os.WriteFile(filepath.Join(work, "x.txt"), []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git(work, "add", "x.txt")
	before := len(f.events())
	want(t, f.resume(outcome.FlagContinue), outcome.Landed, 0)
	evs := f.events()[before:]
	if e := lastEvent(evs, "op_end"); e == nil || e["outcome"] != "landed" {
		t.Fatalf("op_end = %v", e)
	}
	if countEvents(evs, "stage") == 0 {
		t.Errorf("no stage events in %v", eventNames(evs))
	}
	_ = context.Background
}
