package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestGrepIgnoreBinaryAndWorkspaceSiblings pins bead tc-zfzyt (absorbing the
// settings rule Bash(grep:*)). Two facts:
//
//   - `-I`/`--ignore-binary` is an inert grep flag (ugrep 7.8.4 `grep --help`).
//     Before this bead the schema omitted it, so `grep -rIn PAT dir` fell to
//     the unknown-flag path and abstained (decision-DB rows 156162, 156163,
//     166058).
//   - An absolute path under a sibling pn-workspace repo is a read only when
//     WORKSPACE_ROOT names the workspace (the session grant); an UNRELATED
//     absolute path still abstains, and a credential store still rejects.
func TestGrepIgnoreBinaryAndWorkspaceSiblings(t *testing.T) {
	root, _ := fixture(t)
	t.Setenv("CETA_EXTRA_READONLY_ROOTS", "")
	t.Setenv("CETA_EXTRA_READWRITE_ROOTS", "")
	t.Setenv("WORKSPACE_ROOT", "/home/nobody-ws")
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		name string
		cmd  string
		want evalcontract.Decision
	}{
		{"ignore_binary_short_clustered", "grep -rIn -i 'healthchecks' .", evalcontract.Approve},
		{"ignore_binary_short", "grep -I foo README.md", evalcontract.Approve},
		{"ignore_binary_long", "grep --ignore-binary foo README.md", evalcontract.Approve},
		{"ignore_binary_ssh_still_rejected", "grep -I foo ~/.ssh/config", evalcontract.Reject},
		{"sibling_repo_absolute_path", "grep -rn foo /home/nobody-ws/other-repo/src", evalcontract.Approve},
		{"unrelated_absolute_path_stays_abstain", "grep -rn foo /home/nobody/elsewhere/src", evalcontract.Abstain},
		{"unmodeled_flag_stays_abstain", "grep --bogus foo README.md", evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q\n  decision = %s, want %s\n  reason: %s", c.name, c.cmd, resp.Decision, c.want, resp.Reason)
		}
	}
}
