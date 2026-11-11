//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tier 1 scenarios 11 to 19 of section 15.3: patches already upstream,
// conflicts resolved by hand and by a configured resolver, the abort path,
// hook failure and autofix, the fail-fast and dirty refusals and a busy lock.
// Each runs the real binary against real git in a fixture copy; the outcome
// subset, the snapshot and the post-condition must all hold.

// checkNow compares the current result and the repository against exp, and
// reports every difference. It does not apply the post-condition, which only
// holds once the operation is over (see done).
func checkNow(t *testing.T, h *harness, r *result, exp Expected) {
	t.Helper()
	h.sc.Expected, h.result = exp, r
	var problems []string
	problems = append(problems, h.compareOutcome()...)
	problems = append(problems, h.compareSnapshot()...)
	for _, p := range problems {
		t.Error(p)
	}
}

// done checks the final expectation and then the post-condition.
func done(t *testing.T, h *harness, r *result, exp Expected) {
	t.Helper()
	checkNow(t, h, r, exp)
	h.noProblems()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startRefs is the expectation that BRANCH and TARGET are where the fixture
// put them.
func startRefs() map[string]string {
	return map[string]string{"refs/heads/main": "@main", "refs/heads/feature/x": "@feature/x"}
}

// resolveByHand resolves app.txt in the stopped work worktree, as a person
// would.
func resolveByHand(t *testing.T, h *harness, wt string) {
	t.Helper()
	writeFile(t, filepath.Join(wt, "app.txt"), "line1\nresolved\nline3\n")
	h.mustGit(wt, "add", "app.txt")
}

func conflictStop(t *testing.T, h *harness) {
	t.Helper()
	r := h.run("land", "feature/x")
	expect(t, r, 4, "stopped", "conflict")
	checkNow(t, h, r, Expected{Outcome: map[string]any{"stage": "rebasing"}, Snapshot: Snapshot{Refs: startRefs()}})
}

// Scenario 11: every patch of BRANCH is already upstream (the fixture's main
// holds a patch-equivalent copy of the first commit; the test cherry-picks the
// second), so the rebase drops them all: nothing-to-land by patch, with
// dropped_commits listing both.
func TestTier1PatchesAlreadyUpstream(t *testing.T) {
	h := newHarness(t, Scenario{Shape: ShapeCherryPicked, Layout: srcOnBr})
	h.mustGit(h.repo, "cherry-pick", "feature/x")
	mainBefore := h.mustGit(h.repo, "rev-parse", "main")
	r := h.run("land", "feature/x", "--source-worktree", h.wtPath("src"))
	done(t, h, r, Expected{
		Outcome: map[string]any{
			"outcome": "nothing-to-land", "exit": 0, "branch_fate": "deleted",
			"worktree_fate": map[string]any{"fate": "removed"},
		},
		Snapshot: Snapshot{
			Refs:        map[string]string{"refs/heads/main": mainBefore, "refs/heads/feature/x": ""},
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
		},
	})
	if got := len(strs(r.json["dropped_commits"])); got != 2 {
		t.Errorf("dropped_commits lists %d commits, want 2: %s", got, r.stdout)
	}
}

// Scenario 12: a conflict stops the land; a person resolves it in the work
// worktree and --continue lands, run from the main worktree, from the work
// worktree itself without BRANCH, and from a third worktree.
func TestTier1ConflictResolvedByHandThenContinue(t *testing.T) {
	landed := Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted"},
		Snapshot: Snapshot{
			Refs:        map[string]string{"refs/heads/feature/x": ""},
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
		},
	}
	layout := []Worktree{{Name: "src", Branch: "feature/x"}, {Name: "spare", Detach: true}}
	for _, tc := range []struct {
		name string
		cwd  func(h *harness) string // "" runs through the harness (cwd is the main worktree)
		args []string
	}{
		{"from-the-main-worktree", func(*harness) string { return "" }, []string{"land", "feature/x", "--continue"}},
		{"from-the-work-worktree-without-branch", func(h *harness) string { return h.wtPath("src") }, []string{"land", "--continue"}},
		{"from-another-worktree", func(h *harness) string { return h.wtPath("spare") }, []string{"land", "feature/x", "--continue"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Scenario{Shape: ShapeConflict, Layout: layout})
			conflictStop(t, h)
			resolveByHand(t, h, h.wtPath("src"))
			var r *result
			if dir := tc.cwd(h); dir == "" {
				r = h.run(tc.args...)
			} else {
				r = runFrom(t, h, dir, tc.args...)
			}
			done(t, h, r, landed)
			if got := h.mustGit(h.repo, "show", "main:app.txt"); got != "line1\nresolved\nline3" {
				t.Errorf("main:app.txt = %q, want the hand resolution", got)
			}
		})
	}
}

