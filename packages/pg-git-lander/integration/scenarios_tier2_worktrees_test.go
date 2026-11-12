//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tier 2 scenarios 29 to 34, 40 and 41 of section 15.3: worktree and layout
// scenarios (source worktree removal failing, BRANCH checked out elsewhere,
// mismatches, namespace, usage errors, unsupported layouts, BRANCH changed
// during an operation, pull). Each runs the real binary against real git; the
// outcome subset, the snapshot and the post-condition must all hold.

// warningWords returns the warning words of a result's outcome JSON.
func warningWords(r *result) []string {
	var out []string
	ws, _ := r.json["warnings"].([]any)
	for _, w := range ws {
		if m, ok := w.(map[string]any); ok {
			if s, ok := m["warning"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func hasWarning(r *result, word string) bool {
	for _, w := range warningWords(r) {
		if w == word {
			return true
		}
	}
	return false
}

// warningDetail returns the reason and detail text of the first warning with
// the given word.
func warningDetail(r *result, word string) string {
	ws, _ := r.json["warnings"].([]any)
	for _, w := range ws {
		if m, ok := w.(map[string]any); ok && m["warning"] == word {
			a, _ := m["reason"].(string)
			b, _ := m["detail"].(string)
			return a + " " + b
		}
	}
	return ""
}

// checkExpected compares the snapshot of exp and enforces the post-condition,
// the way execScenario does for a declarative scenario, for tests that must
// inspect the outcome JSON themselves.
func checkExpected(t *testing.T, h *harness, exp Expected) {
	t.Helper()
	h.sc.Expected = exp
	for _, p := range h.compareSnapshot() {
		t.Error(p)
	}
	for _, p := range h.postCondition() {
		t.Error(p)
	}
}

// unchanged is the snapshot of a refusal on the ahead shape: no ref moved.
func unchangedAhead() Snapshot {
	return Snapshot{
		Refs:      map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": "@feature/x"},
		Worktrees: cleanMain,
	}
}

// Scenario 29: removing the source worktree fails (a locked worktree): the
// land still succeeds, with source-worktree-not-removed, and the worktree is
// parked on the placeholder branch.
func TestTier2SourceWorktreeRemovalFails(t *testing.T) {
	t.Run("29-locked", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		h.mustGit(h.repo, "worktree", "lock", h.wtPath("src"))
		r := h.run("land", "feature/x", "--source-worktree", h.wtPath("src"))
		expect(t, r, 0, "landed", "")
		if !hasWarning(r, "source-worktree-not-removed") {
			t.Errorf("warnings %v, want source-worktree-not-removed: %s", warningWords(r), r.stdout)
		}
		fate, _ := r.json["worktree_fate"].(map[string]any)
		if fate["fate"] == "removed" {
			t.Errorf("worktree_fate %v, want the failed removal", fate)
		}
		if got := h.head(h.wtPath("src")); !strings.HasPrefix(got, "branch:pg-git-lander/idle/") {
			t.Errorf("source worktree head %s, want parked on a placeholder branch", got)
		}
		checkExpected(t, h, Expected{
			Snapshot: Snapshot{
				Refs: mainMoveTo,
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {CheckStatus: true},
				},
				TargetTree: "@tree:feature/x",
			},
			Placeholder: true,
		})
	})
	t.Run("29-nested-worktree", func(t *testing.T) {
		// A second worktree nested inside the source worktree: git refuses to
		// remove the outer one while the inner one is registered... or removes
		// both; either way the land itself must succeed and leave a clean state.
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		inner := filepath.Join(h.wtPath("src"), "inner")
		h.mustGit(h.repo, "worktree", "add", "-q", "--detach", inner, "main")
		// The inner worktree is an untracked directory of the source worktree
		// unless it is excluded; exclude it so the dirty check passes.
		excl := filepath.Join(h.repo, ".git", "worktrees", "wt-src", "info")
		if err := os.MkdirAll(excl, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(excl, "exclude"), []byte("/inner\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := h.run("land", "feature/x", "--source-worktree", h.wtPath("src"))
		expect(t, r, 0, "landed", "")
		if !hasWarning(r, "source-worktree-not-removed") {
			t.Errorf("warnings %v, want source-worktree-not-removed: %s", warningWords(r), r.stdout)
		}
		for ref, want := range mainMoveTo {
			if want = h.fact(want); h.refOr(ref) != want {
				t.Errorf("%s = %q, want %q", ref, h.refOr(ref), want)
			}
		}
	})
}

// refOr returns the id of ref, or "" when it does not exist.
func (h *harness) refOr(ref string) string {
	out, err := h.gitHere(h.repo, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

const keepBranchWorkSpare = "--work-worktree"

// Scenario 30: BRANCH is checked out in a worktree other than the work
// worktree and has to move (--keep-source-branch): the worktree is updated in
// place with reset --keep when clean; when it is dirty (tracked changes) or git
// refuses, BRANCH stays and the warning is branch-not-moved.
func TestTier2BranchCheckedOutElsewhereMoveNeeded(t *testing.T) {
	layout := []Worktree{{Name: "src", Branch: "feature/x"}, {Name: "spare", Detach: true}}
	runScenario(t, Scenario{
		Name:   "30-clean-holder-updated-in-place",
		Shape:  ShapeDiverged,
		Layout: layout,
		Steps: []Step{{Run: []string{
			"land", "feature/x", "--source-worktree", "{wt:src}", "--work-worktree", "{wt:spare}",
			"--keep-source-worktree", "--keep-source-branch",
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
					"main":  {Head: "branch:main", CheckStatus: true},
					"src":   {Head: "branch:feature/x", CheckStatus: true},
					"spare": {Head: "detached:" + shapes[ShapeDiverged].facts.Tips["main"], CheckStatus: true},
				},
			},
		},
	})
	t.Run("30-dirty-holder-warns", func(t *testing.T) {
		// A hook dirties a tracked file of the holder while the operation
		// runs, so the move is refused and BRANCH stays where it was.
		h := newHarness(t, Scenario{
			Shape:  ShapeDiverged,
			Layout: layout,
			Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\nbefore_move = [{ run = [\"{hooks}/dirty\"], timeout = \"60s\" }]\n",
			Hooks:  map[string]string{"dirty": `echo changed >> "{wt:src}/README.md"`},
		})
		r := h.run("land", "feature/x", "--source-worktree", h.wtPath("src"), keepBranchWorkSpare, h.wtPath("spare"),
			"--keep-source-worktree", "--keep-source-branch")
		expect(t, r, 0, "landed", "")
		if !hasWarning(r, "branch-not-moved") {
			t.Fatalf("warnings %v, want branch-not-moved: %s", warningWords(r), r.stdout)
		}
		if got := h.refOr("refs/heads/feature/x"); got != shapes[ShapeDiverged].facts.Tips["feature/x"] {
			t.Errorf("feature/x = %s, want it left at its own tip", got)
		}
		// The holder keeps its tracked change; the rest of the post-condition
		// (fsck, no stale ref locks) still holds.
		checkExpected(t, h, Expected{Stopped: true})
	})
}

// Scenario 31: --source-worktree is given but BRANCH is checked out in another
// worktree: refusal, nothing changed.
func TestTier2SourceWorktreeButBranchElsewhere(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "31-source-worktree-mismatch",
		Shape:  ShapeAhead,
		Layout: []Worktree{{Name: "src", Branch: "feature/x"}, {Name: "spare", Detach: true}},
		Steps:  []Step{{Run: []string{"land", "feature/x", "--source-worktree", "{wt:spare}"}}},
		Expected: Expected{
			Outcome: map[string]any{"outcome": "refused", "exit": 5, "reason": "source-worktree-mismatch"},
			Snapshot: Snapshot{
				Refs: map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": "@feature/x"},
				Worktrees: map[string]WorktreeState{
					"main":  {Head: "branch:main", CheckStatus: true},
					"src":   {Head: "branch:feature/x", CheckStatus: true},
					"spare": {Head: "detached:" + tipMain(), CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		},
	})
}

// Scenario 32: BRANCH under pg-git-lander/ is refused for both operations.
func TestTier2BranchInToolNamespace(t *testing.T) {
	for _, args := range [][]string{
		{"land", "pg-git-lander/foo"},
		{"rebase", "pg-git-lander/foo", "--onto", "main"},
	} {
		name := "32-" + args[0]
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, Scenario{Shape: ShapeAhead})
			h.mustGit(h.repo, "branch", "pg-git-lander/foo", "feature/x")
			r := h.run(args...)
			expect(t, r, 5, "refused", "namespace-collision")
			h.mustGit(h.repo, "branch", "-q", "-D", "pg-git-lander/foo")
			checkExpected(t, h, Expected{Snapshot: unchangedAhead()})
		})
	}
	t.Run("32-target-in-namespace", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead})
		h.mustGit(h.repo, "branch", "pg-git-lander/t", "main")
		r := h.run("land", "feature/x", "--into", "pg-git-lander/t")
		expect(t, r, 5, "refused", "namespace-collision")
		h.mustGit(h.repo, "branch", "-q", "-D", "pg-git-lander/t")
		checkExpected(t, h, Expected{Snapshot: unchangedAhead()})
	})
}

// Scenario 33: the keep flags are a usage error with rebase; nothing changes.
func TestTier2KeepFlagsWithRebase(t *testing.T) {
	for _, flag := range []string{"--keep-source-worktree", "--keep-source-branch"} {
		flag := flag
		runScenario(t, Scenario{
			Name:   "33-rebase" + flag,
			Shape:  ShapeAhead,
			Layout: srcOnBr,
			Steps:  []Step{{Run: []string{"rebase", "feature/x", "--onto", "main", flag}}},
			Expected: Expected{
				Outcome: map[string]any{"outcome": "usage-error", "exit": 2},
				Snapshot: Snapshot{
					Refs: map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": "@feature/x"},
					Worktrees: map[string]WorktreeState{
						"main": {Head: "branch:main", CheckStatus: true},
						"src":  {Head: "branch:feature/x", CheckStatus: true},
					},
				},
			},
		})
	}
}

// Scenario 34: unsupported layouts (bare clone, core.worktree) and a missing
// git identity are clear refusals that change nothing.
func TestTier2UnsupportedLayoutsAndNoIdentity(t *testing.T) {
	t.Run("34-bare", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead})
		bare := filepath.Join(h.dir, "bare.git")
		h.mustGit(h.dir, "clone", "-q", "--bare", h.repo, bare)
		r := h.run("land", "feature/x", "--repo", bare)
		expect(t, r, 5, "refused", "unsupported-layout")
		if r.json["detail"] != "bare" {
			t.Errorf("detail = %v, want bare: %s", r.json["detail"], r.stdout)
		}
		if got := h.mustGit(bare, "rev-parse", "refs/heads/main"); got != shapes[ShapeAhead].facts.Tips["main"] {
			t.Errorf("bare main moved to %s", got)
		}
		checkExpected(t, h, Expected{Snapshot: unchangedAhead()})
	})
	t.Run("34-core-worktree", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead})
		h.mustGit(h.repo, "config", "--local", "core.worktree", h.repo)
		r := h.run("land", "feature/x")
		h.mustGit(h.repo, "config", "--local", "--unset", "core.worktree")
		expect(t, r, 5, "refused", "unsupported-layout")
		if r.json["detail"] != "core.worktree" {
			t.Errorf("detail = %v, want core.worktree: %s", r.json["detail"], r.stdout)
		}
		checkExpected(t, h, Expected{Snapshot: unchangedAhead()})
	})
	t.Run("34-no-identity", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead})
		empty := filepath.Join(h.dir, "empty.gitconfig")
		if err := os.WriteFile(empty, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		// Later entries win: git has no global identity and may not guess one.
		toolEnv := append([]string{}, h.env...)
		h.env = append(h.env, "GIT_CONFIG_GLOBAL="+empty,
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.useConfigOnly", "GIT_CONFIG_VALUE_0=true")
		r := h.run("land", "feature/x")
		h.env = toolEnv
		expect(t, r, 5, "refused", "no-git-identity")
		checkExpected(t, h, Expected{Snapshot: unchangedAhead()})
	})
}

