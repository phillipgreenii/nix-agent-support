//go:build integration

package integration

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"
)

// Tier 1 scenarios 1 to 10, 38 and 39 of section 15.3: the land and rebase
// paths and the keep flags. Each runs the real binary against real git in a
// fixture copy; the outcome subset, the snapshot and the post-condition must
// all hold.

var (
	tipMain    = func() string { return shapes[ShapeAhead].facts.Tips["main"] }
	srcOnBr    = []Worktree{{Name: "src", Branch: "feature/x"}}
	cleanMain  = map[string]WorktreeState{"main": {Head: "branch:main", CheckStatus: true}}
	mainMoveTo = map[string]string{"refs/heads/main": "@feature/x", "refs/heads/feature/x": ""}
)

// Scenario 1: fast-forward land from BRANCH's own worktree, --source-worktree
// passed.
func TestTier1FastForwardLand(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "01-fast-forward-with-source-worktree",
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:src}"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "path_taken": "fast-forward",
				"branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "removed"},
			},
			Snapshot: Snapshot{
				Refs:        mainMoveTo,
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		},
	})
}

// Scenario 2: a land that needs a rebase.
func TestTier1LandNeedingRebase(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "02-land-needs-rebase",
		Shape:  ShapeDiverged,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:src}"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "path_taken": "rebase",
				"branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "removed"},
				"stats": map[string]any{"rebased_commits": 1},
			},
			Snapshot: Snapshot{
				// Linear: the rebased commit sits directly on main's old tip,
				// which sits directly on the base.
				Refs: map[string]string{
					"refs/heads/main~1":    "@main",
					"refs/heads/feature/x": "",
				},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
			},
		},
	})
}

// Scenario 3: BRANCH is checked out nowhere; a spare clean worktree is the
// source. The spare is restored and kept; BRANCH is deleted.
func TestTier1SpareWorktreeAsSource(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "03-spare-worktree-source",
		Shape:  ShapeAhead,
		Layout: []Worktree{{Name: "spare", Detach: true}},
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:spare}"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "branch_fate": "deleted",
				"worktree_fate": map[string]any{"fate": "kept"},
			},
			Snapshot: Snapshot{
				Refs: mainMoveTo,
				Worktrees: map[string]WorktreeState{
					"main":  {Head: "branch:main", CheckStatus: true},
					"spare": {Head: "detached:" + tipMain(), CheckStatus: true},
				},
				TargetTree: "@tree:feature/x",
			},
		},
	})
}

// Scenario 4: one worktree does everything (a plain clone, TARGET checked
// out, BRANCH not): the work happens in the destination worktree, which ends
// back on TARGET, with the warning work-in-destination.
func TestTier1OneWorktreeDoesEverything(t *testing.T) {
	runScenario(t, Scenario{
		Name:  "04-one-worktree",
		Shape: ShapeAhead,
		Steps: []Step{{Run: []string{"land", "feature/x"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "branch_fate": "deleted",
				"warnings": []any{map[string]any{"warning": "work-in-destination", "reason": "", "detail": ""}},
			},
			Snapshot: Snapshot{
				Refs:       mainMoveTo,
				Worktrees:  cleanMain,
				TargetTree: "@tree:feature/x",
			},
		},
	})
}

// Scenario 5: --keep-source-worktree leaves the worktree parked on the
// placeholder branch.
func TestTier1KeepSourceWorktree(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "05-keep-source-worktree",
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:src}", "--keep-source-worktree"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "branch_fate": "deleted",
				"worktree_fate": map[string]any{"fate": "kept"},
			},
			Snapshot: Snapshot{
				Refs: mainMoveTo,
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {CheckStatus: true},
				},
				TargetTree: "@tree:feature/x",
			},
			Placeholder: true,
		},
	})
	// The parked branch is the placeholder: pg-git-lander/idle/<name>.
	t.Run("05-parked-on-placeholder", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		r := h.run("land", "feature/x", "--source-worktree", h.wtPath("src"), "--keep-source-worktree")
		expect(t, r, 0, "landed", "")
		pb, _ := r.json["placeholder_branch"].(string)
		if pb == "" || h.head(h.wtPath("src")) != "branch:"+pb {
			t.Fatalf("worktree head %s, want the placeholder %q", h.head(h.wtPath("src")), pb)
		}
		if got := h.mustGit(h.repo, "rev-parse", "refs/heads/"+pb); got != shapes[ShapeAhead].facts.Tips["feature/x"] {
			t.Fatalf("placeholder at %s, want TARGET's new tip", got)
		}
	})
}

// Scenario 6: --keep-source-branch: BRANCH equals the result, the worktree is
// removed.
func TestTier1KeepSourceBranch(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "06-keep-source-branch",
		Shape:  ShapeDiverged,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:src}", "--keep-source-branch"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "branch_fate": "moved",
				"worktree_fate": map[string]any{"fate": "removed"},
			},
			Snapshot: Snapshot{
				Refs: map[string]string{
					"refs/heads/feature/x": "=refs/heads/main",
					"refs/heads/main~1":    "@main",
				},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
			},
		},
	})
}

// Scenario 7: both keep flags: the worktree stays on BRANCH at the result,
// clean.
func TestTier1BothKeepFlags(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "07-both-keep-flags",
		Shape:  ShapeDiverged,
		Layout: srcOnBr,
		Steps: []Step{{Run: []string{
			"land", "feature/x", "--source-worktree", "{wt:src}", "--keep-source-worktree", "--keep-source-branch",
		}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "branch_fate": "moved",
				"worktree_fate": map[string]any{"fate": "kept"},
			},
			Snapshot: Snapshot{
				Refs: map[string]string{
					"refs/heads/feature/x": "=refs/heads/main",
					"refs/heads/main~1":    "@main",
				},
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
			},
		},
	})
}

