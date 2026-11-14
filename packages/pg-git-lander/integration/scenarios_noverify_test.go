//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --no-verify scenarios (modelled on scenario 15): the real binary against
// real git. The flag skips pre-merge-verify and git's own hooks for the run;
// post-rebase-fix, the conflict rules with their verify commands and the
// built-in preconditions still run.

const gateConfig = `
[[repo]]
root = "{repo}"

[repo.hooks.land]
pre-merge-verify = [{ run = ["{hooks}/gate"], timeout = "60s" }]
`

var landedPastGate = Expected{
	Outcome: map[string]any{"outcome": "landed", "exit": 0, "branch_fate": "deleted"},
	Snapshot: Snapshot{
		Refs:        mainMoveTo,
		Worktrees:   cleanMain,
		NoWorktrees: []string{"src"},
		TargetTree:  "@tree:feature/x",
	},
}

// verifySkippedEntries reads the verify_skipped field of the outcome.
func verifySkippedEntries(t *testing.T, r *result) []any {
	t.Helper()
	list, ok := r.json["verify_skipped"].([]any)
	if !ok {
		t.Fatalf("verify_skipped is not a list: %s", r.stdout)
	}
	return list
}

// A land with a failing gate stops; the same land with --no-verify lands and
// says so.
func TestNoVerifyLandsPastAFailingGate(t *testing.T) {
	t.Run("without-the-flag-it-stops", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr, Config: gateConfig, Hooks: map[string]string{"gate": `exit 1`}})
		r := h.run("land", "feature/x")
		expect(t, r, 21, "stopped", "hook-failed")
		if hasWarning(r, "verify-skipped") {
			t.Errorf("the warning is there without the flag: %s", r.stdout)
		}
	})
	t.Run("with-the-flag-it-lands", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr, Config: gateConfig, Hooks: map[string]string{"gate": `touch "$PGL_MARKERS/gate-ran"; exit 1`}})
		r := h.run("land", "feature/x", "--no-verify")
		done(t, h, r, landedPastGate)
		if !hasWarning(r, "verify-skipped") || !strings.Contains(warningDetail(r, "verify-skipped"), "no-verify 1 pre-merge-verify entries skipped at ") {
			t.Errorf("warnings %v: %s", warningWords(r), r.stdout)
		}
		if got := verifySkippedEntries(t, r); len(got) != 1 {
			t.Errorf("verify_skipped = %v", got)
		}
		if _, err := os.Stat(filepath.Join(h.marks, "gate-ran")); err == nil {
			t.Error("the gate ran under --no-verify")
		}
		skipped := eventsOfType(h.events(), "verify_skipped")
		if len(skipped) != 1 || skipped[0]["count"] != float64(1) {
			t.Errorf("verify_skipped events = %v", skipped)
		}
	})
	t.Run("a-land-stopped-at-the-gate-continues-with-the-flag", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr, Config: gateConfig, Hooks: map[string]string{"gate": `exit 1`}})
		expect(t, h.run("land", "feature/x"), 21, "stopped", "hook-failed")
		r := h.run("land", "feature/x", "--continue", "--no-verify")
		done(t, h, r, landedPastGate)
		if !hasWarning(r, "verify-skipped") {
			t.Errorf("no verify-skipped warning: %s", r.stdout)
		}
	})
	t.Run("a-timed-out-gate-continues-with-the-flag", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape: ShapeAhead, Layout: srcOnBr,
			Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\npre-merge-verify = [{ run = [\"{hooks}/gate\"], timeout = \"1s\" }]\n",
			Hooks:  map[string]string{"gate": `exec sleep 30`},
		})
		expect(t, h.run("land", "feature/x"), 21, "stopped", "timeout")
		done(t, h, h.run("land", "feature/x", "--continue", "--no-verify"), landedPastGate)
	})
	t.Run("zero-entries-still-warns", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		r := h.run("land", "feature/x", "--no-verify")
		expect(t, r, 0, "landed", "")
		if !hasWarning(r, "verify-skipped") || len(verifySkippedEntries(t, r)) != 0 {
			t.Errorf("warnings %v verify_skipped %v", warningWords(r), r.json["verify_skipped"])
		}
		if !strings.Contains(warningDetail(r, "verify-skipped"), "0 pre-merge-verify entries skipped") {
			t.Errorf("detail = %q", warningDetail(r, "verify-skipped"))
		}
	})
}

// --abort and --quit take no --no-verify: usage errors, nothing changes.
func TestNoVerifyWithAbortAndQuitIsAUsageError(t *testing.T) {
	h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
	for _, flag := range []string{"--abort", "--quit"} {
		r := h.run("land", "feature/x", flag, "--no-verify")
		expect(t, r, 2, "usage-error", "")
	}
	h.noProblems()
}