// Scenario 40: a commit is added to BRANCH from another worktree while the
// operation runs (a before_move hook): BRANCH is kept at its new tip with the
// warning branch-not-deleted (reason moved), the outcome is landed and the
// source worktree is removed or parked as the flags say.
func TestTier2CommitAddedToBranchDuringLand(t *testing.T) {
	const cfg = "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\nbefore_move = [{ run = [\"{hooks}/extra\"], timeout = \"60s\" }]\n"
	// The hook runs in the work worktree (the source worktree, now on the work
	// branch), so BRANCH is checked out nowhere; the commit is made from the
	// worktree "other".
	hook := `[ -e "$PGL_MARKERS/done" ] && exit 0
touch "$PGL_MARKERS/done"
env -u GIT_DIR -u GIT_INDEX_FILE -u GIT_WORK_TREE git -C "{wt:other}" switch -q feature/x &&
env -u GIT_DIR -u GIT_INDEX_FILE -u GIT_WORK_TREE git -C "{wt:other}" commit -q --allow-empty -m "added during the land" &&
env -u GIT_DIR -u GIT_INDEX_FILE -u GIT_WORK_TREE git -C "{wt:other}" switch -q --detach main`
	layout := []Worktree{{Name: "src", Branch: "feature/x"}, {Name: "other", Detach: true}}
	t.Run("40-default-flags", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: layout, Config: cfg, Hooks: map[string]string{"extra": hook}})
		r := h.run("land", "feature/x", "--source-worktree", h.wtPath("src"))
		expect(t, r, 0, "landed", "")
		if !hasWarning(r, "branch-not-deleted") || !strings.Contains(warningDetail(r, "branch-not-deleted"), "moved") {
			t.Errorf("warnings %v, want branch-not-deleted with reason moved: %s", warningWords(r), r.stdout)
		}
		if r.json["branch_fate"] != "kept" {
			t.Errorf("branch_fate = %v, want kept", r.json["branch_fate"])
		}
		newTip := h.refOr("refs/heads/feature/x")
		if newTip == "" || newTip == shapes[ShapeAhead].facts.Tips["feature/x"] {
			t.Errorf("feature/x = %q, want a new tip above the landed one", newTip)
		}
		if h.mustGit(h.repo, "rev-parse", newTip+"~1") != shapes[ShapeAhead].facts.Tips["feature/x"] {
			t.Errorf("the added commit is not directly above the landed tip")
		}
		checkExpected(t, h, Expected{
			Snapshot: Snapshot{
				Refs:        map[string]string{"refs/heads/main": "@feature/x"},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
				TargetTree:  "@tree:feature/x",
			},
		})
	})
	t.Run("40-keep-source-worktree", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: layout, Config: cfg, Hooks: map[string]string{"extra": hook}})
		r := h.run("land", "feature/x", "--source-worktree", h.wtPath("src"), "--keep-source-worktree")
		expect(t, r, 0, "landed", "")
		if !hasWarning(r, "branch-not-deleted") || !strings.Contains(warningDetail(r, "branch-not-deleted"), "moved") {
			t.Errorf("warnings %v, want branch-not-deleted with reason moved: %s", warningWords(r), r.stdout)
		}
		if h.refOr("refs/heads/feature/x") == "" {
			t.Errorf("feature/x was deleted")
		}
		checkExpected(t, h, Expected{
			Snapshot: Snapshot{
				Refs: map[string]string{"refs/heads/main": "@feature/x"},
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {CheckStatus: true},
				},
				TargetTree: "@tree:feature/x",
			},
			Placeholder: true,
		})
	})
}

// Scenario 41: pull. With an origin fixture whose remote advanced and was
// fetched, rebase main --onto origin/main replays the local commits on
// origin/main; the main worktree is back on main, clean.
func TestTier2PullOntoOrigin(t *testing.T) {
	runScenario(t, Scenario{
		Name:  "41-pull-onto-origin",
		Shape: ShapeOrigin,
		Steps: []Step{
			{Git: []string{"mv", "README.md", "moved.md"}},
			{Git: []string{"commit", "-q", "-m", "local one"}},
			{AdvanceRemote: "remote.txt=remote"},
			{Run: []string{"rebase", "main", "--onto", "origin/main"}},
		},
		Expected: Expected{
			Outcome: map[string]any{"outcome": "rebased", "exit": 0, "operation": "rebase"},
			Snapshot: Snapshot{
				Refs: map[string]string{
					// The local commit sits directly on origin/main.
					"refs/heads/main~1": "=refs/remotes/origin/main",
				},
				Worktrees: cleanMain,
			},
		},
	})
}
