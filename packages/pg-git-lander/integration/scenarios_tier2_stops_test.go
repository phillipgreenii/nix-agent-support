//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tier 2 scenarios 36, 43, 45, 46, 52 and 53 of section 15.3: observability,
// and the refusals and stops that need a layout or a hook to provoke. Each
// runs the real binary against real git in a fixture copy; the outcome subset,
// the snapshot and the post-condition must all hold. The mid-operation
// interference (a commit on BRANCH, a dirtied destination worktree, a TARGET
// that is moved back) is done by real scripts planted where git or the tool
// runs them, so the scenarios need no sleeps.

// hookGit makes a script's git commands independent of any git variable the
// tool exports for its children.
const hookGit = "unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE\n"

// slowLease gives a scenario a lease long enough that a heavily loaded host
// cannot make a healthy holder look stale (lock-lost); scenarios that test the
// lease itself set their own.
const slowLease = "[defaults.lock]\nlease_ttl = \"10m\"\n"

// newTier2 is newHarness with the long lease in the user config.
func newTier2(t *testing.T, sc Scenario) *harness {
	t.Helper()
	if !strings.Contains(sc.Config, "[defaults.lock]") {
		sc.Config = slowLease + sc.Config
	}
	return newHarness(t, sc)
}

// keysPresent reports the keys of want that the outcome does not carry.
func keysPresent(t *testing.T, r *result, want ...string) {
	t.Helper()
	for _, k := range want {
		if _, ok := r.json[k]; !ok {
			t.Errorf("outcome %v lacks the key %q: %s", r.json["outcome"], k, r.stdout)
		}
	}
}

// wantNext checks the resume flags a stop offers: each entry of next is a full
// command line, and the flag it carries is the resume flag.
func wantNext(t *testing.T, r *result, flags ...string) {
	t.Helper()
	var got []string
	list, _ := r.json["next"].([]any)
	for _, n := range list {
		for _, tok := range strings.Fields(n.(string)) {
			switch tok {
			case "--continue", "--skip", "--abort", "--quit":
				got = append(got, tok)
			}
		}
	}
	if strings.Join(got, ",") != strings.Join(flags, ",") {
		t.Errorf("next offers %v, want %v: %s", got, flags, r.stdout)
	}
}

// events reads the events file of the scenario's clone.
func (h *harness) events() []map[string]any {
	h.t.Helper()
	f, err := os.Open(filepath.Join(h.repo, ".git", "pg-git-lander", "events.jsonl"))
	if err != nil {
		h.t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			h.t.Fatalf("events.jsonl line does not parse: %v: %q", err, sc.Text())
		}
		out = append(out, m)
	}
	return out
}

// eventsOfType returns the events of one type, in order.
func eventsOfType(evs []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		if e["event"] == typ {
			out = append(out, e)
		}
	}
	return out
}

// checkEventsShape asserts what every line of the events file carries.
func checkEventsShape(t *testing.T, evs []map[string]any) {
	t.Helper()
	if len(evs) == 0 {
		t.Fatal("events.jsonl is empty")
	}
	known := map[string]bool{
		"op_start": true, "stage": true, "lock_wait": true, "race_retry": true,
		"hook": true, "holder_converted": true, "conflict": true, "op_end": true,
	}
	for i, e := range evs {
		typ, _ := e["event"].(string)
		if !known[typ] {
			t.Errorf("line %d: unknown event type %q", i+1, typ)
		}
		for _, k := range []string{"ts", "repo", "host", "pid", "tool_version", "op_id", "operation", "branch", "target"} {
			if _, ok := e[k]; !ok {
				t.Errorf("line %d (%s) lacks %q", i+1, typ, k)
			}
		}
	}
}

func stageNames(evs []map[string]any) []string {
	var s []string
	for _, e := range eventsOfType(evs, "stage") {
		s = append(s, e["stage"].(string))
	}
	return s
}

