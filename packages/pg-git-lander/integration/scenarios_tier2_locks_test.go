//go:build integration

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Tier 2 scenarios 20 to 25, 42, 44, 51 and 54 of section 15.3: TARGET moving
// under a landing, concurrent landings, a holder killed at each stage and then
// resumed, stale locks, --quit, an abort after the move and concurrent resume
// flags. Each runs the real binary against real git in a fixture copy; the
// outcome subset, the snapshot and the post-condition must all hold. Races and
// crashes are made deterministic with hook scripts, marker files and FIFOs,
// never with sleeps.

// moveTargetOnce is a hook body that advances main by one commit the first
// time it runs (guarded by a marker) and does nothing after that: a TARGET
// that moves exactly once during the operation.
const moveTargetOnce = `
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
if [ ! -e "$PGL_MARKERS/raced" ]; then
  : > "$PGL_MARKERS/raced"
  echo r > "$PGL_REPO/r.txt"
  git -C "$PGL_REPO" add r.txt
  git -C "$PGL_REPO" commit -q -m "race once"
fi
exit 0
`

// moveTargetAlways advances main by a new commit every time it runs.
const moveTargetAlways = `
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
n=$(cat "$PGL_MARKERS/count" 2>/dev/null || echo 0)
n=$((n+1))
echo $n > "$PGL_MARKERS/count"
echo r$n > "$PGL_REPO/r$n.txt"
git -C "$PGL_REPO" add "r$n.txt"
git -C "$PGL_REPO" commit -q -m "race $n"
exit 0
`

const beforeMoveHookConfig = `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/mover"], timeout = "60s" }]
`

const resolverBeforeMoveConfig = `
[[repo]]
root = "{repo}"

[[repo.conflict]]
glob = "app.txt"
resolve = ["{hooks}/resolver"]

[repo.hooks.land]
before_move = [{ run = ["{hooks}/mover"], timeout = "60s" }]
`

// Scenario 20: TARGET moves once during before_move. The tool returns to the
// rebase for one retry, keeps the resolution made so far (the resolver is not
// asked a second time) and lands on top of the commit that moved TARGET.
func TestTier2TargetMovedOnceDuringBeforeMove(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape:  ShapeConflict,
		Layout: srcOnBr,
		Config: resolverBeforeMoveConfig,
		Hooks: map[string]string{
			"resolver": `printf 'line1\nresolved\nline3\n' > "$PG_GIT_LANDER_PATH"`,
			"mover":    moveTargetOnce,
		},
	})
	r := h.run("land", "feature/x")
	done(t, h, r, Expected{
		Outcome: map[string]any{
			"outcome": "landed", "exit": 0, "branch_fate": "deleted",
			"stats": map[string]any{"race_retries": 1, "conflict_rounds": 1},
		},
		Snapshot: Snapshot{
			Refs:        map[string]string{"refs/heads/feature/x": ""},
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
		},
	})
	if got := h.mustGit(h.repo, "show", "main:app.txt"); got != "line1\nresolved\nline3" {
		t.Errorf("main:app.txt = %q, want the kept resolution", got)
	}
	if got := h.mustGit(h.repo, "show", "main:r.txt"); got != "r" {
		t.Errorf("main:r.txt = %q, want the commit that moved TARGET", got)
	}
	if got := h.mustGit(h.repo, "log", "--format=%s", "main~2..main"); !strings.Contains(got, "feature one") {
		t.Errorf("BRANCH's commit is not on top of the moved TARGET: %q", got)
	}
	if merges := h.mustGit(h.repo, "rev-list", "--merges", "main"); merges != "" {
		t.Errorf("history of main is not linear: %s", merges)
	}
}

