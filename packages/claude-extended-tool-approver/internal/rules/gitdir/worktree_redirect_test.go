package gitdir

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// worktree_redirect_test.go pins tc-mzr5's extension of the pg2-yoqsr temp-root
// carve-out to four PERSISTENT spellings of the same GIT_DIR/GIT_WORK_TREE/
// --git-dir/--work-tree redirect hazard — see gitdir.go's package doc,
// "PERSISTENT-SPELLING WORKTREE/GIT-DIR REDIRECTS", and gitWorktreeRedirectTarget's
// own doc for the four shapes and why each is scoped the way it is.
//
// OPERATOR RULING (tc-mzr5, via /unblock-human-beads, 2026-09-08), verbatim:
// "i think there is scheduled work to lessen the restrictions on GIT_DIR and
// GIT_INDEX_FILE, but we need to have a better solution for the worktree config
// and it being set wrong." RULED YES: extend the rule to Reject 'git config
// core.worktree ...', '--file'-writes to core.worktree, 'git init
// --separate-git-dir', and 'git worktree' targets resolving outside a temp root.
// The pre-existing GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE/GIT_COMMON_DIR/
// GIT_OBJECT_DIRECTORY refusal is explicitly OUT OF SCOPE for this bead (tracked
// separately as tc-j0aa) and is untouched here.

// TestWorktreeRedirect_ConfigCoreWorktree_RealCheckout_Refused is shape 1: `git
// config core.worktree <path>` (and its --global/--system/--local/--worktree scope
// spellings) in a real (non-temp) checkout stays refused, matching the
// pg2-yoqsr-era GIT_DIR/--work-tree treatment of the identical hazard.
func TestWorktreeRedirect_ConfigCoreWorktree_RealCheckout_Refused(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		cmd  string
	}{
		{"bare form", "git config core.worktree " + realDir + "/elsewhere"},
		{"--global scope", "git config --global core.worktree " + realDir + "/elsewhere"},
		{"--system scope", "git config --system core.worktree " + realDir + "/elsewhere"},
		{"--local scope", "git config --local core.worktree " + realDir + "/elsewhere"},
		{"--worktree scope", "git config --worktree core.worktree " + realDir + "/elsewhere"},
		{"git 2.54 set subcommand", "git config set core.worktree " + realDir + "/elsewhere"},
		{"key spelled uppercase", "git config CORE.WORKTREE " + realDir + "/elsewhere"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, realDir)))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: Decision = %v, want Reject (real checkout core.worktree redirect)", tt.cmd, got.Decision)
			}
		})
	}
}

// TestWorktreeRedirect_ConfigCoreWorktree_UnderTempRoot_Relaxed is shape 1's
// carve-out half: the write is relaxed only once BOTH the invocation's own
// effective directory AND the value being written resolve under the same
// temporary root.
func TestWorktreeRedirect_ConfigCoreWorktree_UnderTempRoot_Relaxed(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	cmd := "git config core.worktree " + tmp + "/elsewhere"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, tmp)))
	if got.Decision == hookio.Reject {
		t.Errorf("cmd %q under temp root: got Reject (%s), want relaxed", cmd, got.Reason)
	}
}

// TestWorktreeRedirect_ConfigCoreWorktree_MixedRealAndTemp_StillRefused mirrors
// tempfixture_test.go's R2 regression at this new shape: a temp-rooted VALUE
// alone does not relax the refusal when the invocation's own effective directory
// (here, the process cwd — no -C is given) is a real checkout. Mixed real+temp is
// the attack the carve-out exists to keep refusing, not a use case to accommodate.
func TestWorktreeRedirect_ConfigCoreWorktree_MixedRealAndTemp_StillRefused(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	cmd := "git config core.worktree " + tmp + "/elsewhere"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, realDir)))
	if got.Decision != hookio.Reject {
		t.Errorf("cmd %q: Decision = %v, want Reject (real cwd + temp value is still mixed)", cmd, got.Decision)
	}
}

