package rcpreflight

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookio.BashToolInput{Command: cmd})
	return b
}

// evalRcPreflight is the shared one-command driver, mirroring pnwf/gh-stack's
// evalPnwf/evalGhStack.
func evalRcPreflight(t *testing.T, cmd string) hookio.RuleResult {
	t.Helper()
	return hookio.Verdict(New().Evaluate(&hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: mustJSON(cmd),
	}))
}

// TestRcPreflight_Help_Approve pins -h/--help to Approve: it only prints usage and
// exits 0.
func TestRcPreflight_Help_Approve(t *testing.T) {
	cmds := []string{"rc-preflight -h", "rc-preflight --help"}
	for _, cmd := range cmds {
		if got := evalRcPreflight(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestRcPreflight_Verify_Approve pins --verify to Approve: it is a read-only
// toplevel+lock+clean-tree assertion that never mutates, per the script's own comment.
func TestRcPreflight_Verify_Approve(t *testing.T) {
	cmds := []string{
		"rc-preflight --verify /tmp/rc-pool-1 my-actor",
		"rc-preflight --verify", // missing args -> the script itself dies with a usage error, but still no mutation
	}
	for _, cmd := range cmds {
		if got := evalRcPreflight(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestRcPreflight_Release_Approve pins --release to Approve: it removes only the lock
// file(s) owned by the given actor and always exits 0 — the documented self-release
// call every zr-refactor work session makes at STOP.
func TestRcPreflight_Release_Approve(t *testing.T) {
	cmds := []string{
		"rc-preflight --release my-actor",
		"rc-preflight --release",
	}
	for _, cmd := range cmds {
		if got := evalRcPreflight(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestRcPreflight_List_Approve pins --list to Approve: it is a pure read-only report of
// pool members/holders/held-since.
func TestRcPreflight_List_Approve(t *testing.T) {
	if got := evalRcPreflight(t, "rc-preflight --list"); got.Decision != hookio.Approve {
		t.Errorf("got %s (%s), want Approve", got.Decision, got.Reason)
	}
}

// TestRcPreflight_BareAcquire_Approve pins the bare `rc-preflight <actor>` acquire form
// to Approve: idempotent for the caller's own actor, or reserves a free pool slot and
// creates its own worktree — never touches another actor's held member. Includes a
// single-dash token that is not "-h": the real script's case statement does not match
// it against any flag arm either, so it falls through to the bare-acquire default arm
// exactly as an ordinary actor string would.
func TestRcPreflight_BareAcquire_Approve(t *testing.T) {
	cmds := []string{
		"rc-preflight my-actor",
		"rc-preflight session-123",
		"rc-preflight -x",
	}
	for _, cmd := range cmds {
		if got := evalRcPreflight(t, cmd); got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
		}
	}
}

// TestRcPreflight_ForceRelease_Ask pins --force-release to Ask (never Approve): it
// force-evicts an arbitrary pool member's held lock with no owner check at all,
// targeting by pool-slot number rather than actor identity, and is explicitly
// documented as the operator's own call to make.
func TestRcPreflight_ForceRelease_Ask(t *testing.T) {
	cmds := []string{
		"rc-preflight --force-release 1",
		"rc-preflight --force-release",
	}
	for _, cmd := range cmds {
		if got := evalRcPreflight(t, cmd); got.Decision != hookio.Ask {
			t.Errorf("cmd %q: got %s (%s), want Ask", cmd, got.Decision, got.Reason)
		}
	}
}

// TestRcPreflight_ForceReleaseGluedOrUnknown_NotApplicable pins that a glued
// `--force-release=<n>` spelling, and any other unrecognized "--"-prefixed option, is
// left UNCLAIMED rather than approved or guessed at — rc-preflight.sh's own exact-string
// case match does not recognize the glued form either (it falls to the script's own
// `--*` catch-all, "unknown option"), so this must never resolve to Approve.
func TestRcPreflight_ForceReleaseGluedOrUnknown_NotApplicable(t *testing.T) {
	cmds := []string{
		"rc-preflight --force-release=1",
		"rc-preflight --some-future-flag",
	}
	for _, cmd := range cmds {
		got := evalRcPreflight(t, cmd)
		if got.Decision != hookio.NoOpinion {
			t.Errorf("cmd %q: got %s (%s), want NoOpinion (chain exhaustion)", cmd, got.Decision, got.Reason)
		}
	}
}

// TestRcPreflight_BareNoArgs_NotApplicable pins that `rc-preflight` with no arguments at
// all is left unclaimed — there is no flag and no actor to classify.
func TestRcPreflight_BareNoArgs_NotApplicable(t *testing.T) {
	if got := evalRcPreflight(t, "rc-preflight"); got.Decision != hookio.NoOpinion {
		t.Errorf("got %s (%s), want NoOpinion (chain exhaustion)", got.Decision, got.Reason)
	}
}

// TestRcPreflight_NonBashTool_NotApplicable pins that this rule only ever looks at Bash
// tool calls.
func TestRcPreflight_NonBashTool_NotApplicable(t *testing.T) {
	input := &hookio.HookInput{ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"/tmp/x"}`)}
	if got := hookio.Verdict(New().Evaluate(input)); got.Decision != hookio.NoOpinion {
		t.Errorf("non-Bash tool: got %s (%s), want NoOpinion (chain exhaustion)", got.Decision, got.Reason)
	}
}

// TestRcPreflight_CompoundWithPrefix_Approve pins that this rule's own for-loop over
// every parsed leaf independently reaches the rc-preflight leaf, even when it is not the
// first leaf in a compound command — mirroring pnwf/ghstack's identical coverage.
func TestRcPreflight_CompoundWithPrefix_Approve(t *testing.T) {
	cmd := "export FOO=bar && rc-preflight --list"
	if got := evalRcPreflight(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
	}
}

// TestRcPreflight_BasenamePath_Approve pins that the executable is matched by basename,
// not literal string equality — an rc-preflight invoked via an absolute/relative path
// still matches.
func TestRcPreflight_BasenamePath_Approve(t *testing.T) {
	cmd := "/nix/store/abc-rc-preflight-1.0/bin/rc-preflight --list"
	if got := evalRcPreflight(t, cmd); got.Decision != hookio.Approve {
		t.Errorf("cmd %q: got %s (%s), want Approve", cmd, got.Decision, got.Reason)
	}
}
