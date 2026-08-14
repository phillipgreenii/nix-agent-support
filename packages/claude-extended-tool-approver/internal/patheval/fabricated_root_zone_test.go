// fabricated_root_zone_test.go — pg2-byh62.
//
// DetectProjectRoot's fallback hands back its argument when no `.git` is found, so from
// a non-repo cwd the WHOLE SUBTREE BELOW IT became PathReadWrite. With cwd=$HOME that is
// every dotfile the user owns, and the CONFIGURED deny-list was the only control left —
// which inverts the usual arrangement, where the zone model is the backstop and the
// deny-list the refinement.
//
// THE INVARIANT: a FABRICATED root at or above $HOME grants NO read-write zone. Narrower
// fabricated roots keep theirs, and a REAL repo at $HOME keeps its own.
//
// WHY THE ZONE IS NOT SIMPLY REFUSED FOR EVERY FABRICATED ROOT — the measurement that
// decided it: this machine's pn-workspace root holds no `.git` of its own (it is a
// directory OF sibling repos) and is the single most common cwd in the asklog corpus,
// 36,408 of 139,842 replayable rows, with 47,829 (34%) from non-repo cwds. Refusing all
// of them would strip the zone from where agents work most. The hazard is the root's
// BREADTH, not its fabrication.
//
// These tests build the filesystem they classify against, because rootGrantsZone asks a
// real question of the disk (is there a `.git`?) and a fixture that only names paths
// would answer it by accident.
package patheval

import (
	"os"
	"path/filepath"
	"testing"
)

// THE ASSERTIONS ARE RELATIONS, NOT ABSOLUTE VERDICTS, AND THAT IS FORCED BY THE NIX
// BUILD SANDBOX. Inside it `t.TempDir()` lands under `/nix/var/nix/builds/...`, so the
// `/nix/**` READ-ONLY zone classifies every path these tests build — and it sits BELOW
// the project-root zone in the ladder, which means an absolute `!CanRead()` assertion
// measures the sandbox rather than the guard. (Verified: `nix flake check` failed on
// exactly that, reporting `read-only` where the machine reports `unknown`.) The same
// hazard is recorded in internal/rules/pathsafety/pathsafety_test.go for the same reason.
//
// So the invariant is stated the way it is actually meant: naming a path under a
// FABRICATED broad root is never MORE PERMISSIVE than naming it under a root that does
// not cover it at all. PathAccess is ordered by permissiveness
// (Reject < Unknown < ReadOnly < ReadWrite), so that comparison is well defined, it holds
// in both environments, and it fails on the pre-fix tree in both. `!CanWrite()` backs it
// up as the direct claim about the project-root zone, whose ONLY effect is to return
// PathReadWrite.

// mkRepo makes dir look like a git working tree. A DIRECTORY `.git` and a FILE `.git`
// (the worktree spelling) must both count — see InGitRepo's doc — so both are exercised.
func mkRepo(t *testing.T, dir string, asFile bool) {
	t.Helper()
	if asFile {
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatalf("write .git file: %v", err)
		}
		return
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
}

// narrowRootBaseline evaluates target under a root that does NOT cover it, giving the
// verdict the path deserves on its own merits in THIS environment. It is the right-hand
// side of every relation below.
func narrowRootBaseline(t *testing.T, home, target string) PathAccess {
	t.Helper()
	narrow := filepath.Join(home, "unrelated-narrow-root")
	if err := os.MkdirAll(narrow, 0o755); err != nil {
		t.Fatalf("mkdir narrow root: %v", err)
	}
	return New(narrow).Evaluate(target)
}

