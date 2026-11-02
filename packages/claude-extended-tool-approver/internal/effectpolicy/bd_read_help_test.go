package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestBdReadHelpAndBlocked pins bead tc-gyiyl (absorbing Bash(bd:*)).
// Replaying decision-DB bd rows showed two bd-owned read gaps: --help
// (root, groups, read verbs) and the `blocked` read verb. Write verbs and
// the --help form of mutating verbs must keep abstaining.
func TestBdReadHelpAndBlocked(t *testing.T) {
	root, _ := fixture(t)
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", "")
	t.Setenv("WORKSPACE_ROOT", "")
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name string
		cmd  string
		want evalcontract.Decision
	}{
		{"blocked", "bd blocked --json", evalcontract.Approve},
		{"blocked_parent", "bd blocked --parent tc-abc", evalcontract.Approve},
		{"root_help", "bd --help", evalcontract.Approve},
		{"read_verb_help", "bd list --help", evalcontract.Approve},
		{"group_help", "bd dep --help", evalcontract.Approve},
		{"comments_help", "bd comments --help", evalcontract.Approve},
		{"mutating_help_abstains", "bd dep remove --help", evalcontract.Abstain},
		{"init_help_abstains", "bd init --help", evalcontract.Abstain},
		{"write_reopen_abstains", "bd reopen tc-abc", evalcontract.Abstain},
		{"write_delete_abstains", "bd delete tc-abc", evalcontract.Abstain},
		{"unmodeled_verb_abstains", "bd config --help", evalcontract.Abstain},
		{"root_unmodeled_flag_abstains", "bd --bogus list", evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q\n  decision = %s, want %s\n  reason: %s", c.name, c.cmd, resp.Decision, c.want, resp.Reason)
		}
	}
}
