//go:build integration

// Characterization for tc-h3mrb: PG_WI_FLOW_IDENT is stamped by an input
// processor, and ceta runs input processors ONLY when its own verdict is
// Approve/Ask (ADR 0070, deliberately not widened to Abstain). A command whose
// whole-expression verdict is Abstain therefore reaches Claude Code unstamped:
// a heredoc fed to pg-wi-flow's own stdin (no heredocStdinSinkFlags carve-out
// for it) and a function definition plus its call (`f` has no rule) both
// abstain. This drives the REAL compiled binary with the REAL
// pg-wi-flow-identity processor (no mock of either), so it pins both halves of
// the documented limitation: which shapes lose the stamp, and the compound
// shapes that keep it (the supported spellings, which the pg-wi-flow identity
// hint and the reviewer/worker agent text name).

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegration_InputProcessor_PgWiFlowIdentityStampVsCompoundCommands(t *testing.T) {
	// Resolve the processor BEFORE HOME is redirected, and build the binary
	// before it too (see TestIntegration_InputProcessor_RtkRewriteDroppedInLinkedWorktree).
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	processor := filepath.Join(wd, "..", "..", "..", "pg-wi-flow", "pg-wi-flow-identity", "pg-wi-flow-identity.sh")
	if _, err := os.Stat(processor); err != nil {
		t.Skipf("pg-wi-flow-identity.sh not reachable from the module source (nix build sandbox?): %v", err)
	}
	testBinary(t)

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CETA_INPUT_PROCESSORS", "bash "+processor)

	const stamp = "export PG_WI_FLOW_IDENT=b53c7f3a-main-main; "
	cases := []struct {
		name, command string
		stamped       bool
	}{
		{"simple", "pg-wi-flow next", true},
		{"pipeline", "echo '{}' | pg-wi-flow record-verdict x --concern c --json -", true},
		{"json-file", "pg-wi-flow record-verdict x --concern c --json /tmp/tc-h3mrb-verdict.json", true},
		{"heredoc-file-write-then-call", "cat > /tmp/tc-h3mrb-note <<'EOF'\nverdict body\nEOF\npg-wi-flow annotate x --kind k", true},
		{"heredoc-to-pg-wi-flow", "pg-wi-flow record-verdict x --concern c --json - <<'EOF'\n{\"verdict\":\"ready\"}\nEOF", false},
		{"function-definition", "f() { pg-wi-flow next; }\nf", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := `{"tool_name":"Bash","tool_input":{"command":` + jsonString(tc.command) + `},"session_id":"b53c7f3a-0000-0000-0000-000000000000","cwd":"/tmp"}`
			res, stderr := runHookOnce(t, in)
			c, ok := updatedCommand(res)
			if tc.stamped {
				if !ok || !strings.HasPrefix(c, stamp) {
					t.Errorf("decision=%q updatedInput.command=%q (present=%v), want the %q stamp\nstderr: %s", getDecision(res), c, ok, stamp, stderr)
				}
				return
			}
			// Unstamped because ceta abstained (ADR 0070), not because the
			// processor declined: assert the cause, not just the absence.
			if ok || getDecision(res) != "" {
				t.Errorf("decision=%q updatedInput.command=%q (present=%v), want an abstain with no rewrite (ADR 0070); if ceta now stamps this shape, update the pg-wi-flow identity hint and agent text", getDecision(res), c, ok)
			}
		})
	}
}