// Scenario 36, landed: the golden outcome and the events of a landed run.
func TestTier2ObservabilityLanded(t *testing.T) {
	h := newTier2(t, Scenario{
		Shape:  ShapeDiverged,
		Layout: srcOnBr,
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/ok"], timeout = "60s" }]
`,
		Hooks: map[string]string{"ok": "true"},
	})
	r := h.run("land", "feature/x")
	done(t, h, r, Expected{
		Outcome: map[string]any{
			"schema": 1, "outcome": "landed", "exit": 0, "operation": "land", "branch": "feature/x", "target": "main",
			"path_taken": "rebase", "branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "removed"},
			"stats": map[string]any{"rebased_commits": 1},
		},
		Snapshot: Snapshot{Worktrees: cleanMain, NoWorktrees: []string{"src"}},
	})
	keysPresent(t, r, "op_id", "tips", "landed_commit", "path_taken", "stats", "dropped_commits", "resolved",
		"autofix", "skipped_hooks", "warnings", "branch_fate", "worktree_fate", "work_branch")

	evs := h.events()
	checkEventsShape(t, evs)
	if evs[0]["event"] != "op_start" {
		t.Errorf("the first event is %v, want op_start", evs[0]["event"])
	}
	last := evs[len(evs)-1]
	if last["event"] != "op_end" || last["outcome"] != "landed" || last["path_taken"] != "rebase" {
		t.Errorf("the last event = %v, want op_end landed by rebase", last)
	}
	if got := strings.Join(stageNames(evs), ","); !strings.HasPrefix(got, "locked,preparing,rebasing") || !strings.HasSuffix(got, "moving,after,cleanup") {
		t.Errorf("stages = %s", got)
	}
	hooks := eventsOfType(evs, "hook")
	if len(hooks) != 1 || hooks[0]["point"] != "before_move" || hooks[0]["exit"] != float64(0) {
		t.Errorf("hook events = %v, want the one before_move run", hooks)
	}
	for _, e := range evs {
		if e["op_id"] != r.json["op_id"] {
			t.Errorf("event %v carries op_id %v, want %v", e["event"], e["op_id"], r.json["op_id"])
		}
	}
}

// Scenario 36, stopped: the golden stop, its events, and status showing the
// stopped operation next to an orphan work branch.
func TestTier2ObservabilityStoppedAndStatus(t *testing.T) {
	h := newTier2(t, Scenario{
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/gate"], timeout = "60s" }]
`,
		Hooks: map[string]string{"gate": `[ -e "$PGL_MARKERS/fixed" ]`},
	})
	r := h.run("land", "feature/x")
	expect(t, r, 21, "stopped", "hook-failed")
	wantNext(t, r, "--continue", "--abort", "--quit")
	keysPresent(t, r, "schema", "op_id", "operation", "branch", "target", "reason", "detail", "retryable", "stage", "next",
		"hint", "log", "log_tail", "work_branch", "tips")
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{
			"schema": 1, "exit": 21, "operation": "land", "branch": "feature/x", "target": "main",
			"stage": "before_move", "retryable": false,
		},
		Snapshot: Snapshot{Refs: startRefs()},
		Stopped:  true,
	})

	evs := h.events()
	checkEventsShape(t, evs)
	last := evs[len(evs)-1]
	if last["event"] != "op_end" || last["outcome"] != "stopped" || last["reason"] != "hook-failed" || last["stage"] != "before_move" {
		t.Errorf("the last event = %v, want op_end stopped hook-failed at before_move", last)
	}
	if hooks := eventsOfType(evs, "hook"); len(hooks) != 1 || hooks[0]["exit"] == float64(0) {
		t.Errorf("hook events = %v, want one failing run", hooks)
	}

	// An orphan work branch: a pg-git-lander/* branch no record names.
	h.mustGit(h.repo, "branch", "pg-git-lander/orphan-fixture", "main")
	st := h.run("status")
	expect(t, st, 0, "status", "")
	stopped, _ := st.json["stopped"].([]any)
	if len(stopped) != 1 {
		t.Fatalf("status lists %d stopped operations, want 1: %s", len(stopped), st.stdout)
	}
	s0, _ := stopped[0].(map[string]any)
	if s0["op_id"] != r.json["op_id"] || s0["reason"] != "hook-failed" || s0["branch"] != "feature/x" || s0["stage"] != "before_move" {
		t.Errorf("status shows the stopped operation as %v, want the op %v", s0, r.json["op_id"])
	}
	var orphans []string
	list, _ := st.json["orphans"].([]any)
	for _, o := range list {
		if m, ok := o.(map[string]any); ok {
			orphans = append(orphans, m["branch"].(string))
		}
	}
	if strings.Join(orphans, ",") != "pg-git-lander/orphan-fixture" {
		t.Errorf("status orphans = %v, want only the planted branch (the stopped op's work branch is named by its record)", orphans)
	}

	// Clean up: abort the stopped operation and drop the orphan.
	expect(t, h.run("land", "feature/x", "--abort"), 0, "aborted", "")
	h.mustGit(h.repo, "branch", "-D", "pg-git-lander/orphan-fixture")
	h.noProblems()
}