// Scenario 21: TARGET moves on every try. The tool gives up at the second
// retry with the stop race, having moved neither BRANCH nor TARGET itself;
// --abort puts everything back.
func TestTier2TargetMovesOnEveryTry(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Config: beforeMoveHookConfig,
		Hooks:  map[string]string{"mover": moveTargetAlways},
	})
	r := h.run("land", "feature/x")
	expect(t, r, 21, "stopped", "race")
	// Three tries (the first and two retries), each moved TARGET once: only
	// the hook moved main, and BRANCH is where the fixture put it.
	if got := h.mustGit(h.repo, "rev-list", "--count", "main"); got != "4" {
		t.Errorf("main has %s commits, want the base plus the 3 commits of the hook (the first try and 2 retries)", got)
	}
	if got := h.mustGit(h.repo, "log", "--format=%s", "main"); strings.Contains(got, "feature") {
		t.Errorf("the tool moved TARGET although it stopped with race: %q", got)
	}
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{"stage": "moving", "stats": map[string]any{"race_retries": 2}},
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/feature/x": "@feature/x"},
		},
		Stopped: true,
	})
	mainAtStop := h.mustGit(h.repo, "rev-parse", "main")

	r = h.run("land", "feature/x", "--abort")
	done(t, h, r, Expected{
		Outcome: map[string]any{"outcome": "aborted", "exit": 0},
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/main": mainAtStop, "refs/heads/feature/x": "@feature/x"},
			Worktrees: map[string]WorktreeState{
				"main": {Head: "branch:main", CheckStatus: true},
				"src":  {Head: "branch:feature/x", CheckStatus: true},
			},
		},
	})
	if got := h.mustGit(h.repo, "for-each-ref", "--format=%(refname)", "refs/heads/pg-git-lander/"); got != "" {
		t.Errorf("work branch left behind: %s", got)
	}
}

// Scenario 22: two lands into the same TARGET started together take turns:
// both land and TARGET's history is linear.
func TestTier2TwoLandsIntoOneTarget(t *testing.T) {
	h := newHarness(t, Scenario{Shape: ShapeAhead})
	h.addBranch("a")
	h.addBranch("b")
	var wg sync.WaitGroup
	results := make([]*result, 2)
	for i, name := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 20; attempt++ {
				p, err := h.startTool([]string{"land", name, "--wait", "60s"})
				if err != nil {
					t.Error(err)
					return
				}
				r, err := h.finish(p)
				if err != nil {
					t.Error(err)
					return
				}
				results[i] = r
				if r.exit != 9 { // busy is retryable
					return
				}
			}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r == nil {
			t.Fatalf("land %d produced no result", i)
		}
		expect(t, r, 0, "landed", "")
	}
	if merges := h.mustGit(h.repo, "rev-list", "--merges", "main"); merges != "" {
		t.Fatalf("history of main is not linear, merges: %s", merges)
	}
	if got := h.mustGit(h.repo, "rev-list", "--count", "main"); got != "3" {
		t.Fatalf("main has %s commits, want the base plus the 2 landed", got)
	}
	files := h.mustGit(h.repo, "ls-tree", "-r", "--name-only", "main")
	for _, f := range []string{"a.txt", "b.txt"} {
		if !strings.Contains(files, f) {
			t.Errorf("%s missing from main's tree: %s", f, files)
		}
	}
	h.noProblems()
}

// crashHolder kills the tool process (a crash) while a blocking hook holds it,
// lets the orphaned hook finish and waits for its process group to go, so the
// next invocation meets a dead holder and nothing is half running.
func crashHolder(t *testing.T, h *harness, p *proc) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	p.killed = true
	<-p.done
	pgid := h.groupOf("hook.pid")
	h.release("go")
	if !groupGone(pgid) {
		t.Fatal("the orphaned hook did not finish after the holder was killed")
	}
}

// crashedLand starts a land of feature/x whose hook at point blocks on its
// first run and crashes the tool there.
func crashedLand(t *testing.T, point string) *harness {
	t.Helper()
	h := newHarness(t, Scenario{
		Shape: ShapeAhead, Fifos: []string{"up", "go"}, Layout: srcOnBr,
		Config: crashConfig(point),
		Hooks:  map[string]string{"blocker": blockingHook},
	})
	p := h.start("holder", "land", "feature/x")
	h.await("up")
	crashHolder(t, h, p)
	return h
}