// TestWorktreeRedirect_ConfigFileCoreWorktree_RealFile_Refused is shape 2: `git
// config --file <path> ... core.worktree <value>` writes into an ARBITRARY file —
// the --file target itself, not the value, is what must resolve under a temp
// root, since the value written could be anything.
func TestWorktreeRedirect_ConfigFileCoreWorktree_RealFile_Refused(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	tests := []struct {
		name string
		cmd  string
	}{
		{"--file separated", "git config --file " + realDir + "/config core.worktree " + tmp + "/wherever"},
		{"--file= glued", "git config --file=" + realDir + "/config core.worktree " + tmp + "/wherever"},
		{"-f short flag", "git config -f " + realDir + "/config core.worktree " + tmp + "/wherever"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// cwd is the temp root so the ONLY non-temp participant is the --file
			// target itself — isolating exactly the operand this shape must check.
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, tmp)))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: Decision = %v, want Reject (--file target is a real, non-temp path)", tt.cmd, got.Decision)
			}
		})
	}
}

// TestWorktreeRedirect_ConfigFileCoreWorktree_UnderTempRoot_Relaxed is shape 2's
// carve-out half: the --file target (and the invocation's own cwd) both resolving
// under the same temp root relaxes the refusal, regardless of what the VALUE is.
func TestWorktreeRedirect_ConfigFileCoreWorktree_UnderTempRoot_Relaxed(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	cmd := "git config --file " + tmp + "/config core.worktree /anything/at/all"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, tmp)))
	if got.Decision == hookio.Reject {
		t.Errorf("cmd %q under temp root: got Reject (%s), want relaxed", cmd, got.Reason)
	}
}

// TestWorktreeRedirect_InitSeparateGitDir_RealCheckout_Refused is shape 3: `git
// init --separate-git-dir=<path>`, both the glued (`=`) and space-separated
// spellings git itself accepts.
func TestWorktreeRedirect_InitSeparateGitDir_RealCheckout_Refused(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		cmd  string
	}{
		{"glued =", "git init --separate-git-dir=" + realDir + "/gitdir"},
		{"space-separated", "git init --separate-git-dir " + realDir + "/gitdir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, realDir)))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: Decision = %v, want Reject (real --separate-git-dir target)", tt.cmd, got.Decision)
			}
		})
	}
}

