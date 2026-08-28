package gitdir

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// tempfixture_test.go pins pg2-yoqsr's R1-R6 temp-root carve-out and its
// side-finding fix, at this rule's own scope — see
// docs/adr/0059-ceta-temp-repo-carve-out.md in phillipgreenii-nix-agent-support
// for the full decision. cmd/claude-extended-tool-approver's own integration
// suite pins the chain-level (Verification section) outcomes, including the
// R2 regression this bead's acceptance criteria singles out as one that
// "MUST NOT be dropped".

// realDir is a path GUARANTEED never to resolve under any of
// internal/temproot's roots on this machine — /etc always exists (mirroring
// internal/rules/git's own git_chdir_test.go, which already uses /etc as its
// "outside every zone" reference point), so a not-yet-existing child under it
// still resolves to something real via the nearest-ancestor fallback.
const realDir = "/etc/pg2-yoqsr-real-canonical"

// bashInputCWD is bashInput with an explicit CWD, needed for every case below
// because ResolveOperand fails safe (never "under a temp root") on an empty
// one — see internal/temproot.ResolveOperand's own doc for why.
func bashInputCWD(cmd, cwd string) *hookio.HookInput {
	return &hookio.HookInput{ToolName: "Bash", ToolInput: bashJSON(cmd), CWD: cwd}
}

// TestTempFixture_GitMetadataWrite_UnderTempRoot_Relaxed is R1/R3/R6: a
// literal `.git/`-metadata write whose effective directory resolves under a
// temporary root is no longer refused by this rule — it falls through
// exactly as a leaf naming no `.git` path at all, letting the generic
// approvers decide. R6: the target need not already exist (`.git/hooks`
// under a FRESH t.TempDir() is never created by this test).
func TestTempFixture_GitMetadataWrite_UnderTempRoot_Relaxed(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	for _, cmd := range []string{
		"echo x > .git/config",
		"rm -rf .git/hooks",
		"sed -i '' 's/a/b/' .git/config",
	} {
		got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, tmp)))
		if got.Decision == hookio.Reject {
			t.Errorf("cmd %q under temp root %s: got Reject (%s), want the refusal RELAXED (R1/R3/R6)", cmd, tmp, got.Reason)
		}
	}
}

// TestTempFixture_GitMetadataWrite_RealCheckout_StillRefused is the "unchanged"
// half of the Verification section: a `.git/` write in a real checkout keeps
// its Reject exactly as before this bead.
func TestTempFixture_GitMetadataWrite_RealCheckout_StillRefused(t *testing.T) {
	r := New()
	got := hookio.Verdict(r.Evaluate(bashInputCWD("echo x > .git/config", realDir)))
	if got.Decision != hookio.Reject {
		t.Errorf("Decision = %v, want Reject (real checkout, unaffected by the carve-out)", got.Decision)
	}
}

// TestTempFixture_SideFinding_BareRepoEnvVar pins the side-finding fix: EVERY
// one of the five canonical repo-locating env vars is caught by NAME,
// regardless of whether its VALUE contains a literal `.git` path segment — a
// bare repository's GIT_DIR/GIT_COMMON_DIR/etc. has none. Each row's REAL
// value is refused; the corresponding TEMP value (same var, same shape) is
// pinned by the sibling test below.
func TestTempFixture_SideFinding_BareRepoEnvVar(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		cmd  string
	}{
		{"GIT_DIR, bare repo (no .git segment)", "GIT_DIR=" + realDir + "/bare-repo git status"},
		{"GIT_WORK_TREE, bare-looking value", "GIT_WORK_TREE=" + realDir + "/worktree git status"},
		{"GIT_INDEX_FILE, no .git segment", "GIT_INDEX_FILE=" + realDir + "/index-alt git status"},
		{"GIT_COMMON_DIR, no .git segment", "GIT_COMMON_DIR=" + realDir + "/common git status"},
		{"GIT_OBJECT_DIRECTORY, no .git segment", "GIT_OBJECT_DIRECTORY=" + realDir + "/objects-alt git status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, realDir)))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: Decision = %v, want Reject — a bare-repo-shaped value must be caught by NAME, "+
					"not by a `.git` path pattern (the side-finding fix)", tt.cmd, got.Decision)
			}
		})
	}
}

// TestTempFixture_SideFinding_BareRepoEnvVar_UnderTempRoot_Relaxed is the
// carve-out half of the same side-finding fix: the identical bare-repo shape,
// this time resolving under a temporary root, is relaxed rather than
// refused — proving the fix keys on the EFFECTIVE directory (R1) and not on
// any path pattern, for a value that would defeat a pattern-based test
// either way.
func TestTempFixture_SideFinding_BareRepoEnvVar_UnderTempRoot_Relaxed(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	tests := []string{
		"GIT_DIR=" + tmp + "/bare-repo git status",
		"GIT_WORK_TREE=" + tmp + "/worktree git status",
		"GIT_INDEX_FILE=" + tmp + "/index-alt git status",
		"GIT_COMMON_DIR=" + tmp + "/common git status",
		"GIT_OBJECT_DIRECTORY=" + tmp + "/objects-alt git status",
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, tmp)))
			if got.Decision == hookio.Reject {
				t.Errorf("cmd %q under temp root: got Reject (%s), want relaxed", cmd, got.Reason)
			}
		})
	}
}

// TestTempFixture_R2Regression_MixedRealAndTemp_StillRefused is the ONE
// regression pg2-yoqsr's Verification section names explicitly as something
// that "MUST NOT be dropped": GIT_DIR pointing at a REAL canonical repository,
// combined with a `-C` that happens to be a tempdir, must stay refused. A
// temp path merely APPEARING in the command is not the carve-out's test —
// GIT_DIR still outranks `-C`, and this is the exact shape that would defeat
// a naive "some argument is a temp path" carve-out.
func TestTempFixture_R2Regression_MixedRealAndTemp_StillRefused(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	cmd := "GIT_DIR=" + realDir + "/.git git -C " + tmp + " config user.email t@example.com"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, "/tmp")))
	if got.Decision != hookio.Reject {
		t.Errorf("mixed real+temp: Decision = %v, want Reject (R2 regression — GIT_DIR outranks -C)", got.Decision)
	}
}

// TestTempFixture_AllTempRoot_Relaxed is R1/R2's positive case: EVERY
// participating operand (here, GIT_DIR and the `-C` target) resolves under
// the SAME temporary root, so the refusal is relaxed.
func TestTempFixture_AllTempRoot_Relaxed(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	cmd := "GIT_DIR=" + tmp + "/.git git -C " + tmp + " config user.email t@example.com"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, "/tmp")))
	if got.Decision == hookio.Reject {
		t.Errorf("all-temp: got Reject (%s), want relaxed", got.Reason)
	}
}