// Scenario 13: a conflict, then --abort: every ref and worktree is back at
// the start and no work branch is left.
func TestTier1ConflictThenAbort(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "13-conflict-then-abort",
		Shape:  ShapeConflict,
		Layout: srcOnBr,
		Steps: []Step{
			{Run: []string{"land", "feature/x"}},
			{Run: []string{"land", "feature/x", "--abort"}},
		},
		Expected: Expected{
			Outcome: map[string]any{"outcome": "aborted", "exit": 0},
			Snapshot: Snapshot{
				Refs: startRefs(),
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
				TargetTree: "@tree:main",
			},
		},
	})
	t.Run("13-no-work-branch", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeConflict, Layout: srcOnBr})
		conflictStop(t, h)
		expect(t, h.run("land", "feature/x", "--abort"), 0, "aborted", "")
		if got := h.mustGit(h.repo, "for-each-ref", "--format=%(refname)", "refs/heads/pg-git-lander/"); got != "" {
			t.Errorf("work branch left behind: %s", got)
		}
		h.noProblems()
	})
}

// Scenario 14: a configured resolver (a fake script that writes the resolved
// file) settles the conflict and the land finishes without stopping.
func TestTier1ConflictResolvedByConfiguredResolver(t *testing.T) {
	runScenario(t, Scenario{
		Name:   "14-resolver",
		Shape:  ShapeConflict,
		Layout: srcOnBr,
		Config: `
[[repo]]
root = "{repo}"

[[repo.conflict]]
glob = "app.txt"
resolve = ["{hooks}/resolver"]
`,
		Hooks: map[string]string{"resolver": `printf 'line1\nresolved\nline3\n' > "$PG_GIT_LANDER_PATH"`},
		Steps: []Step{{Run: []string{"land", "feature/x"}}},
		Expected: Expected{
			Outcome: map[string]any{
				"outcome": "landed", "exit": 0, "path_taken": "rebase", "branch_fate": "deleted",
				"stats": map[string]any{"conflict_rounds": 1},
			},
			Snapshot: Snapshot{
				Refs:        map[string]string{"refs/heads/feature/x": ""},
				Worktrees:   cleanMain,
				NoWorktrees: []string{"src"},
			},
		},
	})
	t.Run("14-resolution-is-in-main", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape:  ShapeConflict,
			Layout: srcOnBr,
			Config: "[[repo]]\nroot = \"{repo}\"\n\n[[repo.conflict]]\nglob = \"app.txt\"\nresolve = [\"{hooks}/resolver\"]\n",
			Hooks:  map[string]string{"resolver": `printf 'line1\nresolved\nline3\n' > "$PG_GIT_LANDER_PATH"`},
		})
		r := h.run("land", "feature/x")
		expect(t, r, 0, "landed", "")
		if res, _ := r.json["resolved"].([]any); len(res) != 1 {
			t.Errorf("resolved lists %d entries, want 1: %s", len(res), r.stdout)
		}
		if got := h.mustGit(h.repo, "show", "main:app.txt"); got != "line1\nresolved\nline3" {
			t.Errorf("main:app.txt = %q", got)
		}
		h.noProblems()
	})
}

