//go:build integration

package integration

import (
	"strings"
	"testing"
)

// TestSmokeStatus proves the harness end to end: the real binary reads a
// clean ahead fixture, the outcome subset and snapshot match, and the
// post-condition holds.
func TestSmokeStatus(t *testing.T) {
	runScenario(t, Scenario{
		Name:  "status-on-clean-repo",
		Shape: ShapeAhead,
		Steps: []Step{{Run: []string{"status"}}},
		Expected: Expected{
			Outcome: map[string]any{"outcome": "status", "locks": []any{}, "stopped": []any{}},
			Snapshot: Snapshot{
				Refs:       map[string]string{"refs/heads/feature/x": "@feature/x", "refs/heads/main": "@main"},
				Worktrees:  map[string]WorktreeState{"main": {Head: "branch:main", CheckStatus: true}},
				TargetTree: "@tree:main",
			},
		},
	})
}

// TestSmokeFifoRendezvous proves the deterministic rendezvous: a git hook
// blocks on a FIFO until the test releases it, with no sleep.
func TestSmokeFifoRendezvous(t *testing.T) {
	sc := Scenario{
		Name:  "hook-blocks-until-released",
		Shape: ShapeAhead,
		Fifos: []string{"up", "go"},
		Hooks: map[string]string{
			"pre-commit": `echo up > "$PGL_MARKERS/up.fifo"; cat "$PGL_MARKERS/go.fifo" >/dev/null`,
		},
		Steps: []Step{
			{StartGit: []string{"commit", "-q", "--allow-empty", "-m", "blocked"}, Name: "commit"},
			{Await: "up"},
			{Release: "go"},
			{Wait: true, Name: "commit"},
		},
		// main moved, so it no longer equals its fixture tip.
		Expected: Expected{Snapshot: Snapshot{Refs: map[string]string{"refs/heads/main": "@main"}}},
	}
	problems := execScenario(t, sc)
	if !containsAny(problems, "ref refs/heads/main") {
		t.Fatalf("the released commit should have advanced main: %v", problems)
	}
}

// TestPostConditionFlagsLeftoverLockRef is the guard test of the
// post-condition: a scenario that leaves a lock ref behind MUST fail. The
// negative control (check removed) was shown once by hand: with the lock-ref
// check deleted from postCondition this test fails.
func TestPostConditionFlagsLeftoverLockRef(t *testing.T) {
	problems := execScenario(t, Scenario{
		Name:  "leftover-lock-ref",
		Shape: ShapeAhead,
		Steps: []Step{{Git: []string{"update-ref", "refs/pg-git-lander/locks/heads/feature/x", "HEAD"}}},
	})
	if !containsAny(problems, "lock refs or records left behind") {
		t.Fatalf("a leftover lock ref did not fail the post-condition; problems: %v", problems)
	}
}

func TestPostConditionAllowsStoppedOperation(t *testing.T) {
	problems := execScenario(t, Scenario{
		Name:     "stopped-keeps-records",
		Shape:    ShapeAhead,
		Steps:    []Step{{Git: []string{"update-ref", "refs/pg-git-lander/locks/heads/feature/x", "HEAD"}}},
		Expected: Expected{Stopped: true},
	})
	if len(problems) != 0 {
		t.Fatalf("a stopped scenario may keep its records, got %v", problems)
	}
}

func TestPostConditionFlagsDirtyWorktreeAndBranch(t *testing.T) {
	problems := execScenario(t, Scenario{
		Name:   "dirty",
		Shape:  ShapeAhead,
		Layout: []Worktree{{Name: "src", Branch: "feature/x", Dirty: "stray.txt"}},
		Steps:  []Step{{Git: []string{"branch", "pg-git-lander/op1"}}},
	})
	if !containsAny(problems, "is not clean") || !containsAny(problems, "pg-git-lander/* branches left behind") {
		t.Fatalf("dirty worktree and work branch must fail the post-condition: %v", problems)
	}
}

func TestOutcomeSubsetMismatchFails(t *testing.T) {
	problems := execScenario(t, Scenario{
		Name:     "wrong-outcome",
		Shape:    ShapeAhead,
		Steps:    []Step{{Run: []string{"status"}}},
		Expected: Expected{Outcome: map[string]any{"outcome": "stopped"}},
	})
	if !containsAny(problems, "outcome.outcome") {
		t.Fatalf("a wrong outcome subset must fail: %v", problems)
	}
}

func TestEveryShapeBuilds(t *testing.T) {
	for _, s := range AllShapes {
		t.Run(string(s), func(t *testing.T) {
			if p := execScenario(t, Scenario{Name: string(s), Shape: s}); len(p) != 0 {
				t.Fatal(p)
			}
			f := shapes[s].facts
			if f.Tips["main"] == "" || f.Tips["feature/x"] == "" {
				t.Fatalf("facts missing tips: %+v", f)
			}
		})
	}
}

func TestOriginAdvanceRemote(t *testing.T) {
	problems := execScenario(t, Scenario{
		Name:  "origin-advance",
		Shape: ShapeOrigin,
		Steps: []Step{{AdvanceRemote: "r.txt=remote"}},
		Expected: Expected{Snapshot: Snapshot{
			Refs: map[string]string{"refs/remotes/origin/main": ""},
		}},
	})
	// origin/main moved, so it no longer equals the "" (absent) expectation.
	if !containsAny(problems, "refs/remotes/origin/main") {
		t.Fatalf("expected origin/main to exist after the fetch: %v", problems)
	}
}

func containsAny(problems []string, sub string) bool {
	for _, p := range problems {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}