// Scenario 23: kill -9 at rebasing, at before_move and right after the move,
// then resume. Each completes or aborts cleanly, and a land whose move
// happened is never reported as nothing-to-land.
func TestTier2KillNineThenResume(t *testing.T) {
	t.Run("at-rebasing", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeLockfile, Fifos: []string{"up", "go"}, Layout: srcOnBr})
		h.plantSlowDriver()
		p := h.start("holder", "land", "feature/x")
		h.await("up") // git's merge driver is running inside the rebase
		if err := p.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		p.killed = true
		<-p.done
		pgid := h.groupOf("hook.pid")
		h.release("go")
		if !groupGone(pgid) {
			t.Fatal("git did not finish after the holder was killed")
		}
		expect(t, h.run("land", "feature/x", "--wait", "10s"), 22, "refused", "holder-died")
		done(t, h, h.run("land", "feature/x", "--abort"), Expected{
			Outcome: map[string]any{"outcome": "aborted", "exit": 0},
			Snapshot: Snapshot{
				Refs: startRefs(),
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		})
	})
	t.Run("at-before-move", func(t *testing.T) {
		h := crashedLand(t, "before_move")
		expect(t, h.run("land", "feature/x", "--wait", "10s"), 22, "refused", "holder-died")
		done(t, h, h.run("land", "feature/x", "--continue"), Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted"},
			Snapshot: Snapshot{
				Refs:        mainMoveTo,
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		})
	})
	t.Run("right-after-the-move", func(t *testing.T) {
		h := crashedLand(t, "after")
		// The move happened: main holds BRANCH's commits already.
		if got := h.mustGit(h.repo, "rev-parse", "main"); got != shapes[ShapeAhead].facts.Tips["feature/x"] {
			t.Fatalf("main = %s, want the moved result", got)
		}
		// Not nothing-to-land: the record says the move happened here.
		r := h.run("land", "feature/x", "--wait", "10s")
		expect(t, r, 22, "refused", "holder-died")
		done(t, h, h.run("land", "feature/x", "--continue"), Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0},
			Snapshot: Snapshot{
				Refs:        mainMoveTo,
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		})
	})
}

// Scenario 24: a TARGET-role lock left by a dead holder. The next land of
// another branch takes the TARGET record over at once and finishes; the dead
// holder's own operation is then resolved by an abort.
func TestTier2StaleTargetRoleLockTakenOver(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape: ShapeAhead, Fifos: []string{"up", "go"},
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}, {Name: "spare", Detach: true}},
		Config: crashConfig("before_move"),
		Hooks:  map[string]string{"blocker": blockingHook},
	})
	// TARGET is checked out nowhere and the holder starts in the spare
	// worktree, so no destination worktree is locked: the dead holder leaves a stale TARGET record that a waiter may take over.
	h.mustGit(h.repo, "switch", "-q", "--detach")
	spare := h.wtPath("spare")
	cmd := exec.Command(toolBinary, "land", "feature/x", "--json")
	cmd.Dir, cmd.Env = spare, h.env
	p := &proc{cmd: cmd, out: new(bytes.Buffer), errb: new(bytes.Buffer), done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	h.procs["holder"] = p
	h.await("up")
	crashHolder(t, h, p)
	if refs := h.lockRefs(); !strings.Contains(refs, "locks/heads/main") {
		t.Fatalf("the crash should leave TARGET's record behind: %s", refs)
	}
	h.addBranch("other")
	r := h.run("land", "other", "--wait", "10s") // from the main worktree: the dead holder's spare is not needed
	expect(t, r, 0, "landed", "")
	if got := h.mustGit(h.repo, "log", "--format=%s", "-1", "main"); got != "add other" {
		t.Errorf("main's tip is %q, want the landed other", got)
	}
	if refs := h.lockRefs(); strings.Contains(refs, "locks/heads/main") {
		t.Errorf("TARGET's stale record is still there: %s", refs)
	}
	// What remains is the dead holder's own operation.
	expect(t, runFrom(t, h, spare, "land", "feature/x", "--wait", "10s"), 22, "refused", "holder-died")
	expect(t, runFrom(t, h, spare, "land", "feature/x", "--abort"), 0, "aborted", "")
	h.noProblems()
}

// Scenario 25: --quit after a conflict. The tool's locks and records are
// gone, git's rebase state and the work branch are left for the caller.
func TestTier2QuitAfterConflict(t *testing.T) {
	h := newHarness(t, Scenario{Shape: ShapeConflict, Layout: srcOnBr})
	conflictStop(t, h)
	r := h.run("land", "feature/x", "--quit")
	expect(t, r, 0, "quit", "")
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{"exit": 0},
		Snapshot: Snapshot{
			Refs: startRefs(),
		},
		Stopped: true,
	})
	if refs := h.lockRefs(); refs != "" {
		t.Errorf("locks or records left behind: %s", refs)
	}
	src := h.wtPath("src")
	gitDir := h.mustGit(src, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err != nil {
		t.Errorf("git's rebase state is gone: %v", err)
	}
	if got := h.mustGit(h.repo, "for-each-ref", "--format=%(refname)", "refs/heads/pg-git-lander/"); got == "" {
		t.Errorf("the work branch is gone, --quit leaves it to the caller")
	}
	if st := h.run("status"); st.json == nil || len(strs(st.json["stopped"])) != 0 {
		t.Errorf("status still lists a stopped operation: %s", st.stdout)
	}
	h.noFsckProblems()
}

