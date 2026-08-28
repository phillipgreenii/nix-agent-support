package git

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// tempfixture_test.go pins the `git` rule's half of pg2-yoqsr's temp-root
// carve-out — see docs/adr/0059-ceta-temp-repo-carve-out.md in
// phillipgreenii-nix-agent-support. Two distinct mechanisms are covered:
//
//   - `--git-dir` / `--work-tree` (pre-subcommand FLAG operands): a
//     newly-added floor, gitDirWorkTreeRedirectReason, closing a gap that
//     predates this bead entirely — these flags redirected the effective
//     repository with NO refusal at all (measured against main before this
//     bead: `git --git-dir=<real>/.git config user.email x` was auto-approved
//     outright).
//   - GIT_DIR / GIT_WORK_TREE (env-var form) via hasRedirectEnvVar: this
//     rule's Ask must not survive on top of the gitdir rule's OWN relaxation,
//     or the carve-out would be defeated for checkout/rebase/filter-branch/
//     the modifying set/soft reset — see hasRedirectEnvVar's doc comment.
const realDir = "/etc/pg2-yoqsr-real-canonical"

func chdirInputNoZone(cmd, cwd string) *hookio.HookInput {
	return &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": cmd}), CWD: cwd}
}

// TestTempFixture_GitDirFlag_Real_Rejected closes the pre-existing gap: a
// `--git-dir`/`--work-tree` operand pointing at a real, non-temp location is
// refused, matching the severity the GIT_DIR/GIT_WORK_TREE env-var spelling
// of the identical hazard already has.
func TestTempFixture_GitDirFlag_Real_Rejected(t *testing.T) {
	r := New(nil)
	tests := []string{
		"git --git-dir=" + realDir + "/.git config user.email t@example.com",
		"git --work-tree=" + realDir + " config user.email t@example.com",
		// Presence alone is the match — even a read-only subcommand this rule
		// would otherwise unconditionally Approve gets refused, for the SAME
		// "one access, one verdict regardless of spelling" reason the env-var
		// form (gitdir's rule) already applies to a read-only subcommand.
		"git --git-dir=" + realDir + "/.git rev-parse HEAD",
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(chdirInputNoZone(cmd, "/tmp")))
			if got.Decision != hookio.Reject {
				t.Errorf("Decision = %v (reason %q), want Reject", got.Decision, got.Reason)
			}
		})
	}
}

// TestTempFixture_GitDirFlag_Temp_NotRejected is the carve-out half: the same
// flags, pointing under a temporary root, no longer trigger this rule's new
// floor — the leaf falls through to classify's ordinary verdict.
func TestTempFixture_GitDirFlag_Temp_NotRejected(t *testing.T) {
	r := New(nil)
	tmp := t.TempDir()
	tests := []string{
		"git --git-dir=" + tmp + "/.git config user.email t@example.com",
		"git --work-tree=" + tmp + " config user.email t@example.com",
	}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(chdirInputNoZone(cmd, "/tmp")))
			if got.Decision == hookio.Reject {
				t.Errorf("Decision = %v (reason %q), want NOT Reject (carve-out)", got.Decision, got.Reason)
			}
		})
	}
}

// TestTempFixture_RedirectEnvVar_Temp_NoLongerAsks pins WHY hasRedirectEnvVar
// needed the carve-out at all: without it, a leaf the gitdir rule relaxes
// (every operand under a temp root) would still surface THIS rule's
// "git command with redirected context" Ask on top, on every subcommand that
// consults hasRedirectEnvVar (checkout/rebase/filter-branch/the modifying
// set/soft reset) — defeating the carve-out for exactly the fixture-building
// traffic pg2-yoqsr exists to unblock.
func TestTempFixture_RedirectEnvVar_Temp_NoLongerAsks(t *testing.T) {
	r := New(nil)
	tmp := t.TempDir()
	cmd := "GIT_DIR=" + tmp + "/.git git -C " + tmp + " config user.email t@example.com"
	got := hookio.Verdict(r.Evaluate(chdirInputNoZone(cmd, "/tmp")))
	if got.Decision == hookio.Ask {
		t.Errorf("Decision = Ask (%s), want Approve — hasRedirectEnvVar must not re-surface the redirect Ask "+
			"once every participating operand resolves under a temp root", got.Reason)
	}
	if got.Decision != hookio.Approve {
		t.Errorf("Decision = %v, want Approve", got.Decision)
	}
}

// TestTempFixture_RedirectEnvVar_RealStillAsks confirms hasRedirectEnvVar's
// ORDINARY (non-fixture) behaviour is unchanged: a real GIT_DIR redirect on a
// modifying subcommand still Asks.
func TestTempFixture_RedirectEnvVar_RealStillAsks(t *testing.T) {
	r := New(nil)
	cmd := "GIT_DIR=" + realDir + "_noshape git commit -m x"
	got := hookio.Verdict(r.Evaluate(chdirInputNoZone(cmd, "/tmp")))
	if got.Decision != hookio.Ask {
		t.Errorf("Decision = %v (reason %q), want Ask (unchanged, real redirect)", got.Decision, got.Reason)
	}
}
