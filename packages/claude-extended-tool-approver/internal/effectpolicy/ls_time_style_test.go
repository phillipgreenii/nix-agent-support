package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestLsTimeStyleAndHelp pins bead tc-monxg (absorbing the settings rule
// Bash(ls:*)). Replaying the decision-DB ls rows showed one ls-owned spec gap:
// `--time-style=STYLE` (and `--help`) were unmodeled, so
// `ls -ld --time-style=long-iso PATH` abstained. Both are display-only. The
// guardrails: a credential store and an unknown-zone absolute path must not
// widen.
func TestLsTimeStyleAndHelp(t *testing.T) {
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
		{"time_style_glued", "ls -ld --time-style=long-iso README.md", evalcontract.Approve},
		{"time_style_separate", "ls -l --time-style full-iso .", evalcontract.Approve},
		{"help", "ls --help", evalcontract.Approve},
		{"time_style_ssh_still_rejected", "ls -l --time-style=long-iso ~/.ssh/id_rsa", evalcontract.Reject},
		{"time_style_unknown_zone_stays_abstain", "ls -l --time-style=long-iso /var/lib/elsewhere", evalcontract.Abstain},
		{"unmodeled_flag_stays_abstain", "ls --bogus README.md", evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q\n  decision = %s, want %s\n  reason: %s", c.name, c.cmd, resp.Decision, c.want, resp.Reason)
		}
	}
}