// Scenario 8: the rebase operation: BRANCH is the result, TARGET unchanged.
func TestTier1RebaseOperation(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "08-rebase-operation",
		Shape:  ShapeDiverged,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"rebase", "feature/x", "--onto", "main", "--source-worktree", "{wt:src}"}}},
		Expected: Expected{
			Outcome: map[string]any{"outcome": "rebased", "exit": 0, "operation": "rebase"},
			Snapshot: Snapshot{
				Refs: map[string]string{
					"refs/heads/main":        "@main",
					"refs/heads/feature/x~1": "@main",
				},
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		},
	})
}

// Scenario 9: rebase when BRANCH is up to date changes nothing.
func TestTier1RebaseUpToDate(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "09-rebase-up-to-date",
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"rebase", "feature/x", "--onto", "main", "--source-worktree", "{wt:src}"}}},
		Expected: Expected{
			Outcome: map[string]any{"outcome": "up-to-date", "exit": 0},
			Snapshot: Snapshot{
				Refs: map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": "@feature/x"},
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		},
	})
}

// Scenario 10: BRANCH already merged: nothing-to-land; BRANCH is deleted and
// the source worktree removed, or BRANCH is kept where it is with
// --keep-source-branch (section 11.3).
func TestTier1AlreadyMerged(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "10-merged-default",
		Shape:  ShapeMerged,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:src}"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "nothing-to-land", "exit": 0, "branch_fate": "deleted",
				"worktree_fate": map[string]any{"fate": "removed"},
			},
			Snapshot: Snapshot{
				Refs:        map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": ""},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:main",
			},
		},
	})
	runScenario(t, Scenario{
		Name:   "10-merged-keep-source-branch",
		Shape:  ShapeMerged,
		Layout: srcOnBr,
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:src}", "--keep-source-branch"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "nothing-to-land", "exit": 0, "branch_fate": "kept",
				"worktree_fate": map[string]any{"fate": "removed"},
			},
			Snapshot: Snapshot{
				Refs:        map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": "@feature/x"},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:main",
			},
		},
	})
}

// runFrom runs the tool with dir as its current directory and no --repo, so
// the directory itself decides which worktree is the source.
func runFrom(t *testing.T, h *harness, dir string, args ...string) *result {
	t.Helper()
	cmd := exec.Command(toolBinary, append(h.expandAll(args), "--json")...)
	cmd.Dir, cmd.Env = dir, h.env
	p := &proc{cmd: cmd, out: new(bytes.Buffer), errb: new(bytes.Buffer), done: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = p.out, p.errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); close(p.done) }()
	r, err := h.finish(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Scenario 38: rebase with BRANCH checked out nowhere and no flag. From a
// worktree the current directory is the source and work worktree and BRANCH
// is the result; from a path that is not a worktree the refusal is
// no-worktree and nothing changes.
func TestTier1RebaseBranchNowhereNoFlag(t *testing.T) {
	t.Run("38-from-a-worktree", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeDiverged, Layout: []Worktree{{Name: "spare", Detach: true}}})
		r := runFrom(t, h, h.wtPath("spare"), "rebase", "feature/x", "--onto", "main")
		expect(t, r, 0, "rebased", "")
		h.mustGit(h.repo, "worktree", "list")
		if got := h.mustGit(h.repo, "rev-parse", "refs/heads/feature/x~1"); got != shapes[ShapeDiverged].facts.Tips["main"] {
			t.Fatalf("feature/x~1 = %s, want main's tip", got)
		}
		if got := h.mustGit(h.repo, "rev-parse", "refs/heads/main"); got != shapes[ShapeDiverged].facts.Tips["main"] {
			t.Fatalf("main moved to %s", got)
		}
		if got := h.head(h.wtPath("spare")); got != "detached:"+shapes[ShapeDiverged].facts.Tips["main"] {
			t.Fatalf("spare worktree ended at %s, want it restored", got)
		}
		h.noProblems()
	})
	t.Run("38-from-a-non-worktree", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeDiverged})
		// The .git directory is inside the clone but is not a worktree.
		r := runFrom(t, h, filepath.Join(h.repo, ".git"), "rebase", "feature/x", "--onto", "main")
		expect(t, r, 22, "refused", "no-worktree")
		for ref, want := range map[string]string{
			"refs/heads/main":      shapes[ShapeDiverged].facts.Tips["main"],
			"refs/heads/feature/x": shapes[ShapeDiverged].facts.Tips["feature/x"],
		} {
			if got := h.mustGit(h.repo, "rev-parse", ref); got != want {
				t.Fatalf("%s = %s, want %s: the refusal must change nothing", ref, got, want)
			}
		}
		h.noProblems()
	})
}

// Scenario 39: land with TARGET checked out nowhere, BRANCH in its own
// worktree and no --target-worktree: TARGET moves by update-ref, no worktree
// is switched.
func TestTier1LandTargetCheckedOutNowhere(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "39-target-checked-out-nowhere",
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Steps: []Step{
			// Free main in the main worktree.
			{Git: []string{"checkout", "-q", "--detach", "main"}},
			{Run: []string{"land", "feature/x"}},
		},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "path_taken": "fast-forward",
				"branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "removed"},
			},
			Snapshot: Snapshot{
				Refs: mainMoveTo,
				Worktrees: map[string]WorktreeState{
					// No worktree was switched: the main worktree is still
					// detached at TARGET's old tip.
					"main": {Head: "detached:" + tipMain(), CheckStatus: true},
				},
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		},
	})
}
