package pnwf

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// evalPnwf is the shared one-command driver, mirroring gh/ghstack's evalGH/evalGhStack.
func evalPnwf(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New().Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(cmd),
	}))
}

// TestPnwf_ReadOnlyProbes_Approve pins the read-only, no-mutation-surface band pnwf.sh's own
// "Subcommands (read-only, implemented)" list documents: resolve/repos/stage/land-plan/
// status/residue, with and without their documented flags.
func TestPnwf_ReadOnlyProbes_Approve(t *testing.T) {
	cmds := []string{
		"pnwf resolve",
		"pnwf resolve --set",
		"pnwf repos",
		"pnwf repos --set",
		"pnwf stage",
		"pnwf stage --set",
		"pnwf land-plan my-branch",
		"pnwf status my-branch",
		"pnwf residue",
		"pnwf residue --set",
	}
	for _, cmd := range cmds {
		if got := evalPnwf(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPnwf_ForkPreflight_Approve pins fork-preflight (bare and with --repos) to Approve: it
// only ever prints proceed/resume/stop plus a reason and never mutates anything, even on its
// "stop" paths.
func TestPnwf_ForkPreflight_Approve(t *testing.T) {
	cmds := []string{
		"pnwf fork-preflight my-branch",
		"pnwf fork-preflight my-branch --repos repoA,repoB",
	}
	for _, cmd := range cmds {
		if got := evalPnwf(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPnwf_SyncFetchUpdateRelock_Approve pins the two mutating WORK-recipe helpers that
// mirror an already-approved `pn workspace` verb (operator ruling pg2-4zyqf/pg2-vlpe1,
// pg2-tvdh3): sync-fetch mirrors rebase+push, update-relock mirrors update --in-place. Both
// only take --set.
func TestPnwf_SyncFetchUpdateRelock_Approve(t *testing.T) {
	cmds := []string{
		"pnwf sync-fetch",
		"pnwf sync-fetch --set",
		"pnwf update-relock",
		"pnwf update-relock --set",
	}
	for _, cmd := range cmds {
		if got := evalPnwf(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPnwf_Cleanup_NoForceFlag_Approve pins the "closed flag surface" shape of cleanup: with
// neither force flag present, the only mutation possible is removing an already-confirmed-
// landed member via wtdone (itself blanket-approved) — the same shape wtdone is safe-listed
// for.
func TestPnwf_Cleanup_NoForceFlag_Approve(t *testing.T) {
	cmds := []string{
		"pnwf cleanup my-branch",
	}
	for _, cmd := range cmds {
		if got := evalPnwf(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPnwf_Cleanup_ForceFlag_Ask pins the inverse: EITHER force flag present bypasses one of
// the two checks that make the bare form safe (force-removing a dirty worktree, or
// `git branch -D`-deleting a not-yet-landed branch) — unlike wtdone, cleanup's flag surface is
// NOT closed, so this defaults to Ask rather than the bare form's Approve.
func TestPnwf_Cleanup_ForceFlag_Ask(t *testing.T) {
	cmds := []string{
		"pnwf cleanup my-branch --force-dirty-worktree-removal",
		"pnwf cleanup my-branch --force-unlanded-branch-removal",
		"pnwf cleanup my-branch --force-dirty-worktree-removal --force-unlanded-branch-removal",
		// Flag before the branch positional — pnwf.sh's own arg loop accepts either order.
		"pnwf cleanup --force-unlanded-branch-removal my-branch",
	}
	for _, cmd := range cmds {
		if got := evalPnwf(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPnwf_UnrecognizedSubcommand_NotApplicable pins that an unrecognized/future pnwf
// subcommand is left unclaimed rather than guessed at — chain exhaustion, not a wrong
// Approve.
func TestPnwf_UnrecognizedSubcommand_NotApplicable(t *testing.T) {
	cmds := []string{
		"pnwf some-future-subcommand",
		"pnwf",
	}
	for _, cmd := range cmds {
		if got := evalPnwf(t, cmd); got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (chain exhaustion)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestPnwf_NonBashTool_NotApplicable pins that this rule only ever looks at Bash tool calls.
func TestPnwf_NonBashTool_NotApplicable(t *testing.T) {
	input := &hookio.HookInput{ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"/tmp/x"}`)}
	if got := hookio.Verdict(New().Evaluate(input)); got.Decision != hookio.NoOpinion {
		t.Errorf("non-Bash tool: got %s (%s), want NoOpinion (chain exhaustion)", got.Decision, got.Reason)
	}
}

// TestPnwf_CompoundWithPrefix_Approve pins that this rule's own for-loop over every parsed
// leaf independently reaches the pnwf leaf, even when it is not the first leaf in a compound
// command — mirroring pnworkspace's identical coverage for `pn`.
func TestPnwf_CompoundWithPrefix_Approve(t *testing.T) {
	cmd := "export FOO=bar && pnwf resolve --set"
	if got := evalPnwf(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
	}
}

// TestPnwf_BasenamePath_Approve pins that the executable is matched by basename, not literal
// string equality — a `pnwf` invoked via an absolute/relative path still matches.
func TestPnwf_BasenamePath_Approve(t *testing.T) {
	cmd := "/nix/store/abc-pnwf-1.0/bin/pnwf resolve"
	if got := evalPnwf(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
	}
}