// Scenario 15: a before_move hook fails; BRANCH and TARGET are untouched at
// the failure; after the fix --continue lands.
func TestTier1HookFailsAtBeforeMoveThenContinue(t *testing.T) {
	h := newHarness(t, Scenario{
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
	expect(t, r, 4, "stopped", "hook-failed")
	checkNow(t, h, r, Expected{
		Outcome: map[string]any{"stage": "before_move"},
		Snapshot: Snapshot{
			Refs:       startRefs(),
			TargetTree: "@tree:main",
		},
		Stopped: true,
	})
	writeFile(t, filepath.Join(h.marks, "fixed"), "")
	r = h.run("land", "feature/x", "--continue")
	done(t, h, r, Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted"},
		Snapshot: Snapshot{
			Refs:        mainMoveTo,
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
			TargetTree:  "@tree:feature/x",
		},
	})
}

// Scenario 16: a hook that rewrites a tracked file (a formatter) and fails is
// autofixed: the tool commits the change and runs the hook again, and the fix
// commit is part of the result.
func TestTier1HookAutofix(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape:  ShapeAhead,
		Layout: srcOnBr,
		Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
before_move = [{ run = ["{hooks}/format"], timeout = "60s" }]
`,
		Hooks: map[string]string{"format": `
if grep -q formatted x1.txt; then exit 0; fi
echo formatted >> x1.txt
exit 1
`},
	})
	r := h.run("land", "feature/x")
	done(t, h, r, Expected{
		Outcome: map[string]any{
			"outcome": "landed", "exit": 0, "branch_fate": "deleted",
			"stats": map[string]any{"autofix_commits": 1},
		},
		Snapshot: Snapshot{
			Refs:        map[string]string{"refs/heads/feature/x": ""},
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
		},
	})
	if af, _ := r.json["autofix"].([]any); len(af) != 1 {
		t.Errorf("autofix lists %d entries, want 1: %s", len(af), r.stdout)
	}
	if got := h.mustGit(h.repo, "log", "-1", "--format=%s", "main"); !strings.HasPrefix(got, "style: apply before_move fixes made during land") {
		t.Errorf("main's tip is %q, want the autofix commit", got)
	}
	if got := h.mustGit(h.repo, "show", "main:x1.txt"); got != "one\nformatted" {
		t.Errorf("main:x1.txt = %q, want the autofixed content", got)
	}
	// Both of BRANCH's own commits are below the autofix commit.
	if got := h.mustGit(h.repo, "rev-parse", "main~1"); got != shapes[ShapeAhead].facts.Tips["feature/x"] {
		t.Errorf("main~1 = %s, want BRANCH's original tip", got)
	}
}

// Scenario 17: fail fast. The derived source worktree is the current
// directory, or a directory inside it: the refusal names the three fixes and
// changes nothing, and each fix works.
func TestTier1FailFastDerivedSourceIsCwd(t *testing.T) {
	refused := func(t *testing.T, h *harness, cwd string) {
		t.Helper()
		r := runFrom(t, h, cwd, "land", "feature/x")
		expect(t, r, 5, "refused", "cwd-would-be-removed")
		msg, _ := r.json["message"].(string)
		for _, fix := range []string{"another path", "--source-worktree", "--keep-source-worktree"} {
			if !strings.Contains(msg, fix) {
				t.Errorf("the message %q does not name the fix %q", msg, fix)
			}
		}
		done(t, h, r, Expected{
			Snapshot: Snapshot{
				Refs: startRefs(),
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
			},
		})
	}
	setup := func(t *testing.T) (*harness, string, string) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		src := h.wtPath("src")
		sub := filepath.Join(src, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		return h, src, sub
	}
	t.Run("refused-from-the-worktree", func(t *testing.T) {
		h, src, _ := setup(t)
		refused(t, h, src)
	})
	t.Run("refused-from-a-subdirectory", func(t *testing.T) {
		h, _, sub := setup(t)
		refused(t, h, sub)
	})
	landedRemoved := Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "removed"}},
		Snapshot: Snapshot{
			Refs:        mainMoveTo,
			Worktrees:   cleanMain,
			NoWorktrees: []string{"src"},
			TargetTree:  "@tree:feature/x",
		},
	}
	t.Run("fix-run-from-another-path", func(t *testing.T) {
		h, _, _ := setup(t)
		done(t, h, h.run("land", "feature/x"), landedRemoved)
	})
	t.Run("fix-pass-source-worktree", func(t *testing.T) {
		h, src, sub := setup(t)
		// Consent from inside the worktree: the directory is removed with it.
		r := runFrom(t, h, sub, "land", "feature/x", "--source-worktree", src)
		done(t, h, r, landedRemoved)
	})
	t.Run("fix-keep-source-worktree", func(t *testing.T) {
		h, src, _ := setup(t)
		r := runFrom(t, h, src, "land", "feature/x", "--keep-source-worktree")
		done(t, h, r, Expected{
			Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted", "worktree_fate": map[string]any{"fate": "kept"}},
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
}

// Scenario 18: a dirty source worktree, a dirty destination worktree and a
// path that is not a worktree are each refused, and nothing changes.
func TestTier1DirtyAndNotAWorktreeRefusals(t *testing.T) {
	t.Run("dirty-source", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		writeFile(t, filepath.Join(h.wtPath("src"), "x1.txt"), "changed\n")
		r := h.run("land", "feature/x")
		expect(t, r, 5, "refused", "dirty")
		checkNow(t, h, r, Expected{
			Outcome: map[string]any{"role": "source"},
			Snapshot: Snapshot{
				Refs: startRefs(),
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true},
					"src":  {Head: "branch:feature/x", CheckStatus: true, Status: "M x1.txt"},
				},
			},
		})
		h.mustGit(h.wtPath("src"), "checkout", "--", "x1.txt")
		h.noProblems()
	})
	t.Run("dirty-destination", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		writeFile(t, filepath.Join(h.repo, "README.md"), "changed\n")
		r := h.run("land", "feature/x")
		expect(t, r, 5, "refused", "dirty")
		checkNow(t, h, r, Expected{
			Outcome: map[string]any{"role": "destination"},
			Snapshot: Snapshot{
				Refs: startRefs(),
				Worktrees: map[string]WorktreeState{
					"main": {Head: "branch:main", CheckStatus: true, Status: "M README.md"},
					"src":  {Head: "branch:feature/x", CheckStatus: true},
				},
			},
		})
		h.mustGit(h.repo, "checkout", "--", "README.md")
		h.noProblems()
	})
	t.Run("not-a-worktree", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead})
		notWT := filepath.Join(h.dir, "home")
		r := h.run("land", "feature/x", "--source-worktree", notWT)
		expect(t, r, 5, "refused", "not-a-worktree")
		done(t, h, r, Expected{
			Outcome: map[string]any{"role": "source"},
			Snapshot: Snapshot{
				Refs:      startRefs(),
				Worktrees: cleanMain,
			},
		})
	})
}

// Scenario 19: a lock held by a live process: a second landing into the same
// TARGET is busy and changes nothing; the holder then finishes normally.
func TestTier1BusyWhileLockHeldByLiveProcess(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape:  ShapeAhead,
		Fifos:  []string{"up", "go"},
		Layout: srcOnBr,
		Config: blockingConfig("before_move"),
		Hooks:  map[string]string{"blocker": blockingHook},
	})
	other := h.addBranch("other")
	otherTip := h.mustGit(h.repo, "rev-parse", "other")

	h.start("holder", "land", "feature/x")
	h.await("up") // the holder is inside before_move, holding main's lock

	r := h.run("land", "other", "--wait", "1s")
	expect(t, r, 3, "busy", "")
	holder, _ := r.json["holder"].(map[string]any)
	if holder == nil || holder["branch"] != "feature/x" {
		t.Errorf("busy does not name the holder: %s", r.stdout)
	}
	checkNow(t, h, r, Expected{
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/main": "@main", "refs/heads/other": otherTip},
			Worktrees: map[string]WorktreeState{
				"main": {Head: "branch:main"},
			},
		},
	})
	if st := h.mustGit(other, "status", "--porcelain"); st != "" {
		t.Errorf("the other worktree is dirty: %s", st)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("the other worktree is gone: %v", err)
	}

	h.release("go")
	done(t, h, h.wait("holder"), Expected{
		Outcome: map[string]any{"outcome": "landed", "exit": 0},
		Snapshot: Snapshot{
			Refs: map[string]string{"refs/heads/main": "@feature/x", "refs/heads/other": otherTip},
		},
	})
}
