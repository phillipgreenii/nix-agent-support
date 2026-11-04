package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestGitRevParseReadOnlyFlags pins bead tc-cw7sf (absorbing the settings
// rule Bash(git rev-parse:*)). Replaying the decision DB showed two spec gaps:
// --is-shallow-repository and `--verify -q`/--quiet fell to the unknown-flag
// path and abstained. The remaining read-only query flags are modeled beside
// them; the stdin/path-taking modes (--parseopt, --sq-quote,
// --resolve-git-dir) and a --git-dir=<path> / -C into an unknown zone or a
// credential store stay conservative.
func TestGitRevParseReadOnlyFlags(t *testing.T) {
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
		{"is_shallow_repository", "git rev-parse --is-shallow-repository", evalcontract.Approve},
		{"verify_short_quiet", "git rev-parse --verify -q main", evalcontract.Approve},
		{"verify_long_quiet_peel", "git rev-parse --verify --quiet main^{commit}", evalcontract.Approve},
		{"is_inside_git_dir", "git rev-parse --is-inside-git-dir", evalcontract.Approve},
		{"superproject", "git rev-parse --show-superproject-working-tree", evalcontract.Approve},
		{"sibling_workspace_repo", "git -C /home/nobody-ws/other rev-parse --is-shallow-repository", evalcontract.Approve},
		{"credential_store_rejected", "git -C ~/.ssh rev-parse HEAD", evalcontract.Reject},
		{"unknown_zone_abstains", "git -C /home/nobody/elsewhere rev-parse HEAD", evalcontract.Abstain},
		{"git_dir_option_abstains", "git --git-dir=/home/nobody/.ssh rev-parse HEAD", evalcontract.Abstain},
		{"parseopt_abstains", "git rev-parse --parseopt -- a", evalcontract.Abstain},
		{"sq_quote_abstains", "git rev-parse --sq-quote a", evalcontract.Abstain},
		{"resolve_git_dir_abstains", "git rev-parse --resolve-git-dir ~/.ssh", evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q = %v (%s), want %v", c.name, c.cmd, resp.Decision, resp.Reason, c.want)
		}
	}
}
