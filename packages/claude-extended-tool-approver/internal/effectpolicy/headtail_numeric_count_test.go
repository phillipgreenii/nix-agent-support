package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestHeadTailNumericCount pins bead tc-kf5m0: the obsolete GNU "-NUM" count
// spelling (head -20, tail -100) is an inert count like -n NUM, so a head/tail
// of a read-only input approves (including as a pipeline sink), while an
// unmodeled flag still abstains and credential-store / unknown-zone inputs are
// not widened by the new spelling.
func TestHeadTailNumericCount(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name string
		cmd  string
		want evalcontract.Decision
	}{
		{"head_numeric_file", "head -20 README.md", evalcontract.Approve},
		{"tail_numeric_file", "tail -100 README.md", evalcontract.Approve},
		{"head_numeric_pipe", "cat README.md | head -50", evalcontract.Approve},
		{"tail_numeric_pipe", "cat README.md | tail -25", evalcontract.Approve},
		{"head_explicit_n_still_approves", "head -n 20 README.md", evalcontract.Approve},
		{"tail_lines_long_still_approves", "tail --lines=20 README.md", evalcontract.Approve},
		{"tail_single_digit_still_approves", "tail -1 README.md", evalcontract.Approve},
		// An unmodeled flag still abstains; a non-digit tail of the -NUM
		// spelling (the -NUM[bkm][cqv] suffix forms) is not the plain count.
		{"head_unmodeled_flag_abstains", "head -z README.md", evalcontract.Abstain},
		{"head_numeric_suffix_abstains", "head -20q README.md", evalcontract.Abstain},
		{"tail_unmodeled_flag_abstains", "tail --debug README.md", evalcontract.Abstain},
		{"head_numeric_then_unmodeled_abstains", "head -20 -z README.md", evalcontract.Abstain},
		// Credential stores and unknown-zone absolute paths are not widened.
		{"head_numeric_ssh_key_not_approved", "head -20 ~/.ssh/id_ed25519", evalcontract.Reject},
		{"tail_numeric_dotenv_not_approved", "tail -100 .env", evalcontract.Reject},
		{"head_numeric_unknown_zone_not_approved", "head -20 /srv/outside-zone/data.txt", evalcontract.Abstain},
		{"tail_numeric_pipe_ssh_key_not_approved", "tail -100 $HOME/.ssh/id_ed25519 | head -5", evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q = %v (%s), want %v", c.name, c.cmd, resp.Decision, resp.Reason, c.want)
		}
	}
}