// TestFabricatedRootAtOrAboveHomeGrantsNoZone is the security direction: the hole this
// bead exists for.
func TestFabricatedRootAtOrAboveHomeGrantsNoZone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A credential file NOT in any deny-list, which is the case the zone model is the
	// only control for. `.npmrc` holds auth tokens and is not deny-listed on this
	// machine; measured `approve` from cwd=$HOME on main @71a6abba and `abstain` from
	// every repo cwd — the same command, two verdicts, differing only by cwd.
	unconfigured := filepath.Join(home, ".npmrc")

	baseline := narrowRootBaseline(t, home, unconfigured)

	pe := New(home) // $HOME as the project root, no `.git` anywhere: FABRICATED
	if got := pe.Evaluate(unconfigured); got > baseline || got.CanWrite() {
		t.Errorf("FABRICATED $HOME ROOT STILL GRANTS A ZONE: Evaluate(%s) = %s, but under a root that does not cover it the same path is %s — a root DetectProjectRoot invented must not make every dotfile readable",
			unconfigured, got, baseline)
	}

	// An ancestor of $HOME is worse, not better: it covers $HOME and everything beside it.
	parent := filepath.Dir(home)
	if got := New(parent).Evaluate(unconfigured); got > baseline || got.CanWrite() {
		t.Errorf("FABRICATED ROOT ABOVE $HOME STILL GRANTS A ZONE: root=%s Evaluate(%s) = %s, want no more permissive than %s",
			parent, unconfigured, got, baseline)
	}
}

// TestRealRepoAtHomeKeepsItsZone is the deliberate exception, and it is the reason the
// guard keys on InGitRepo rather than on the path alone: a user who version-controls
// their home directory has DECLARED it a project. Both `.git` spellings count.
func TestRealRepoAtHomeKeepsItsZone(t *testing.T) {
	for _, asFile := range []bool{false, true} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		mkRepo(t, home, asFile)

		pe := New(home)
		target := filepath.Join(home, "notes.md")
		if got := pe.Evaluate(target); !got.CanWrite() {
			spelling := "directory"
			if asFile {
				spelling = "file (worktree)"
			}
			t.Errorf("REAL REPO AT $HOME LOST ITS ZONE: .git as a %s, Evaluate(%s) = %s, want read-write — an explicit user declaration is not a fabricated root",
				spelling, target, got)
		}
	}
}

// TestNarrowerFabricatedRootKeepsItsZone is the false-positive direction, and it is the
// half the corpus measurement is about: the pn-workspace root is fabricated and MUST
// keep working, or 34% of logged rows lose their approvals at once.
func TestNarrowerFabricatedRootKeepsItsZone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A workspace-of-repos directory under $HOME with no `.git` of its own — the exact
	// shape of /Users/phillipg/phillipg_mbp.
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "repo-a"), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	mkRepo(t, filepath.Join(workspace, "repo-a"), false)

	pe := New(workspace)
	for _, target := range []string{
		filepath.Join(workspace, "notes.md"),
		filepath.Join(workspace, "repo-a", "main.go"),
	} {
		if got := pe.Evaluate(target); !got.CanWrite() {
			t.Errorf("NARROWER FABRICATED ROOT LOST ITS ZONE: Evaluate(%s) = %s, want read-write — refusing this is the prompt flood pg2-byh62 measured before choosing",
				target, got)
		}
	}

	// And the guard must not leak upward from it: the workspace root grants nothing
	// outside itself.
	outside := filepath.Join(home, ".npmrc")
	if got, baseline := pe.Evaluate(outside), narrowRootBaseline(t, home, outside); got > baseline || got.CanWrite() {
		t.Errorf("ZONE LEAKED ABOVE THE ROOT: Evaluate(%s) = %s, want no more permissive than %s", outside, got, baseline)
	}
}

