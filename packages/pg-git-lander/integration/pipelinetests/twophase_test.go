//go:build integration

package pipelinetests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/x/cmdrun"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/locks"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/outcome"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-git-lander/internal/pipeline"
)

// Tests of the two lock phases of a land (design section 7.1, steps 10 and
// 10a): the hooks run holding only the branch-side locks; TARGET and the
// destination worktree are taken afterwards, for the move.

// twoPhaseXDG writes the XDG config with top-level keys (revalidate,
// max_race_retries) and a before_move hook script.
func (f *fx) twoPhaseXDG(top, beforeMove string) {
	f.t.Helper()
	s := "[defaults]\n" + top + "\n[defaults.lock]\nwait = \"3s\"\n"
	if beforeMove != "" {
		s += "[defaults.hooks]\nland.before_move = [{ run = ['" + beforeMove + "'] }]\n"
	}
	f.writeXDG(s)
}

// recordLocksHook is a hook that writes the lock refs it sees to a file.
func (f *fx) recordLocksHook(extra string) (script, out string) {
	out = filepath.Join(f.t.TempDir(), "locks.seen")
	return f.hookScript(`git for-each-ref --format='%(refname)' refs/pg-git-lander/locks/ >> ` + out + "\n" + extra), out
}

func (f *fx) read(path string) string {
	f.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// foreignLock makes another live operation hold ref in role.
func (f *fx) foreignLock(ref, role string) {
	f.t.Helper()
	f.live.SetAlive(777, "1794132898")
	rec := map[string]any{
		"schema": 1, "role": role, "state": "running", "op_id": "other", "branch": "other-branch",
		"owner": map[string]any{"host": thisHost, "pid": 777, "pid_start": 1794132898, "token": "x", "tool_version": "t"},
		"lease": map[string]any{"renewed_at": f.clock.Now().UTC().Format("2006-01-02T15:04:05Z"), "ttl_s": 600},
	}
	b, _ := json.Marshal(rec)
	h := f.g.RunInput(f.t.Context(), f.repo.Dir, append(b, '\n'), "hash-object", "-w", "--stdin")
	f.git(f.repo.Dir, "update-ref", ref, strings.TrimSpace(string(h.Stdout)))
}

func hookRuns(evs []map[string]any, point string) int {
	n := 0
	for _, e := range evs {
		if e["event"] == "hook" && e["point"] == point {
			n++
		}
	}
	return n
}

func (f *fx) stats(o *outcome.Outcome) map[string]any {
	f.t.Helper()
	e := lastEvent(f.events(), "op_end")
	if e == nil {
		f.t.Fatal("no op_end event")
	}
	st, _ := e["stats"].(map[string]any)
	if st == nil {
		f.t.Fatalf("op_end stats = %v", e["stats"])
	}
	return st
}

func TestHooksRunWithoutTargetOrDestinationLocksAndUnmovedMainFastForwards(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	featTip := f.tip("feature/x")
	script, seen := f.recordLocksHook("")
	f.hooksXDG("land", map[string]string{"before_move": script}, "")
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	got := f.read(seen)
	if !strings.Contains(got, locks.BranchRef("feature/x")) || !strings.Contains(got, locks.WorktreeRef("wt-x")) {
		t.Fatalf("the branch-side locks were not held during the hook:\n%s", got)
	}
	if strings.Contains(got, locks.BranchRef(f.main)) || strings.Contains(got, locks.WorktreeRef("_main")) {
		t.Fatalf("TARGET or the destination worktree was locked during the hook:\n%s", got)
	}
	if out.PathTaken != outcome.PathFastForward || f.tip(f.main) != featTip || out.Stats.RaceRetries != 0 {
		t.Fatalf("path %s, main %s, stats %+v", out.PathTaken, f.tip(f.main), out.Stats)
	}
	if n := countEvents(f.events(), "race_retry"); n != 0 {
		t.Errorf("%d race_retry events on an unmoved main", n)
	}
	st := f.stats(out)
	for _, k := range []string{"target_lock_wait_ms", "target_lock_held_ms"} {
		if _, ok := st[k].(float64); !ok {
			t.Errorf("op_end stats lack %s: %v", k, st)
		}
	}
	f.untouched()
}

func TestOpEndCarriesTheTimeTargetWasHeld(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	d := f.deps(owner(41721, "t1"))
	// The clean-up runs while TARGET is still held: let five seconds pass there.
	d.Chdir = func(string) error { f.clock.Advance(5 * time.Second); return nil }
	res, err := pipeline.Start(f.t.Context(), d, f.land())
	if err != nil || res.Prepared == nil {
		t.Fatalf("Start: %v %+v", err, res.Outcome)
	}
	out := f.complete(res.Prepared)
	want(t, out, outcome.Landed, 0)
	st := f.stats(out)
	if held, _ := st["target_lock_held_ms"].(float64); held < 5000 {
		t.Fatalf("target_lock_held_ms = %v, want at least 5000: %v", st["target_lock_held_ms"], st)
	}
	if wait, _ := st["target_lock_wait_ms"].(float64); wait != 0 {
		t.Errorf("target_lock_wait_ms = %v on a free main", wait)
	}
}

// movingHook commits a file to TARGET in the destination worktree, once.
func (f *fx) movingHook(file, content string) string {
	marker := filepath.Join(f.t.TempDir(), "moved")
	return f.hookScript(`if [ -e ` + marker + ` ]; then exit 0; fi
touch ` + marker + `
printf '` + content + `' > "$PG_GIT_LANDER_DESTINATION_WORKTREE/` + file + `"
git -C "$PG_GIT_LANDER_DESTINATION_WORKTREE" add ` + file + `
git -c user.name=t -c user.email=t@example.invalid -C "$PG_GIT_LANDER_DESTINATION_WORKTREE" commit -qm "moved ` + file + `"`)
}

func TestMovedMainWithDisjointFilesReusesTheHookResultUnderPaths(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	f.twoPhaseXDG(`revalidate = "paths"`, f.movingHook("m.txt", "m\\n"))
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	evs := f.events()
	if n := hookRuns(evs, "before_move"); n != 1 {
		t.Fatalf("before_move ran %d times, want 1 (the result is reused)", n)
	}
	rr := lastEvent(evs, "revalidate_reuse")
	if rr == nil || rr["revalidate"] != "paths" || rr["old_target_tip"] == rr["new_target_tip"] {
		t.Fatalf("revalidate_reuse = %v", rr)
	}
	if n := countEvents(evs, "race_retry"); n != 0 {
		t.Fatalf("%d race_retry events: a reuse is not a retry", n)
	}
	if out.Stats.RaceRetries != 0 || out.PathTaken != outcome.PathRebase {
		t.Fatalf("stats %+v path %s", out.Stats, out.PathTaken)
	}
	if f.mainFile("x.txt") != "x" || f.mainFile("m.txt") != "m" {
		t.Fatal("the landed history lacks a file")
	}
	if m := f.git(f.repo.Dir, "rev-list", "--merges", f.main); m != "" {
		t.Fatalf("merge commits: %s", m)
	}
	f.untouched()
}

// overlapping prepares a shared file the branch changes at the bottom and the
// moving hook at the top: the rebase is clean, but the files overlap.
func (f *fx) overlapping() {
	f.t.Helper()
	f.commit(f.repo.Dir, "shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	f.feature()
	f.commit(f.wt, "shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9 branch\n")
}

func TestMovedMainWithOverlappingFilesRerunsTheHooksOutsideTheLock(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.overlapping()
	moving := f.movingHook("shared.txt", "1 main\\n2\\n3\\n4\\n5\\n6\\n7\\n8\\n9\\n")
	script, seen := f.recordLocksHook(`. ` + moving)
	f.twoPhaseXDG(`revalidate = "paths"`, script)
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	evs := f.events()
	if n := hookRuns(evs, "before_move"); n != 2 {
		t.Fatalf("before_move ran %d times, want 2", n)
	}
	rr := lastEvent(evs, "race_retry")
	if rr == nil || rr["attempt"].(float64) != 1 || countEvents(evs, "revalidate_reuse") != 0 {
		t.Fatalf("race_retry = %v", rr)
	}
	if got := f.read(seen); strings.Contains(got, locks.BranchRef(f.main)) || strings.Contains(got, locks.WorktreeRef("_main")) {
		t.Fatalf("a rerun hook held TARGET or the destination worktree:\n%s", got)
	}
	if got := f.mainFile("shared.txt"); got != "1 main\n2\n3\n4\n5\n6\n7\n8\n9 branch" {
		t.Fatalf("shared.txt = %q", got)
	}
	if out.Stats.RaceRetries != 1 {
		t.Fatalf("stats %+v", out.Stats)
	}
	f.untouched()
}

func TestMovedMainWithNoneReusesEvenForOverlappingFiles(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.overlapping()
	f.twoPhaseXDG(`revalidate = "none"`, f.movingHook("shared.txt", "1 main\\n2\\n3\\n4\\n5\\n6\\n7\\n8\\n9\\n"))
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if n := hookRuns(f.events(), "before_move"); n != 1 {
		t.Fatalf("before_move ran %d times, want 1", n)
	}
	if rr := lastEvent(f.events(), "revalidate_reuse"); rr == nil || rr["revalidate"] != "none" || countEvents(f.events(), "race_retry") != 0 {
		t.Fatalf("revalidate_reuse = %v", rr)
	}
	f.untouched()
}

func TestMovedMainWithAConflictingRebaseFallsBackToTheFullReturn(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	// Same file, same line: not disjoint is not even needed under none, the
	// rebase itself conflicts and the hooks must run again (and the conflict stops).
	f.twoPhaseXDG(`revalidate = "none"`, f.movingHook("x.txt", "main\\n"))
	out := f.run(f.land())
	f.wantStopped(out, outcome.StopConflict)
	if n := hookRuns(f.events(), "before_move"); n != 1 {
		t.Errorf("before_move ran %d times before the conflict", n)
	}
	if got := f.git(f.wt, "status", "--porcelain=v1"); got != "" && !strings.Contains(got, "x.txt") {
		t.Errorf("status = %q", got)
	}
}

func TestMovedMainWithFullRevalidateRerunsEvenWhenDisjoint(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	f.twoPhaseXDG(`revalidate = "full"`, f.movingHook("m.txt", "m\\n"))
	out := f.run(f.land())
	want(t, out, outcome.Landed, 0)
	if n := hookRuns(f.events(), "before_move"); n != 2 {
		t.Fatalf("before_move ran %d times, want 2", n)
	}
	if rr := lastEvent(f.events(), "race_retry"); rr == nil || rr["revalidate"] != "full" {
		t.Fatalf("race_retry = %v", rr)
	}
	f.untouched()
}

func TestRaceBoundStopsRaceWithoutEverHoldingMain(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	f.twoPhaseXDG("max_race_retries = 0", f.raceHook(true))
	out := f.run(f.land())
	f.wantStopped(out, outcome.StopRace)
	if f.refExists(locks.BranchRef(f.main)) || f.refExists(locks.WorktreeRef("_main")) {
		t.Fatalf("main is locked after the stop:\n%s", f.lockRefs())
	}
	st := f.stats(out)
	if held, _ := st["target_lock_held_ms"].(float64); held != 0 {
		t.Errorf("target_lock_held_ms = %v", held)
	}
	// The destination worktree was taken and given back by the one attempt, so
	// its record is gone; the stopped records are the branch-side ones only.
	if refs := f.lockRefs(); strings.Contains(refs, "worktrees/_main") || strings.Contains(refs, "heads/"+f.main) {
		t.Fatalf("lock refs left:\n%s", refs)
	}
}

func TestBusyTargetAfterTheHooksStopsTargetBusyAndLeavesMainFree(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	featTip, mainTip := f.tip("feature/x"), f.tip(f.main)
	script, seen := f.recordLocksHook("")
	f.hooksXDG("land", map[string]string{"before_move": script}, "")
	// Another landing holds main for the whole wait. The first phase does not
	// need it, so the hooks still run.
	f.foreignLock(locks.BranchRef(f.main), "target")

	out := f.run(f.land())
	want(t, out, outcome.Stopped, 21)
	if out.Reason != string(outcome.StopTargetBusy) || out.Retryable == nil || !*out.Retryable {
		t.Fatalf("reason %q retryable %v (%s)", out.Reason, out.Retryable, out.Message)
	}
	for _, w := range []string{"other-branch", "op other", "stage", "for"} {
		if !strings.Contains(out.Message, w) {
			t.Errorf("message lacks %q: %s", w, out.Message)
		}
	}
	if !strings.Contains(strings.Join(out.Next, "\n"), "--continue") {
		t.Errorf("next = %v", out.Next)
	}
	if n := strings.Count(f.read(seen), "\n"); n == 0 {
		t.Fatal("the hook did not run before the lock phase")
	}
	if f.tip(f.main) != mainTip || f.tip("feature/x") != featTip {
		t.Fatal("a ref moved")
	}
	if f.refExists(locks.WorktreeRef("_main")) {
		t.Fatal("the destination worktree was locked by a stopped operation")
	}
	if !f.refExists(locks.BranchRef(f.main)) {
		t.Fatal("the foreign TARGET lock was removed")
	}
	if r := f.record(locks.BranchRef("feature/x")); r["state"] != "stopped" {
		t.Fatalf("BRANCH record = %v", r)
	}
	st := f.stats(out)
	if wait, _ := st["target_lock_wait_ms"].(float64); wait < 3000 {
		t.Errorf("target_lock_wait_ms = %v, want the 3s wait", st["target_lock_wait_ms"])
	}
	if held, _ := st["target_lock_held_ms"].(float64); held != 0 {
		t.Errorf("target_lock_held_ms = %v", held)
	}

	// The holder goes away; --continue goes straight to the lock phase, with
	// no second run of the hooks.
	f.git(f.repo.Dir, "update-ref", "-d", locks.BranchRef(f.main))
	hooksBefore := strings.Count(f.read(seen), "\n")
	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	if got := strings.Count(f.read(seen), "\n"); got != hooksBefore {
		t.Errorf("the hook ran again on --continue after target-busy")
	}
	if f.tip(f.main) != featTip {
		t.Fatalf("main = %s, want %s", f.tip(f.main), featTip)
	}
	f.untouched()
}

func TestResumeOfAStopBeforeTheLockPhaseDoesNotTakeTargetOrDestination(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	failing := f.hookScript("exit 1")
	f.hooksXDG("land", map[string]string{"before_move": failing}, "")
	f.wantStopped(f.run(f.land()), outcome.StopHookFailed)
	if f.refExists(locks.WorktreeRef("_main")) || f.refExists(locks.BranchRef(f.main)) {
		t.Fatalf("a stop before the lock phase left a main-line lock:\n%s", f.lockRefs())
	}

	// A holder of both main-line locks does not block the resume itself.
	script, seen := f.recordLocksHook("")
	f.hooksXDG("land", map[string]string{"before_move": script}, "")
	var during []string
	d := f.deps(owner(50001, "r1"))
	d.Sleep = func(_ context.Context, dur time.Duration) error { f.clock.Advance(dur); return nil }
	o, err := pipeline.Resume(f.t.Context(), d, f.resumeReq(outcome.FlagContinue, "land", "feature/x"))
	if err != nil {
		t.Fatal(err)
	}
	want(t, o, outcome.Landed, 0)
	during = strings.Fields(f.read(seen))
	for _, ref := range during {
		if ref == locks.BranchRef(f.main) || ref == locks.WorktreeRef("_main") {
			t.Fatalf("the resume held %s while the hook ran: %v", ref, during)
		}
	}
	f.untouched()
}

func TestResumeAtStageMovingTakesTargetAndDestinationBack(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	f.twoPhaseXDG("max_race_retries = 0", f.raceHook(true))
	f.wantStopped(f.run(f.land()), outcome.StopRace)
	// With the destination held by someone else the resume is busy: it takes
	// the destination worktree back at stage moving (see also the busy-resume
	// test), where a stop before the lock phase would not.
	f.foreignLock(locks.WorktreeRef("_main"), "worktree")
	want(t, f.resume(outcome.FlagContinue), outcome.Busy, 9)
}

// failGitAfter makes every git sub-command sub fail once marker exists (the moving
// hook creates it), standing in for a git error while the operation holds
// TARGET and the destination worktree at a stage before the move.
func (f *fx) failGitAfter(marker, sub string) {
	f.g.Runner = func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		if _, err := os.Stat(marker); err == nil && len(c.Args) > 0 {
			for _, a := range c.Args {
				if a == sub {
					return cmdrun.Result{Code: 1, Stderr: []byte("fatal: injected git failure\n")}
				}
			}
		}
		return cmdrun.Exec(ctx, c)
	}
}

// A stop while phase two is held, at a stage before the move, leaves the
// operation's own stopped destination record; --continue must take it back
// instead of waiting on itself.
func TestContinueAfterAStopHoldingPhaseTwoBeforeTheMoveDoesNotWaitOnItself(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	marker := filepath.Join(t.TempDir(), "moved")
	hook := f.hookScript(`if [ -e ` + marker + ` ]; then exit 0; fi
touch ` + marker + `
printf 'm\n' > "$PG_GIT_LANDER_DESTINATION_WORKTREE/m.txt"
git -C "$PG_GIT_LANDER_DESTINATION_WORKTREE" add m.txt
git -c user.name=t -c user.email=t@example.invalid -C "$PG_GIT_LANDER_DESTINATION_WORKTREE" commit -qm "moved m.txt"`)
	f.twoPhaseXDG(`revalidate = "paths"`, hook)
	realRunner := f.g.Runner
	f.failGitAfter(marker, "diff")
	out := f.run(f.land())
	if out.Outcome != outcome.Stopped {
		t.Fatalf("outcome = %s %s (%s)", out.Outcome, out.Reason, out.Message)
	}
	if r := f.record(locks.WorktreeRef("_main")); r["state"] != "stopped" {
		t.Fatalf("destination record = %v, want the stopped record of the failed phase two", r)
	}
	f.g.Runner = realRunner

	var slept time.Duration
	d := f.deps(owner(50001, "r1"))
	d.Sleep = func(_ context.Context, dur time.Duration) error { slept += dur; f.clock.Advance(dur); return nil }
	o, err := pipeline.Resume(f.t.Context(), d, f.resumeReq(outcome.FlagContinue, "land", "feature/x"))
	if err != nil {
		t.Fatal(err)
	}
	want(t, o, outcome.Landed, 0)
	if slept != 0 {
		t.Errorf("the resume waited %v on its own stopped record", slept)
	}
	if f.mainFile("x.txt") != "x" || f.mainFile("m.txt") != "m" {
		t.Errorf("feature/x did not land")
	}
	if refs := f.lockRefs(); strings.Contains(refs, "locks/") {
		t.Errorf("lock refs left:\n%s", refs)
	}
	f.untouched()
}

// A lock lost after phase two began still closes the TARGET hold account.
func TestLockLostAfterPhaseTwoBeganRecordsTheHoldTime(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	var once sync.Once
	f.g.Runner = func(ctx context.Context, c cmdrun.Call) cmdrun.Result {
		args := strings.Join(c.Args, " ")
		if strings.Contains(args, "merge --ff-only") ||
			(strings.Contains(args, "update-ref --stdin") && strings.Contains(string(c.Stdin), "refs/heads/"+f.main)) {
			once.Do(func() {
				// TARGET has been held since phase two began; swap BRANCH's
				// record out from under the operation, five seconds later.
				f.clock.Advance(5 * time.Second)
				b := cmdrun.Exec(ctx, cmdrun.Call{Name: "git", Args: []string{"-C", f.repo.Dir, "hash-object", "-w", "--stdin"}, Stdin: []byte("{}\n")})
				f.git(f.repo.Dir, "update-ref", locks.BranchRef("feature/x"), strings.TrimSpace(string(b.Stdout)))
			})
		}
		return cmdrun.Exec(ctx, c)
	}
	out := f.run(f.land())
	if out.Outcome != outcome.Stopped || out.Reason != string(outcome.StopLockLost) {
		t.Fatalf("outcome = %s %s (%s)", out.Outcome, out.Reason, out.Message)
	}
	st := f.stats(out)
	if held, _ := st["target_lock_held_ms"].(float64); held < 5000 {
		t.Fatalf("target_lock_held_ms = %v on a lock-lost stop, want at least 5000", st["target_lock_held_ms"])
	}
}

// onceHook is a before_move hook that runs body in the destination worktree
// the first time only (the later run, after a race return, passes).
func (f *fx) onceHook(body string) string {
	marker := filepath.Join(f.t.TempDir(), "moved")
	return f.hookScript(`if [ -e ` + marker + ` ]; then exit 0; fi
touch ` + marker + `
cd "$PG_GIT_LANDER_DESTINATION_WORKTREE"
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
` + body)
}

func TestContinueAfterTargetBusyThenMovedMainTakesTheFullRaceReturn(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	script, seen := f.recordLocksHook("")
	f.twoPhaseXDG("", script)
	f.foreignLock(locks.BranchRef(f.main), "target")
	out := f.run(f.land())
	if out.Outcome != outcome.Stopped || out.Reason != string(outcome.StopTargetBusy) {
		t.Fatalf("outcome = %s %s (%s)", out.Outcome, out.Reason, out.Message)
	}
	hooksBefore := strings.Count(f.read(seen), "\n")
	// The holder goes away and main moves before the resume.
	f.git(f.repo.Dir, "update-ref", "-d", locks.BranchRef(f.main))
	f.commit(f.repo.Dir, "m.txt", "m\n")

	o := f.resume(outcome.FlagContinue)
	want(t, o, outcome.Landed, 0)
	evs := f.events()
	if rr := lastEvent(evs, "race_retry"); rr == nil || rr["attempt"].(float64) != 1 {
		t.Fatalf("race_retry = %v", rr)
	}
	if n := strings.Count(f.read(seen), "\n"); n <= hooksBefore {
		t.Errorf("the hooks did not run again after the moved main (lines %d, before %d)", n, hooksBefore)
	}
	for _, ref := range strings.Fields(f.read(seen))[hooksBefore:] {
		if ref == locks.BranchRef(f.main) || ref == locks.WorktreeRef("_main") {
			t.Fatalf("a rerun hook held %s", ref)
		}
	}
	if f.mainFile("x.txt") != "x" || f.mainFile("m.txt") != "m" {
		t.Fatal("the landed history lacks a file")
	}
	f.untouched()
}

// pathsReuse runs a land with revalidate = paths whose before_move hook moves
// main with body, after setup; it returns the outcome.
func (f *fx) pathsLand(body string) *outcome.Outcome {
	f.t.Helper()
	f.twoPhaseXDG(`revalidate = "paths"`, f.onceHook(body))
	return f.run(f.land())
}

func TestPathsRevalidateCountsBothNamesOfARenamedFile(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.commit(f.repo.Dir, "a.txt", "1\n2\n3\n")
	f.feature()
	f.commit(f.wt, "a.txt", "1\n2\n3 branch\n")
	o := f.pathsLand("git mv a.txt b.txt\ngit commit -qm rename")
	want(t, o, outcome.Landed, 0)
	evs := f.events()
	if n := hookRuns(evs, "before_move"); n != 2 {
		t.Fatalf("before_move ran %d times, want 2: the old name of the rename overlaps the branch", n)
	}
	if countEvents(evs, "revalidate_reuse") != 0 {
		t.Error("a rename was reused as if disjoint")
	}
	f.untouched()
}

func TestPathsRevalidateReusesAcrossAMergeCommitInTheMovedRange(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	o := f.pathsLand("git switch -q -c side\nprintf 's\\n' > s.txt\ngit add s.txt\ngit commit -qm side\ngit switch -q " + f.main + "\ngit merge -q --no-ff -m merge side")
	want(t, o, outcome.Landed, 0)
	evs := f.events()
	if n := hookRuns(evs, "before_move"); n != 1 || countEvents(evs, "revalidate_reuse") != 1 {
		t.Fatalf("before_move ran %d times, reuse events %d, want 1 and 1", n, countEvents(evs, "revalidate_reuse"))
	}
	if f.mainFile("x.txt") != "x" || f.mainFile("s.txt") != "s" {
		t.Fatal("the landed history lacks a file")
	}
	// Only the moved range's own merge: the tool made none.
	if got := len(strings.Fields(f.git(f.repo.Dir, "rev-list", "--merges", f.main))); got != 1 {
		t.Fatalf("%d merge commits on main, want only the moved range's own", got)
	}
	f.untouched()
}

func TestPathsRevalidateRerunsWhenTheConfigurationChangedInTheMovedRange(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	f.feature()
	o := f.pathsLand("printf 'max_race_retries = 7\\n' > .pg-git-lander.toml\ngit add .pg-git-lander.toml\ngit commit -qm cfg")
	want(t, o, outcome.Landed, 0)
	evs := f.events()
	if n := hookRuns(evs, "before_move"); n != 2 {
		t.Fatalf("before_move ran %d times, want 2: the digest changed", n)
	}
	if countEvents(evs, "revalidate_reuse") != 0 {
		t.Error("a changed configuration was reused")
	}
	f.untouched()
}

func TestPathsRevalidateWithAnEmptyBranchDiffReusesTheHookResult(t *testing.T) {
	t.Parallel()
	f := newFx(t)
	p := filepath.Join(f.repo.Root(), "wt-x")
	f.git(f.repo.Dir, "worktree", "add", "-q", "-b", "feature/x", p)
	f.wt, _ = filepath.EvalSymlinks(p)
	f.git(f.wt, "commit", "-q", "--allow-empty", "-m", "empty")
	o := f.pathsLand("printf 'm\\n' > m.txt\ngit add m.txt\ngit commit -qm moved")
	want(t, o, outcome.Landed, 0)
	evs := f.events()
	if n := hookRuns(evs, "before_move"); n != 1 || countEvents(evs, "revalidate_reuse") != 1 {
		t.Fatalf("before_move ran %d times, reuse events %d, want 1 and 1", n, countEvents(evs, "revalidate_reuse"))
	}
	if f.mainFile("m.txt") != "m" {
		t.Fatal("main lost the moved commit")
	}
	f.untouched()
}
