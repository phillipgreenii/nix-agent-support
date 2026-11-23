package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// A hook-bypass config key (core.hooksPath) typed in a `git -c` is Rejected. The same
// key reaching git by INDIRECTION is not statically visible, so the engine ABSTAINS
// (P3: unknown never approves; ADR 0075 "dynamic expansions" are Uncertain) and the
// verdict goes to Claude Code's own prompt. Two shapes were reported as slipping
// through: a shell variable and a wrapper's argv. Neither may ever Approve.
//
// Static resolution of the variable is deliberately out of scope: the engine does not
// track assignments for subcommand and flag tokens, and abstaining is the fail-closed
// answer. Abstain, not Reject, is the contract; an auto-approving Claude Code mode is
// the documented consequence of every Abstain.
func TestHooksPathIndirectionNeverApproves(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name, command string
		want          evalcontract.Decision
	}{
		{"typed_direct", "git -c core.hooksPath=/dev/null status", evalcontract.Reject},
		{"typed_through_env_wrapper", "env git -c core.hooksPath=/dev/null status", evalcontract.Reject},
		{"shell_variable_assigned", "G='-c core.hooksPath=/dev/null'; git $G status", evalcontract.Abstain},
		{"shell_variable_assigned_commit", "G='-c core.hooksPath=/dev/null'; git $G commit -m x", evalcontract.Abstain},
		{"shell_variable_unassigned", "git $G status", evalcontract.Abstain},
		{"shell_variable_braced", "G='-c core.hooksPath=/dev/null'; git ${G} status", evalcontract.Abstain},
		{"command_substitution", "git $(echo -c core.hooksPath=/dev/null) status", evalcontract.Abstain},
		{"unknown_wrapper_argv", "pg-rescue git -c core.hooksPath=/dev/null status", evalcontract.Abstain},
		{"unknown_wrapper_argv_dashdash", "pg-rescue -- git -c core.hooksPath=/dev/null commit -m x", evalcontract.Abstain},
		{"git_variable_positional", "git log $REF", evalcontract.Abstain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := Evaluate(evalcontract.Request{Command: tc.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
			if resp.Decision == evalcontract.Approve {
				t.Fatalf("%q Approved (reason: %s)", tc.command, resp.Reason)
			}
			if resp.Decision != tc.want {
				t.Errorf("%q: decision = %s, want %s (reason: %s)", tc.command, resp.Decision, tc.want, resp.Reason)
			}
		})
	}
}