// Scenario 42: a BRANCH-role lock left by a dead holder, and a waiter that
// needs the branch. The record is converted to stopped and the waiter is
// refused holder-died, naming the operation in the way; then --continue or
// --abort on the converted operation works.
func TestTier2StaleBranchRoleLockConverted(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h := crashedLand(t, "before_move")
		// The waiter is a rebase of the very branch the dead holder was landing.
		r := h.run("rebase", "feature/x", "--onto", "main", "--wait", "10s")
		expect(t, r, 22, "refused", "holder-died")
		if bo := r.json["blocking_op"]; bo == nil {
			t.Errorf("the refusal does not name blocking_op: %s", r.stdout)
		}
		st := h.run("status")
		if st.json == nil || len(strs(st.json["stopped"])) != 1 {
			t.Errorf("the record should now be a stopped operation: %s", st.stdout)
		}
		return h
	}
	t.Run("continue", func(t *testing.T) {
		h := setup(t)
		done(t, h, h.run("land", "feature/x", "--continue"), Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0},
			Snapshot: Snapshot{
				Refs:        mainMoveTo,
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		})
	})
	t.Run("abort", func(t *testing.T) {
		h := setup(t)
		done(t, h, h.run("land", "feature/x", "--abort"), Expected{
			Outcome: map[string]any{"outcome": "aborted", "exit": 0},
			Snapshot: Snapshot{
				Refs: startRefs(),
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		})
	})
}

// Scenario 44: stopped-pending and target-stopped. A rebase of main stops at a
// conflict; a land into main is refused target-stopped, a land of main is
// refused stopped-pending, both naming blocking_op; resolving the rebase
// unblocks them.
func TestTier2StoppedPendingAndTargetStopped(t *testing.T) {
	h := newHarness(t, Scenario{Shape: ShapeConflict})
	h.addBranch("y")
	// main (in the main worktree) is replayed onto feature/x and conflicts.
	r := h.run("rebase", "main", "--onto", "feature/x")
	expect(t, r, 21, "stopped", "conflict")

	r = h.run("land", "y", "--wait", "1s")
	expect(t, r, 22, "refused", "target-stopped")
	if r.json["blocking_op"] == nil {
		t.Errorf("target-stopped does not name blocking_op: %s", r.stdout)
	}
	r = h.run("land", "main", "--into", "y", "--wait", "1s")
	expect(t, r, 22, "refused", "stopped-pending")
	if r.json["blocking_op"] == nil {
		t.Errorf("stopped-pending does not name blocking_op: %s", r.stdout)
	}

	// Resolving the rebase unblocks both.
	resolveByHand(t, h, h.repo)
	expect(t, h.run("rebase", "main", "--continue"), 0, "rebased", "")
	// y was cut from the old main, so its own land stops at a conflict with
	// the rewritten main: a stop of its own, no longer a refusal.
	expect(t, h.run("land", "y"), 21, "stopped", "conflict")
	expect(t, h.run("land", "y", "--abort"), 0, "aborted", "")
	h.addBranch("z")
	expect(t, h.run("land", "z"), 0, "landed", "")
	h.noProblems()
}

// afterHookCrash crashes a land of feature/x right after the move (inside its
// after hook) and converts the dead holder's record to stopped with a waiter.
func afterHookCrash(t *testing.T) *harness {
	t.Helper()
	h := crashedLand(t, "after")
	expect(t, h.run("land", "feature/x", "--wait", "10s"), 22, "refused", "holder-died")
	return h
}

// Scenario 51: --abort after the move reached expected_result only finishes
// the cleanup and reports moved_already, leaving TARGET at the result;
// --continue with the source worktree already removed counts it as done and
// finishes.
func TestTier2AbortAfterMoveAndContinueWithoutSourceWorktree(t *testing.T) {
	t.Run("abort-after-the-move", func(t *testing.T) {
		h := afterHookCrash(t)
		r := h.run("land", "feature/x", "--abort")
		done(t, h, r, Expected{
			Outcome: map[string]any{"outcome": "aborted", "exit": 0, "moved_already": true},
			Snapshot: Snapshot{
				Refs:       map[string]string{"refs/heads/main": "@feature/x"},
				Worktrees:  cleanMain,
				TargetTree: "@tree:feature/x",
			},
		})
	})
	t.Run("continue-without-the-source-worktree", func(t *testing.T) {
		h := afterHookCrash(t)
		h.mustGit(h.repo, "worktree", "remove", h.wtPath("src"))
		r := h.run("land", "feature/x", "--continue")
		done(t, h, r, Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0},
			Snapshot: Snapshot{
				Refs:        mainMoveTo,
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		})
	})
}