// TestWorktreeRedirect_InitSeparateGitDir_UnderTempRoot_Relaxed is shape 3's
// carve-out half.
func TestWorktreeRedirect_InitSeparateGitDir_UnderTempRoot_Relaxed(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	tests := []string{
		"git init --separate-git-dir=" + tmp + "/gitdir",
		"git init --separate-git-dir " + tmp + "/gitdir",
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

// TestWorktreeRedirect_WorktreeAddMove_RealCheckout_Refused is shape 4: `git
// worktree add`/`git worktree move` whose target resolves outside a temp root.
func TestWorktreeRedirect_WorktreeAddMove_RealCheckout_Refused(t *testing.T) {
	r := New()
	tests := []struct {
		name string
		cmd  string
	}{
		{"add with branch operand", "git worktree add " + realDir + "/feature feature-branch"},
		{"add with -b new-branch flag", "git worktree add -b newbranch " + realDir + "/feature main"},
		{"add, detached", "git worktree add --detach " + realDir + "/feature"},
		{"move destination", "git worktree move ../old " + realDir + "/new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hookio.Verdict(r.Evaluate(bashInputCWD(tt.cmd, realDir)))
			if got.Decision != hookio.Reject {
				t.Errorf("cmd %q: Decision = %v, want Reject (real worktree add/move target)", tt.cmd, got.Decision)
			}
		})
	}
}

// TestWorktreeRedirect_WorktreeAddMove_UnderTempRoot_Relaxed is shape 4's
// carve-out half.
func TestWorktreeRedirect_WorktreeAddMove_UnderTempRoot_Relaxed(t *testing.T) {
	r := New()
	tmp := t.TempDir()
	tests := []string{
		"git worktree add " + tmp + "/feature feature-branch",
		"git worktree move " + tmp + "/old " + tmp + "/new",
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

// TestWorktreeRedirect_ChdirFlag_ResolvesAgainstDashC exercises the `-C <dir>`
// interaction tempFixtureCarveOutApplies' leafCwd already models for GIT_DIR/
// --work-tree: a `-C` pointing at a temp root relaxes a core.worktree write whose
// value ALSO resolves under that same root, even though the process cwd itself is
// a different, real location — and the mirrored mixed case (`-C` pointing at a
// REAL directory) stays refused even though the process cwd is a temp root.
func TestWorktreeRedirect_ChdirFlag_ResolvesAgainstDashC(t *testing.T) {
	r := New()
	tmp := t.TempDir()

	relaxed := "git -C " + tmp + " config core.worktree " + tmp + "/elsewhere"
	if got := hookio.Verdict(r.Evaluate(bashInputCWD(relaxed, realDir))).Decision; got == hookio.Reject {
		t.Errorf("cmd %q: got Reject, want relaxed (-C and value both resolve under the same temp root)", relaxed)
	}

	mixed := "git -C " + realDir + " config core.worktree " + tmp + "/elsewhere"
	if got := hookio.Verdict(r.Evaluate(bashInputCWD(mixed, tmp))).Decision; got != hookio.Reject {
		t.Errorf("cmd %q: got %v, want Reject (-C names a real directory; a temp process cwd must not relax it)", mixed, got)
	}
}

// TestWorktreeRedirect_NotMatched pins the shapes this extension deliberately
// leaves alone: a bare read of core.worktree, an unset, an unrelated config key,
// and the worktree subcommands gitWorktreeSubcommandTarget's own doc names as out
// of scope (list/prune/repair/remove). None of these should be RECOGNISED by this
// rule at all (matched=false) — the rest of the chain (or, for list/prune/repair/
// remove, internal/rules/git's own existing TestGit_Worktree_Approve) decides.
func TestWorktreeRedirect_NotMatched(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{"bare key is a read", "git config core.worktree"},
		{"--get is a read", "git config --get core.worktree"},
		{"--file --get is a read through an explicit file", "git config --file /repo/.git/config --get core.worktree"},
		{"--unset has no value to check", "git config --unset core.worktree"},
		{"--unset-all has no value to check", "git config --unset-all core.worktree"},
		{"unrelated config key", "git config clean.requireForce false"},
		{"worktree list", "git worktree list"},
		{"worktree prune", "git worktree prune"},
		{"worktree repair", "git worktree repair"},
		{"worktree remove", "git worktree remove ../feature"},
		{"plain git status", "git status"},
		{"plain git init, no separate-git-dir", "git init /repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if matchedBash(tt.cmd) {
				t.Errorf("cmd %q: matched = true, want this rule to Abstain entirely (unrecognised/out-of-scope shape)", tt.cmd)
			}
		})
	}
}

// TestWorktreeRedirect_EnvVarRuleUntouched is a narrow regression guard: this
// bead's own ruling explicitly keeps the pre-existing GIT_DIR/GIT_WORK_TREE/
// GIT_INDEX_FILE/GIT_COMMON_DIR/GIT_OBJECT_DIRECTORY refusal (tc-j0aa's scope,
// not this bead's) untouched. A plain env-var redirect must still Reject with
// its OWN (pre-existing) reason, not the new worktreeRedirect one.
func TestWorktreeRedirect_EnvVarRuleUntouched(t *testing.T) {
	r := New()
	cmd := "GIT_DIR=" + realDir + "/.git git status"
	got := hookio.Verdict(r.Evaluate(bashInputCWD(cmd, realDir)))
	if got.Decision != hookio.Reject {
		t.Fatalf("Decision = %v, want Reject (pre-existing GIT_DIR refusal, unchanged)", got.Decision)
	}
	if got.Reason == "" {
		t.Fatal("Reason is empty")
	}
}
