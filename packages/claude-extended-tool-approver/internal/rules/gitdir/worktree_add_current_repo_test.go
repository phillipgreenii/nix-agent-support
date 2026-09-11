package gitdir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// worktree_add_current_repo_test.go pins tc-uelj's current-repo-root
// carve-out for `git worktree add` — see gitdir.go's package doc,
// "CURRENT-REPO-ROOT CARVE-OUT FOR `git worktree add`", and
// currentRepoWorktreeAddCarveOutApplies' own doc for the full decision.
//
// OPERATOR POSITION (tc-uelj, 2026-09-10), verbatim: "you should always be
// allowed to do that" — creating an ADDITIONAL worktree of the repo an agent
// is already in (this repo's own `.worktrees/<id>` convention, R-4-mandated)
// is not the tc-mzr5/tc-7mqr persistent-redirect hazard.
//
// # WHY A REAL, ON-DISK `.git` FIXTURE UNDER /var/tmp, NOT t.TempDir()
//
// The carve-out is keyed on patheval.GitRoot, a genuine filesystem walk for
// a `.git` entry — unlike tempFixtureCarveOutApplies (pg2-yoqsr), which is
// pure path-string comparison against a KNOWN, fixed root set and needs no
// real `.git` on disk at all. A fixture built with t.TempDir() lands under
// $TMPDIR/os.TempDir(), which on this machine resolves under `/tmp` — itself
// one of internal/temproot.Roots() — so a positive test built that way would
// pass regardless of whether THIS carve-out's own logic is correct at all:
// tempFixtureCarveOutApplies would relax it anyway. `/var/tmp` is a distinct,
// real, world-writable directory that is NOT in temproot.Roots() (checked:
// only $TMPDIR, /private/var/folders, /private/tmp and /tmp are), so a
// fixture built there isolates this carve-out from that one — proving IT
// specifically is what approves the command. Fixtures are created directly
// (not via t.TempDir()) and removed in t.Cleanup, matching this file's own
// isolation requirement without borrowing $TMPDIR's ambient temp-root grant.
func newRealRepoFixture(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "ceta-tc-uelj-repo-")
	if err != nil {
		t.Fatalf("MkdirTemp under /var/tmp: %v (is it writable on this machine?)", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("creating fixture .git dir: %v", err)
	}
	return dir
}

// TestWorktreeAdd_CurrentRepoRoot_Approved is acceptance criterion (a): `git
// worktree add .worktrees/<id> -b <branch>` from within the repo — this
// repo's own documented convention — is now approved (relaxed) when run from
// that repo's own root.
func TestWorktreeAdd_CurrentRepoRoot_Approved(t *testing.T) {
	repo := newRealRepoFixture(t)
	r := New()
	tests := []struct {
		name string
		cmd  string
	}{
		{"relative .worktrees path", "git worktree add .worktrees/tc-test -b drain/tc-test"},
		{"absolute path under the repo", "git worktree add " + repo + "/.worktrees/tc-test -b drain/tc-test"},
		{"detached, no branch", "git worktree add --detach .worktrees/tc-test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, repo)))
			if got.Decision == hookio.Reject {
				t.Errorf("cmd %q from repo root %s: got Reject (%s), want relaxed", tt.cmd, repo, got.Reason)
			}
		})
	}
}

// TestWorktreeAdd_UnrelatedAbsolutePath_StillRejected is acceptance criterion
// (c): `git worktree add /some/unrelated/absolute/path -b x` — a target NOT
// under the current repo's own root — is still rejected. cwd is a real
// repo fixture (so patheval.GitRoot DOES find a root), but the target names a
// path elsewhere entirely, so only the current-repo-root case is newly
// approved, not "any worktree add whatsoever".
func TestWorktreeAdd_UnrelatedAbsolutePath_StillRejected(t *testing.T) {
	repo := newRealRepoFixture(t)
	r := New()
	tests := []struct {
		name string
		cmd  string
	}{
		{"unrelated absolute path, no ancestor relation", "git worktree add " + realDir + "/feature -b x"},
		{"escaping via ../ above the repo root", "git worktree add ../outside-the-repo -b x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, repo)))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q from repo root %s: Decision = %v, want Reject (target not under the current repo's root)", tt.cmd, repo, got.Decision)
			}
		})
	}
}

// TestWorktreeAdd_NoRepoFound_StillRejected is the fail-safe half: when
// patheval.GitRoot finds no enclosing `.git` at all (a fabricated or
// not-a-repo cwd), the carve-out never relaxes anything — matching
// tempFixtureCarveOutApplies' own "no participants, never relax" default.
func TestWorktreeAdd_NoRepoFound_StillRejected(t *testing.T) {
	r := New()
	// realDir does not exist on disk at all, so patheval.GitRoot's walk finds
	// no `.git` anywhere above it either.
	cmd := "git worktree add " + realDir + "/.worktrees/tc-test -b drain/tc-test"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, realDir)))
	if got.Decision != hookio.Reject {
		t.Errorf("cmd %q: Decision = %v, want Reject (no enclosing repo found, fail-safe)", cmd, got.Decision)
	}
}