// Scenario 36, busy: the golden busy outcome and the waiter's lock_wait event.
func TestTier2ObservabilityBusy(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape:  ShapeAhead,
		Fifos:  []string{"up", "go"},
		Layout: srcOnBr,
		// A long lease keeps a heartbeat starved by host load from expiring the
		// holder's lock, which would turn the busy refusal into a takeover.
		Config: crashConfig("before_move"),
		Hooks:  map[string]string{"blocker": blockingHook},
	})
	h.addBranch("other")
	h.start("holder", "land", "feature/x")
	h.await("up")

	r := h.run("land", "other", "--wait", "1s")
	expect(t, r, 9, "busy", "")
	keysPresent(t, r, "schema", "op_id", "operation", "branch", "target", "holder", "hint")
	holder, _ := r.json["holder"].(map[string]any)
	if holder == nil || holder["branch"] != "feature/x" || holder["op_id"] == "" {
		t.Errorf("busy does not name the holder: %s", r.stdout)
	}
	waits := eventsOfType(h.events(), "lock_wait")
	if len(waits) == 0 {
		t.Errorf("no lock_wait event for the waiting land")
	}
	h.release("go")
	done(t, h, h.wait("holder"), Expected{Outcome: map[string]any{"outcome": "landed", "exit": 0}})
}

// Scenario 36, race: TARGET moves under the land at before_move on every try,
// so the retries run out; the golden stop is race and the events record each
// retry.
func TestTier2ObservabilityRace(t *testing.T) {
	h := newTier2(t, Scenario{
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Config: `
[[repo]]
root = "{repo}"
max_race_retries = 1

[repo.hooks.land]
before_move = [{ run = ["{hooks}/bump"], timeout = "60s" }]
`,
		Hooks: map[string]string{"bump": hookGit + `
t=$(git -C "$PGL_REPO" rev-parse 'main^{tree}')
c=$(git -C "$PGL_REPO" commit-tree -p main -m "moved by someone else" "$t")
git -C "$PGL_REPO" update-ref refs/heads/main "$c"
`},
	})
	r := h.run("land", "feature/x")
	expect(t, r, 21, "stopped", "race")
	keysPresent(t, r, "schema", "op_id", "reason", "retryable", "stage", "next", "work_branch", "tips")
	wantNext(t, r, "--continue", "--abort")
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{"stage": "moving", "retryable": true},
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/feature/x": "@feature/x"},
		},
		Stopped: true,
	})
	evs := h.events()
	checkEventsShape(t, evs)
	retries := eventsOfType(evs, "race_retry")
	if len(retries) != 1 || retries[0]["attempt"] != float64(1) {
		t.Errorf("race_retry events = %v, want one with attempt 1", retries)
	}
	for _, k := range []string{"old_target_tip", "new_target_tip"} {
		if _, ok := retries[0][k]; !ok {
			t.Errorf("race_retry lacks %q", k)
		}
	}
	last := evs[len(evs)-1]
	if last["event"] != "op_end" || last["outcome"] != "stopped" || last["reason"] != "race" {
		t.Errorf("the last event = %v, want op_end stopped race", last)
	}
	expect(t, h.run("land", "feature/x", "--abort"), 0, "aborted", "")
	h.noProblems()
}