// post-rebase-fix, the conflict verify and the built-in preconditions still
// run under --no-verify.
func TestNoVerifyKeepsTheOtherChecks(t *testing.T) {
	t.Run("post-rebase-fix-still-runs", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape: ShapeDiverged, Layout: srcOnBr,
			Config: `
[[repo]]
root = "{repo}"

[repo.hooks.land]
post-rebase-fix = [{ run = ["{hooks}/fixer"], timeout = "60s" }]
pre-merge-verify = [{ run = ["{hooks}/gate"], timeout = "60s" }]
`,
			Hooks: map[string]string{"fixer": `touch "$PGL_MARKERS/fixer-ran"`, "gate": `exit 1`},
		})
		r := h.run("land", "feature/x", "--no-verify")
		expect(t, r, 0, "landed", "")
		if _, err := os.Stat(filepath.Join(h.marks, "fixer-ran")); err != nil {
			t.Error("post-rebase-fix did not run under --no-verify")
		}
	})
	t.Run("a-failing-post-rebase-fix-still-stops", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape: ShapeDiverged, Layout: srcOnBr,
			Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\npost-rebase-fix = [{ run = [\"{hooks}/fixer\"], timeout = \"60s\" }]\n",
			Hooks:  map[string]string{"fixer": `exit 1`},
		})
		expect(t, h.run("land", "feature/x", "--no-verify"), 21, "stopped", "hook-failed")
	})
	t.Run("the-conflict-verify-still-runs", func(t *testing.T) {
		h := newHarness(t, Scenario{
			Shape:  ShapeConflict,
			Layout: srcOnBr,
			Config: resolverConfig("app.txt") + "verify = [\"{hooks}/verify\"]\n",
			Hooks:  map[string]string{"resolver": `printf 'line1\nresolved\nline3\n' > "$PG_GIT_LANDER_PATH"`, "verify": "exit 1"},
		})
		r := h.run("land", "feature/x", "--no-verify")
		expect(t, r, 21, "stopped", "resolve-failed")
		if r.json["detail"] != "verify-failed" {
			t.Errorf("detail = %v: %s", r.json["detail"], r.stdout)
		}
	})
	t.Run("the-built-in-preconditions-still-run", func(t *testing.T) {
		h := newHarness(t, Scenario{Shape: ShapeAhead, Layout: srcOnBr})
		writeFile(t, filepath.Join(h.repo, "README.md"), "changed\n")
		expect(t, h.run("land", "feature/x", "--no-verify"), 22, "refused", "dirty")
		h.mustGit(h.repo, "checkout", "--", "README.md")
		h.noProblems()
	})
}

// A land started with --no-verify that stops on a conflict keeps the flag
// across a plain --continue.
func TestNoVerifyIsStickyAcrossAConflictStop(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape: ShapeConflict, Layout: srcOnBr, Config: gateConfig,
		Hooks: map[string]string{"gate": `touch "$PGL_MARKERS/gate-ran"; exit 1`},
	})
	r := h.run("land", "feature/x", "--no-verify")
	expect(t, r, 21, "stopped", "conflict")
	resolveByHand(t, h, h.wtPath("src"))
	r = h.run("land", "feature/x", "--continue") // no flag this time
	expect(t, r, 0, "landed", "")
	if !hasWarning(r, "verify-skipped") {
		t.Errorf("the flag was forgotten: %s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(h.marks, "gate-ran")); err == nil {
		t.Error("the gate ran after a plain --continue")
	}
	h.noProblems()
}

// Git's own hooks, planted through the harness's core.hooksPath: the initial
// rebase runs pre-rebase and an autofix commit runs pre-commit; under
// --no-verify neither runs.
func TestNoVerifySkipsGitsOwnHooksForTheToolsCommands(t *testing.T) {
	for _, c := range []struct {
		name  string
		extra []string
		ran   bool
	}{
		{"without-the-flag", nil, true},
		{"with-the-flag", []string{"--no-verify"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Scenario{
				Shape: ShapeDiverged, Layout: srcOnBr,
				Config: "[[repo]]\nroot = \"{repo}\"\n\n[repo.hooks.land]\npost-rebase-fix = [{ run = [\"{hooks}/fixer\"], timeout = \"60s\" }]\n",
				Hooks: map[string]string{
					"fixer":      `grep -q fixed x1.txt || echo fixed >> x1.txt`,
					"pre-rebase": `touch "$PGL_MARKERS/pre-rebase-ran"`,
					"pre-commit": `touch "$PGL_MARKERS/pre-commit-ran"`,
				},
			})
			r := h.run(append([]string{"land", "feature/x"}, c.extra...)...)
			expect(t, r, 0, "landed", "")
			for _, name := range []string{"pre-rebase-ran", "pre-commit-ran"} {
				_, err := os.Stat(filepath.Join(h.marks, name))
				if got := err == nil; got != c.ran {
					t.Errorf("%s = %v, want %v", name, got, c.ran)
				}
			}
		})
	}
}

// A pre-merge-verify entry that modifies a tracked file stops with hook-failed
// and verify-modified-tree and leaves the work worktree clean.
func TestVerifyThatModifiesTheTreeStopsAndLeavesItClean(t *testing.T) {
	h := newHarness(t, Scenario{
		Shape: ShapeAhead, Layout: srcOnBr, Config: gateConfig,
		Hooks: map[string]string{"gate": `echo changed >> README.md`},
	})
	r := h.run("land", "feature/x")
	expect(t, r, 21, "stopped", "hook-failed")
	if r.json["detail"] != "verify-modified-tree" {
		t.Fatalf("detail = %v: %s", r.json["detail"], r.stdout)
	}
	wt, _ := r.json["work_worktree"].(map[string]any)
	path, _ := wt["path"].(string)
	if got := h.mustGit(path, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("the work worktree is not clean:\n%s", got)
	}
}