// quitStopped runs the hook-failing land of resumeRaceScenario so that the
// operation is stopped (hook-failed) with the work worktree and branch in place.
func quitStopped(t *testing.T, block bool) *harness {
	t.Helper()
	h := newHarness(t, resumeRaceScenario("quit", block))
	expect(t, h.run("land", "feature/x"), 21, "stopped", "hook-failed")
	return h
}

// quitLeft asserts what --quit leaves: no records or locks, the source
// worktree and the work branch in place, and a sound repository.
func quitLeft(t *testing.T, h *harness) {
	t.Helper()
	if refs := h.lockRefs(); refs != "" {
		t.Errorf("records or locks left behind: %s", refs)
	}
	if _, err := os.Stat(h.wtPath("src")); err != nil {
		t.Errorf("the source worktree is gone: %v", err)
	}
	if got := h.mustGit(h.repo, "for-each-ref", "--format=%(refname)", "refs/heads/pg-git-lander/"); got == "" {
		t.Errorf("the work branch is gone, --quit leaves it to the caller")
	}
	h.noFsckProblems()
}

// Scenario 54: --quit takes the stopped record by compare-and-swap and deletes
// the records; the loser of a race with it gets lost-to-concurrent; the
// worktree and the work branch stay.
func TestTier2QuitTakesTheStoppedRecord(t *testing.T) {
	t.Run("quit-alone", func(t *testing.T) {
		h := quitStopped(t, false)
		expect(t, h.run("land", "feature/x", "--quit"), 0, "quit", "")
		quitLeft(t, h)
	})
	t.Run("quit-while-continue-holds-the-record", func(t *testing.T) {
		h := quitStopped(t, true)
		h.start("continue", "land", "feature/x", "--continue")
		h.await("up") // --continue holds the record as running, inside the hook
		expect(t, h.run("land", "feature/x", "--quit"), 22, "refused", "lost-to-concurrent")
		h.release("go")
		expect(t, h.wait("continue"), 0, "landed", "")
		h.noProblems()
	})
	t.Run("continue-while-quit-holds-nothing-left", func(t *testing.T) {
		h := quitStopped(t, false)
		expect(t, h.run("land", "feature/x", "--quit"), 0, "quit", "")
		// The record is gone, so a later resume has nothing to take.
		expect(t, h.run("land", "feature/x", "--continue"), 22, "refused", "nothing-stopped")
		quitLeft(t, h)
	})
	t.Run("two-quits-started-together", func(t *testing.T) {
		h := quitStopped(t, false)
		h.start("q1", "land", "feature/x", "--quit")
		h.start("q2", "land", "feature/x", "--quit")
		a, b := h.wait("q1"), h.wait("q2")
		wins := func(r *result) bool { return r.exit == 0 && r.json["outcome"] == "quit" }
		loses := func(r *result) bool {
			return r.exit == 22 && (r.json["reason"] == "lost-to-concurrent" || r.json["reason"] == "nothing-stopped")
		}
		if !(wins(a) && loses(b)) && !(wins(b) && loses(a)) {
			t.Fatalf("exactly one --quit must win:\nq1: exit %d %s\nq2: exit %d %s", a.exit, a.stdout, b.exit, b.stdout)
		}
		quitLeft(t, h)
	})
}

// noFsckProblems checks only the repository's soundness, for scenarios that
// end with a deliberately left work branch and worktree (--quit).
func (h *harness) noFsckProblems() {
	h.t.Helper()
	h.sc.Expected.Stopped = true
	h.noProblems()
}

// crashConfig is blockingConfig with the default lease: a crashed holder on
// this host is recognised by its dead pid at once, so these scenarios need no
// short lease, and a long one keeps a loaded host from expiring a live holder.
func crashConfig(point string) string {
	return `
[[repo]]
root = "{repo}"

[repo.hooks.land]
` + point + ` = [{ run = ["{hooks}/blocker"], timeout = "60s" }]
`
}