// Scenario 43: the source worktree is the main worktree and it holds BRANCH.
// Without --keep-source-worktree the land is refused before anything is locked
// or changed; with it the land succeeds and parks the main worktree.
func TestTier2MainWorktreeRemoval(t *testing.T) {
	setup := func(t *testing.T) *harness {
		h := newTier2(t, Scenario{Shape: ShapeAhead})
		h.mustGit(h.repo, "switch", "-q", "feature/x")
		return h
	}
	t.Run("refused", func(t *testing.T) {
		h := setup(t)
		r := h.run("land", "feature/x")
		expect(t, r, 22, "refused", "main-worktree-removal")
		if hint, _ := r.json["hint"].(string); !strings.Contains(hint, "--keep-source-worktree") {
			t.Errorf("the refusal does not name --keep-source-worktree: %s", r.stdout)
		}
		done(t, h, r, Expected{
			Outcome: map[string]any{"role": "source", "path": h.repo},
			Snapshot: Snapshot{
				Refs: startRefs(),
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:feature/x", CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		})
		if got := h.mustGit(h.repo, "for-each-ref", "refs/pg-git-lander/"); got != "" {
			t.Errorf("the refusal left records or locks: %s", got)
		}
	})
	t.Run("keep-source-worktree-lands-and-parks", func(t *testing.T) {
		h := setup(t)
		r := h.run("land", "feature/x", "--keep-source-worktree")
		done(t, h, r, Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "branch_fate": "deleted",
				"worktree_fate": map[string]any{"fate": "kept"},
			},
			Snapshot: Snapshot{
				Refs:       mainMoveTo,
				Worktrees:  map[string]WorktreeState{"main": {CheckStatus: true}},
				TargetTree: "@tree:feature/x",
			},
			Placeholder: true,
		})
		pb, _ := r.json["placeholder_branch"].(string)
		if pb == "" || h.head(h.repo) != "branch:"+pb {
			t.Errorf("the main worktree is on %s, want the placeholder %q", h.head(h.repo), pb)
		}
	})
}

// Scenario 45: a commit is added to BRANCH from another path during a rebase
// (at after_rebase). The move stops with branch-moved, --abort is the only
// next step, and it restores everything: BRANCH keeps the commit someone else
// made.
func TestTier2BranchMovedDuringRebase(t *testing.T) {
	h := newTier2(t, Scenario{
		Shape:  ShapeDiverged,
		Layout: srcOnBr,
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.rebase]
after_rebase = [{ run = ["{hooks}/bump"], timeout = "60s" }]
`,
		Hooks: map[string]string{"bump": hookGit + `
t=$(git -C "$PGL_REPO" rev-parse 'feature/x^{tree}')
c=$(git -C "$PGL_REPO" commit-tree -p feature/x -m "added from another worktree" "$t")
git -C "$PGL_REPO" update-ref refs/heads/feature/x "$c"
`},
	})
	r := h.run("rebase", "feature/x", "--onto", "main")
	expect(t, r, 21, "stopped", "branch-moved")
	moved := h.mustGit(h.repo, "rev-parse", "feature/x")
	if moved == shapes[ShapeDiverged].facts.Tips["feature/x"] {
		t.Fatal("the hook did not move BRANCH")
	}
	wantNext(t, r, "--abort")
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{"stage": "moving", "retryable": false},
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": moved},
		},
		Stopped: true,
	})

	// Resuming with --continue is not offered; --abort is.
	if c := h.run("rebase", "feature/x", "--continue"); c.exit == 0 {
		t.Errorf("--continue succeeded after branch-moved: %s", c.stdout)
	}
	expect(t, h.run("rebase", "feature/x", "--abort"), 0, "aborted", "")
	checkNow(t, h, h.result, Expected{
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": moved},
			Worktrees: map[string]WorktreeState{
				"main": {Head: "branch:main", CheckStatus: true},
				"src":  {Head: "branch:feature/x", CheckStatus: true},
			},
			TargetTree: "@tree:main",
		},
	})
	h.noProblems()
}

// dirtyOnce is a before_move hook that, the first time it runs, changes a
// tracked file of the main worktree (the destination worktree).
const dirtyOnce = `
[ -e "$PGL_MARKERS/once" ] && exit 0
touch "$PGL_MARKERS/once"
echo changed > "$PGL_REPO/README.md"
`

// detachOnce is a before_move hook that, the first time it runs, detaches the
// main worktree (the destination worktree) from TARGET.
const detachOnce = hookGit + `
[ -e "$PGL_MARKERS/once" ] && exit 0
touch "$PGL_MARKERS/once"
git -C "$PGL_REPO" switch -q --detach
`

func beforeMoveConfig(script string) string {
	return "\n[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\nbefore_move = [{ run = [\"{hooks}/" + script + "\"], timeout = \"60s\" }]\n"
}

// Scenario 46, checkout-changed: the destination worktree's tree or HEAD
// changed after the start. The stop is resumable: --continue after the fix
// lands.
func TestTier2CheckoutChangedAtDestination(t *testing.T) {
	for _, tc := range []struct {
		name, hook, fix string
	}{
		{"tree-changed", dirtyOnce, "checkout"},
		{"head-changed", detachOnce, "switch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTier2(t, Scenario{
				Shape: ShapeAhead, Layout: srcOnBr,
				Config: beforeMoveConfig("disturb"), Hooks: map[string]string{"disturb": tc.hook},
			})
			r := h.run("land", "feature/x")
			expect(t, r, 21, "stopped", "checkout-changed")
			wantMain := WorktreeState{Head: "branch:main", CheckStatus: true, Status: "M README.md"}
			if tc.fix == "switch" {
				wantMain = WorktreeState{Head: "detached:" + tipMain(), CheckStatus: true}
			}
			wantNext(t, r, "--continue", "--abort")
			checkNow(t, h, r, Expected{
				Outcome: map[string]any{"stage": "moving", "retryable": true},
				Snapshot: Snapshot{
					Refs:       startRefs(),
					Worktrees:  map[string]WorktreeState{"main": wantMain},
					TargetTree: "@tree:main",
				},
				Stopped: true,
			})
			if tc.fix == "switch" {
				h.mustGit(h.repo, "switch", "-q", "main")
			} else {
				h.mustGit(h.repo, "checkout", "--", "README.md")
			}
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
	}
}

// Scenario 46, the destination worktree being the work worktree: the tool
// switched it to TARGET for the move and the check fails, so the stop leaves
// it back on the work branch.
func TestTier2CheckoutChangedWhenDestinationIsWorkWorktree(t *testing.T) {
	// git itself runs this post-checkout hook: the first time the main worktree
	// is switched onto main (the tool's switch for the move) it changes a
	// tracked file, so the check that follows finds the worktree changed.
	h := newTier2(t, Scenario{
		Shape: ShapeAhead,
		Hooks: map[string]string{"post-checkout": hookGit + `
[ -e "$PGL_MARKERS/once" ] && exit 0
[ "$(git symbolic-ref -q HEAD)" = refs/heads/main ] || exit 0
touch "$PGL_MARKERS/once"
echo changed > README.md
`},
	})
	r := h.run("land", "feature/x")
	expect(t, r, 21, "stopped", "checkout-changed")
	wb, _ := r.json["work_branch"].(string)
	if wb == "" {
		t.Fatalf("the stop names no work branch: %s", r.stdout)
	}
	wantNext(t, r, "--continue", "--abort")
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{"stage": "moving"},
		Snapshot: Snapshot{
			Refs:       startRefs(),
			Worktrees:  map[string]WorktreeState{"main": {Head: "branch:" + wb, CheckStatus: true, Status: "M README.md"}},
			TargetTree: "@tree:main",
		},
		Stopped: true,
	})
	h.mustGit(h.repo, "checkout", "--", "README.md")
	done(t, h, h.run("land", "feature/x", "--continue"), Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0},
		Snapshot: Snapshot{
			Refs:       mainMoveTo,
			Worktrees:  cleanMain,
			TargetTree: "@tree:feature/x",
		},
	})
}

// Scenario 46, move-unverified: after git merge --ff-only a post-merge hook
// (git runs it, not the tool) puts TARGET back, so TARGET is not the result.
// Only --abort is offered, and BRANCH is not retired.
func TestTier2MoveUnverifiedAtDestination(t *testing.T) {
	h := newTier2(t, Scenario{
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Hooks:  map[string]string{"post-merge": hookGit + "git reset -q --hard HEAD~1\n"},
	})
	r := h.run("land", "feature/x")
	expect(t, r, 21, "stopped", "move-unverified")
	back := h.mustGit(h.repo, "rev-parse", "main")
	wantNext(t, r, "--abort")
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{"stage": "moving", "retryable": false},
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/main": back, "refs/heads/feature/x": "@feature/x"},
		},
		Stopped: true,
	})
	if back == shapes[ShapeAhead].facts.Tips["feature/x"] {
		t.Fatal("the post-merge hook did not move TARGET back")
	}
	// BRANCH was not retired, and the worktree that held it is still there.
	if _, err := os.Stat(h.wtPath("src")); err != nil {
		t.Errorf("the source worktree is gone: %v", err)
	}
	if c := h.run("land", "feature/x", "--continue"); c.exit == 0 {
		t.Errorf("--continue succeeded after move-unverified: %s", c.stdout)
	}
	expect(t, h.run("land", "feature/x", "--abort"), 0, "aborted", "")
	checkNow(t, h, h.result, Expected{
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/feature/x": "@feature/x"},
			Worktrees: map[string]WorktreeState{
				"main": {Head: "branch:main", CheckStatus: true},
				"src":  {Head: "branch:feature/x", CheckStatus: true},
			},
		},
	})
	h.noProblems()
}

// Scenario 52: nothing-to-land with each combination of the keep flags. BRANCH
// and the source worktree follow section 11.3 and nothing moves; with
// --keep-source-branch BRANCH stays where it is.
func TestTier2NothingToLandKeepFlags(t *testing.T) {
	branchGone := map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": ""}
	branchKept := map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": "@feature/x"}
	for _, tc := range []struct {
		name  string
		flags []string
		exp   Expected
	}{
		{"no-flag", nil, Expected{
			Outcome:  map[string]any{"outcome": "nothing-to-land", "exit": 0, "branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "removed"}},
			Snapshot: Snapshot{Refs: branchGone, Worktrees: cleanMain, NoWorktrees: []string{"src"}, TargetTree: "@tree:main"},
		}},
		{"keep-source-worktree", []string{"--keep-source-worktree"}, Expected{
			Outcome: map[string]any{"outcome": "nothing-to-land", "exit": 0, "branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "kept"}},
			Snapshot: Snapshot{
				Refs: branchGone,
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
			Placeholder: true,
		}},
		{"keep-source-branch", []string{"--keep-source-branch"}, Expected{
			Outcome:  map[string]any{"outcome": "nothing-to-land", "exit": 0, "branch_fate": "kept", "worktree_fate": map[string]any{"fate": "removed"}},
			Snapshot: Snapshot{Refs: branchKept, Worktrees: cleanMain, NoWorktrees: []string{"src"}, TargetTree: "@tree:main"},
		}},
		{"both", []string{"--keep-source-worktree", "--keep-source-branch"}, Expected{
			Outcome: map[string]any{"outcome": "nothing-to-land", "exit": 0, "branch_fate": "kept", "worktree_fate": map[string]any{"fate": "kept"}},
			Snapshot: Snapshot{
				Refs: branchKept,
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTier2(t, Scenario{Shape: ShapeMerged, Layout: srcOnBr})
			args := append([]string{"land", "feature/x", "--source-worktree", "{wt:src}"}, tc.flags...)
			done(t, h, h.run(args...), tc.exp)
		})
	}
}

// Scenario 53: a --target-worktree or --source-worktree that is not the
// worktree where TARGET or BRANCH is checked out is refused, nothing is locked
// or changed, and naming the right worktree works.
func TestTier2WorktreeMismatchRefusals(t *testing.T) {
	layout := []Worktree{{Name: "src", Branch: "feature/x"}, {Name: "spare", Detach: true}}
	untouched := Expected{
		Snapshot: Snapshot{
			Refs: startRefs(),
			Worktrees: map[string]WorktreeState{
				"main":  {Head: "branch:main", CheckStatus: true},
				"src":   {Head: "branch:feature/x", CheckStatus: true},
				"spare": {CheckStatus: true},
			},
			TargetTree: "@tree:main",
		},
	}
	landed := Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted"},
		Snapshot: Snapshot{
			Refs:        mainMoveTo,
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
			TargetTree:  "@tree:feature/x",
		},
	}
	t.Run("target-worktree-mismatch", func(t *testing.T) {
		h := newTier2(t, Scenario{Shape: ShapeAhead, Layout: layout})
		r := h.run("land", "feature/x", "--target-worktree", "{wt:spare}")
		expect(t, r, 22, "refused", "target-worktree-mismatch")
		exp := untouched
		exp.Outcome = map[string]any{"role": "destination", "path": h.wtPath("spare")}
		done(t, h, r, exp)
		if got := h.lockRefs(); got != "" {
			t.Errorf("the refusal left locks: %s", got)
		}
		done(t, h, h.run("land", "feature/x", "--target-worktree", h.repo), landed)
	})
	t.Run("source-worktree-mismatch", func(t *testing.T) {
		h := newTier2(t, Scenario{Shape: ShapeAhead, Layout: layout})
		r := h.run("land", "feature/x", "--source-worktree", "{wt:spare}")
		expect(t, r, 22, "refused", "source-worktree-mismatch")
		exp := untouched
		exp.Outcome = map[string]any{"role": "source", "path": h.wtPath("spare")}
		done(t, h, r, exp)
		if got := h.lockRefs(); got != "" {
			t.Errorf("the refusal left locks: %s", got)
		}
		done(t, h, h.run("land", "feature/x", "--source-worktree", h.wtPath("src")), landed)
	})
}