// TestWorktreeAdd_OtherRedirectShapes_NotEligible_EvenUnderRepoRoot pins that
// ONLY `git worktree add` is eligible for this carve-out: `git worktree
// move`, `git config core.worktree`, and `git init --separate-git-dir` all
// redirect something that ALREADY EXISTS (the tc-mzr5/tc-7mqr hazard), so
// they stay refused even when their own target resolves under the SAME
// current-repo root that would relax an `add`.
func TestWorktreeAdd_OtherRedirectShapes_NotEligible_EvenUnderRepoRoot(t *testing.T) {
	repo := newRealRepoFixture(t)
	r := New()
	tests := []struct {
		name string
		cmd  string
	}{
		{"worktree move to a path under the repo root", "git worktree move ../old " + repo + "/.worktrees/new"},
		{"config core.worktree to a value under the repo root", "git config core.worktree " + repo + "/.worktrees/elsewhere"},
		{"init --separate-git-dir under the repo root", "git init --separate-git-dir=" + repo + "/.worktrees/gitdir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, repo)))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: Decision = %v, want Reject (only `worktree add` is eligible for the current-repo-root carve-out)", tt.cmd, got.Decision)
			}
		})
	}
}

// TestWorktreeAdd_EnvVarRedirect_AlongsideEligibleAdd_StillRejected is the
// mixed-hazard regression this carve-out's own doc names explicitly: a
// GIT_DIR/GIT_WORK_TREE redirect on ONE leaf of a compound must not be
// masked by an eligible `worktree add` on ANOTHER leaf of the SAME compound.
func TestWorktreeAdd_EnvVarRedirect_AlongsideEligibleAdd_StillRejected(t *testing.T) {
	repo := newRealRepoFixture(t)
	r := New()
	cmd := "GIT_DIR=" + realDir + "/.git git status && git worktree add .worktrees/tc-test -b drain/tc-test"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, repo)))
	if got.Decision != hookio.Reject {
		t.Errorf("cmd %q: Decision = %v, want Reject (a GIT_DIR redirect elsewhere in the compound must not be masked by an eligible worktree add)", cmd, got.Decision)
	}
}

// TestWorktreeAdd_OtherGitMetadataWrite_AlongsideEligibleAdd_StillRejected is
// the non-git-leaf counterpart: a plain `.git/`-metadata write riding
// alongside an eligible `worktree add` in the same compound must not be
// relaxed either.
func TestWorktreeAdd_OtherGitMetadataWrite_AlongsideEligibleAdd_StillRejected(t *testing.T) {
	repo := newRealRepoFixture(t)
	r := New()
	cmd := "git worktree add .worktrees/tc-test -b drain/tc-test && echo x > .git/config"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, repo)))
	if got.Decision != hookio.Reject {
		t.Errorf("cmd %q: Decision = %v, want Reject (an unrelated .git/config write elsewhere in the compound must not be masked)", cmd, got.Decision)
	}
}

// TestCurrentRepoWorktreeAddCarveOutApplies_Unit exercises the predicate
// directly for the shapes above the Evaluate-level tests don't isolate on
// their own: a `-b`/branch-name operand must never be mistaken for the
// worktree path, and `git -C <repo>` must resolve leafCwd/target the same
// way the pre-existing GIT_DIR/--work-tree carve-out participant resolution
// already does.
func TestCurrentRepoWorktreeAddCarveOutApplies_Unit(t *testing.T) {
	repo := newRealRepoFixture(t)

	carveOutApplies := []string{
		"git worktree add .worktrees/x -b newbranch",
		"git worktree add -b newbranch .worktrees/x main",
		"git -C " + repo + " worktree add .worktrees/x -b newbranch",
	}
	for _, cmd := range carveOutApplies {
		t.Run("applies: "+cmd, func(t *testing.T) {
			if !currentRepoWorktreeAddCarveOutApplies(cmdparse.Parse(cmd), repo) {
				t.Errorf("cmd %q: carve-out did not apply, want it to", cmd)
			}
		})
	}

	carveOutDoesNotApply := []string{
		"git worktree add /etc/pg2-yoqsr-real-canonical/x -b newbranch",
		"git worktree move ../old " + repo + "/new",
		"git worktree remove ../feature", // not matched at all: gitWorktreeRedirectTarget returns
		// false, so this carve-out has no opinion — internal/rules/git's own
		// TestGit_Worktree_Approve is what approves it, unaffected by this bead.
		"git status",
	}
	for _, cmd := range carveOutDoesNotApply {
		t.Run("does not apply: "+cmd, func(t *testing.T) {
			if currentRepoWorktreeAddCarveOutApplies(cmdparse.Parse(cmd), repo) {
				t.Errorf("cmd %q: carve-out applied, want it not to", cmd)
			}
		})
	}
}