// TestBothZoneLaddersHonourTheGuard is the one that would have caught the incomplete
// first attempt at this fix.
//
// Evaluate and classifyWithoutEscapeCheck carry INDEPENDENT COPIES of the same zone
// ladder — classifyWithoutEscapeCheck is reached only for a symlink that appears to be
// inside the project and resolves outside it. Guarding only that copy left the hole
// fully open while every unit test still passed, because an ordinary read never reaches
// it. Asserting both is what makes the two copies stay in step.
func TestBothZoneLaddersHonourTheGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unconfigured := filepath.Join(home, ".npmrc")
	if err := os.WriteFile(unconfigured, []byte("//registry:_authToken=x\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", unconfigured, err)
	}

	baseline := narrowRootBaseline(t, home, unconfigured)
	pe := New(home)
	if got := pe.Evaluate(unconfigured); got > baseline || got.CanWrite() {
		t.Errorf("Evaluate's copy of the ladder grants a zone for a fabricated $HOME root: %s, want no more permissive than %s", got, baseline)
	}
	if got := pe.classifyWithoutEscapeCheck(unconfigured); got > baseline || got.CanWrite() {
		t.Errorf("classifyWithoutEscapeCheck's copy of the ladder grants a zone for a fabricated $HOME root: %s, want no more permissive than %s", got, baseline)
	}
}

// TestGuardSurvivesWithCWD covers the PRODUCTION PATH, which neither constructor test
// reaches. internal/rules/safecmds does not build an evaluator — it takes the configured
// one and calls WithCWD once per command, and WithCWD builds a NEW struct field by field.
// A field omitted there is silently zero, and for a bool that means the guard would be
// OFF for every safecmds decision while every New()-based test still passed.
func TestGuardSurvivesWithCWD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unconfigured := filepath.Join(home, ".npmrc")

	pe := New(home) // fabricated $HOME root: the guard is ON
	if pe.projectRootGrantsZone {
		t.Fatal("precondition: the guard is not engaged, so this cannot show WithCWD preserves it")
	}
	derived := pe.WithCWD(home)
	if derived.projectRootGrantsZone {
		t.Error("WithCWD DROPPED THE GUARD: the derived evaluator grants the fabricated root's zone again — safecmds uses this path for every command")
	}
	if got, baseline := derived.Evaluate(unconfigured), narrowRootBaseline(t, home, unconfigured); got > baseline || got.CanWrite() {
		t.Errorf("WithCWD-derived evaluator grants a zone for a fabricated $HOME root: Evaluate(%s) = %s, want no more permissive than %s", unconfigured, got, baseline)
	}

	// And the opposite direction: a root that DOES grant its zone must keep granting it
	// through WithCWD, or every safecmds path check silently tightens.
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	peWS := New(workspace).WithCWD(workspace)
	target := filepath.Join(workspace, "notes.md")
	if got := peWS.Evaluate(target); !got.CanWrite() {
		t.Errorf("WithCWD LOST A LEGITIMATE ZONE: Evaluate(%s) = %s, want read-write", target, got)
	}
}

// TestRootGrantsZoneTable pins the predicate itself, so a later reader can see the rule
// without reconstructing it from the zone ladder.
func TestRootGrantsZoneTable(t *testing.T) {
	home := t.TempDir()
	nested := filepath.Join(home, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	mkRepo(t, repo, false)

	for _, tc := range []struct {
		name string
		root string
		want bool
		why  string
	}{
		{"fabricated root IS home", home, false, "covers every dotfile the user owns"},
		{"fabricated root ABOVE home", filepath.Dir(home), false, "covers home and everything beside it"},
		{"fabricated root BELOW home", nested, true, "narrow: the pn-workspace-root shape"},
		{"real repo below home", repo, true, "a declared project"},
		{"filesystem root", "/", false, "the broadest possible fabricated root"},
		{"empty root", "", false, "nothing to grant"},
	} {
		if got := rootGrantsZone(tc.root, home); got != tc.want {
			t.Errorf("rootGrantsZone(%q, home) = %v, want %v — %s", tc.root, got, tc.want, tc.why)
		}
	}

	// With no home known, the guard cannot decide breadth and MUST NOT deny — denying
	// would strip every zone in an environment where os.UserHomeDir() failed.
	if !rootGrantsZone(nested, "") {
		t.Error("rootGrantsZone(root, \"\") = false, want true — an unknown home must not silently remove every zone")
	}
}
