package ghstack

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// evalGhStack is the shared one-command driver, mirroring gh package's evalGH.
func evalGhStack(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New().Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(cmd),
	}))
}

// TestGhStack_ReadOnlyNavigation_Approve pins the always-safe read-only/navigation band:
// view (with or without --json/--short — SKILL.md's own "always use --json" advice is an
// operability concern, not a safety one, so this rule does not gate on it), and
// up/down/top/bottom/trunk, none of which have any documented flag at all.
func TestGhStack_ReadOnlyNavigation_Approve(t *testing.T) {
	cmds := []string{
		"gh stack view --json",
		"gh stack view",
		"gh stack view --short",
		"gh stack up",
		"gh stack up 3",
		"gh stack down",
		"gh stack down 2",
		"gh stack top",
		"gh stack bottom",
		"gh stack trunk",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Init_Approve pins `gh stack init`, with and without --base, and with
// multiple branch names (adoption of existing branches is handled automatically per
// SKILL.md and carries no extra risk this rule needs to gate).
func TestGhStack_Init_Approve(t *testing.T) {
	cmds := []string{
		"gh stack init auth",
		"gh stack init branch-a branch-b branch-c",
		"gh stack init --base develop branch-a branch-b",
		"gh stack init -b develop branch-a",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Checkout_BranchName_Approve pins the LOCAL-ONLY branch-name form of
// `gh stack checkout` to Approve: SKILL.md states this "resolves against locally tracked
// stacks only" and "is always safe for non-interactive use" — no network involved.
func TestGhStack_Checkout_BranchName_Approve(t *testing.T) {
	cmds := []string{
		"gh stack checkout feature-auth",
		"gh stack checkout auth",
		"gh stack checkout refactor/foo",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Checkout_NumberOrURL_Ask pins the NETWORK-mediated forms of
// `gh stack checkout` — a bare number (resolved as a stack number, then a PR number) and
// a PR URL — to Ask, per SKILL.md's "Check out a stack" section: "the command fetches the
// stack on GitHub, pulls the branches, and sets up the stack locally." A bare invocation
// (no positional at all) triggers an interactive selection menu per the SKILL.md's own
// "Never do" list and is also not the safe branch-name form, so it gets Ask too.
func TestGhStack_Checkout_NumberOrURL_Ask(t *testing.T) {
	cmds := []string{
		"gh stack checkout 7",
		"gh stack checkout 42",
		"gh stack checkout https://github.com/owner/repo/pull/42",
		"gh stack checkout http://github.com/owner/repo/pull/42",
		"gh stack checkout", // bare: interactive selection menu
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Add_Bare_Approve pins `gh stack add <branch>` with NO -A/-u/-m flag to
// Approve: it only creates a local branch on top of the stack, per SKILL.md "Add a
// branch" — no commit, no PR.
func TestGhStack_Add_Bare_Approve(t *testing.T) {
	cmds := []string{
		"gh stack add api-routes",
		"gh stack add refactor/foo",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Add_BundledCommit_Unclaimed pins EVERY documented form of `gh stack add`
// that bundles a `git add` + `git commit` (-A/--all, -u/--update, -m/--message, and their
// clustered short spellings) to being deliberately left UNCLAIMED — hookio.Verdict of an
// unclaimed leaf collapses to NoOpinion, the same terminal value chain exhaustion
// produces, which is the point: this rule must not itself manufacture an Approve, but it
// also must not be the one recording a NoOpinion VERDICT (see the package doc — that
// distinction matters at the RuleResult/error level even though Verdict() cannot show it).
func TestGhStack_Add_BundledCommit_Unclaimed(t *testing.T) {
	cmds := []string{
		"gh stack add -Am \"Add API routes\" api-routes",
		"gh stack add -um \"Fix auth bug\" auth-fix",
		"gh stack add -A -m msg api-routes",
		"gh stack add --all --message msg api-routes",
		"gh stack add -u -m msg auth-fix",
		"gh stack add --update --message msg auth-fix",
		"gh stack add -m msg api-routes",
		"gh stack add --message msg api-routes",
		"gh stack add --message=msg api-routes",
		"gh stack add -A api-routes",
		"gh stack add --all api-routes",
		"gh stack add -mfixup api-routes", // glued value, 'm' present
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed) — CETA cannot see what is being committed", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Unstack_Local_Approve pins `--local` (in either position relative to a
// stack number, and pflag's last-one-wins for a repeated/negated spelling) to Approve:
// SKILL.md states it "never contacts GitHub".
func TestGhStack_Unstack_Local_Approve(t *testing.T) {
	cmds := []string{
		"gh stack unstack --local",
		"gh stack unstack 7 --local",
		"gh stack unstack --local 7",
		"gh stack unstack --local=true",
		"gh stack unstack --local=false --local", // last-one-wins: back to local
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Unstack_Remote_Ask pins every unstack spelling WITHOUT --local — bare, and
// scoped to a specific stack number — to Ask: SKILL.md's own agent note calls this "a
// remote-first API wrapper" that "unstacks on GitHub by number from anywhere in the
// repo", and an explicit `--local=false` is the same remote case.
func TestGhStack_Unstack_Remote_Ask(t *testing.T) {
	cmds := []string{
		"gh stack unstack",
		"gh stack unstack 7",
		"gh stack unstack --local=false",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Push_Ask pins `gh stack push` (with or without --remote) to Ask: a batched,
// non-atomic multi-ref push across the WHOLE stack, per SKILL.md "Push branches to
// remote" — deliberately NOT loosened to the single-branch --force-with-lease Approve
// already granted in internal/rules/git (see rebaseVerdict's sibling doc in ghstack.go).
func TestGhStack_Push_Ask(t *testing.T) {
	cmds := []string{
		"gh stack push",
		"gh stack push --remote upstream",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Submit_Ask pins every documented `gh stack submit` spelling — --auto alone
// (drafts), --auto --open (ready for review), and the bare/no-flags form the SKILL.md's
// own "Never do" list warns hangs on a prompt — to Ask. See submitVerdict's doc for why
// neither --auto variant is promoted to Approve this pass.
func TestGhStack_Submit_Ask(t *testing.T) {
	cmds := []string{
		"gh stack submit --auto",
		"gh stack submit --auto --open",
		"gh stack submit --open --auto", // flag order independence
		"gh stack submit",               // bare: not a documented safe form either
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Link_Ask pins every `gh stack link` spelling to Ask: it creates new PRs
// with a draft-default the vendored SKILL.md leaves undocumented (see the package doc's
// "could become Approve or Reject" note).
func TestGhStack_Link_Ask(t *testing.T) {
	cmds := []string{
		"gh stack link branch-a branch-b branch-c",
		"gh stack link --base develop --open branch-a branch-b branch-c",
		"gh stack link 10 20 30",
		"gh stack link 7 48 feature-auth",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Sync_Ask pins every `gh stack sync` spelling to Ask: it bundles
// fetch+rebase+push+PR-sync+optional-prune with no per-step flag to soften any part of
// it, per SKILL.md "Sync the stack" — the riskiest "convenience" command in the family.
func TestGhStack_Sync_Ask(t *testing.T) {
	cmds := []string{
		"gh stack sync",
		"gh stack sync --remote origin",
		"gh stack sync --prune",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Rebase_Approve pins EVERY documented `gh stack rebase` flag/form to
// Approve, mirroring plain `git rebase`'s Approve verdict in internal/rules/git (see
// rebaseVerdict's doc for why this departs from an earlier Ask-bucketed design draft):
// gh-stack's rebase has no interactive/-i mode at all, so it is structurally always the
// non-interactive case git.go already approves.
func TestGhStack_Rebase_Approve(t *testing.T) {
	cmds := []string{
		"gh stack rebase",
		"gh stack rebase --downstack",
		"gh stack rebase --upstack",
		"gh stack rebase --no-trunk",
		"gh stack rebase --continue",
		"gh stack rebase --abort",
		"gh stack rebase --remote upstream",
		"gh stack rebase some-branch",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_Merge_Reject pins EVERY documented `gh stack merge` spelling — bare,
// --yes, scoped to a stack/PR number, and with each explicit merge-method flag — to
// Reject, mirroring `gh pr merge`'s immediate-merge Reject exactly. There is no
// deferred/queued flag spelling (no --auto equivalent) that would earn a carve-out.
func TestGhStack_Merge_Reject(t *testing.T) {
	cmds := []string{
		"gh stack merge",
		"gh stack merge --yes",
		"gh stack merge 7 --yes",
		"gh stack merge 42 --yes",
		"gh stack merge --yes --squash",
		"gh stack merge --yes --rebase",
		"gh stack merge --yes --merge",
		"gh stack merge --yes --merge-method squash",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.Reject {
			t.Errorf("cmd %q: got %s (%s), want Reject", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_ModifyAndUnknown_Unclaimed pins `gh stack modify` (the SKILL.md's own
// exit-code table states this skill never produces it) and an unrecognized/future
// subcommand to being left unclaimed (NoOpinion via hookio.Verdict), not guessed at.
func TestGhStack_ModifyAndUnknown_Unclaimed(t *testing.T) {
	cmds := []string{
		"gh stack modify",
		"gh stack modify --abort",
		"gh stack frobnicate",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (unclaimed)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_NonStackGhCommands_Unclaimed pins the EXACT-TOKEN resource match the
// vendored SKILL.md's own scope-gate section calls out: "a substring match on `stack`
// would also accept `gh stackoverflow` or `gh unstack`." Neither of those — nor gh's own
// built-in resources this rule has no business in — may be claimed here.
func TestGhStack_NonStackGhCommands_Unclaimed(t *testing.T) {
	cmds := []string{
		"gh unstack", // not a real gh command, but must not match resource=="stack"
		"gh stackoverflow",
		"gh pr view",
		"gh pr merge --auto",
		"gh issue create",
	}
	for _, cmd := range cmds {
		if got := evalGhStack(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (not this rule's business)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestGhStack_NonBashTool_Unclaimed pins that a non-Bash tool call is always unclaimed.
func TestGhStack_NonBashTool_Unclaimed(t *testing.T) {
	input := &hookio.HookInput{ToolName: "Read", ToolInput: mustJSON("")}
	if got := hookio.Verdict(New().Evaluate(input)).Decision; got != hookio.NoOpinion {
		t.Errorf("non-bash tool => %v, want NoOpinion", got)
	}
}

// TestGhStack_GlobalFlagBeforeCommandPath is the pg2-by1ij bypass-regression guard,
// scoped to gh-stack: gh registers each installed extension (`stack`) as a real command
// at its own cobra root, so the SAME "a global flag may precede or sit inside the command
// path" bypass gh.go measures for its own built-in resources applies here too — this rule
// reuses gh.CommandPath specifically so it cannot regress independently.
func TestGhStack_GlobalFlagBeforeCommandPath(t *testing.T) {
	tests := []struct {
		cmd  string
		want hookio.Decision
	}{
		{"gh --repo o/r stack view", hookio.Approve},
		{"gh --repo o/r stack merge", hookio.Reject},
		{"gh stack --repo o/r view", hookio.Approve}, // flag INSIDE the path
		{"gh -R o/r stack merge --yes", hookio.Reject},
	}
	for _, tt := range tests {
		got := evalGhStack(t, tt.cmd)
		if got.Decision != tt.want {
			t.Errorf("cmd %q: got %s (%s), want %s", tt.cmd, got.Decision, got.Reason, tt.want)
		}
	}
}

// TestGhStack_CompoundLeaf_StillMatches pins that this rule's own for-loop over every
// parsed leaf reaches a gh-stack invocation even when it is not the first leaf in a
// compound command, mirroring pgccaudit's identical coverage.
func TestGhStack_CompoundLeaf_StillMatches(t *testing.T) {
	cmd := "export FOO=bar && gh stack view --json"
	if got := evalGhStack(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
	}
}
