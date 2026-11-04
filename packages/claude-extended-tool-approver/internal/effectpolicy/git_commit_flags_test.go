package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestGitCommitFlags pins bead tc-iqhja (absorbing the settings rule
// Bash(git commit:*)). Replaying the decision DB showed one spec gap for
// `git commit` itself: --dry-run (no commit, no hooks) hit the unknown-flag
// path. It is modeled beside documented harmless siblings. Everything that
// skips hooks, reuses/reads more than the message text, or rewrites history
// metadata stays an abstain; a -F / -C path into a credential store or
// unknown zone does not widen.
func TestGitCommitFlags(t *testing.T) {
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
		{"plain_m", `git commit -m "fix: x"`, evalcontract.Approve},
		{"dry_run", "git commit --dry-run", evalcontract.Approve},
		{"dry_run_with_message", `git commit -m test --dry-run`, evalcontract.Approve},
		{"status", `git commit --status -m x`, evalcontract.Approve},
		{"no_status", `git commit --no-status -m x`, evalcontract.Approve},
		{"allow_empty_message", `git commit --allow-empty-message -m ""`, evalcontract.Approve},
		{"no_gpg_sign", `git commit --no-gpg-sign -m x`, evalcontract.Approve},
		{"quiet_amend_no_edit", `git commit -q --amend --no-edit`, evalcontract.Approve},
		// The workspace rule: never --no-verify / -n without explicit approval.
		{"no_verify_long_abstains", `git commit --no-verify -m x`, evalcontract.Abstain},
		{"no_verify_trailing_abstains", `git commit -m x --no-verify`, evalcontract.Abstain},
		{"no_verify_short_abstains", `git commit -n -m x`, evalcontract.Abstain},
		{"amend_no_verify_abstains", `git commit --amend --no-verify -F msg.txt`, evalcontract.Abstain},
		// Unmodeled options that run/read more than the message text.
		{"reuse_message_abstains", `git commit -C HEAD~1`, evalcontract.Abstain},
		{"reedit_message_abstains", `git commit -c HEAD~1`, evalcontract.Abstain},
		{"template_abstains", `git commit --template msg.txt`, evalcontract.Abstain},
		{"date_abstains", `git commit -m x --date=2020-01-01`, evalcontract.Abstain},
		{"gpg_sign_abstains", `git commit -S -m x`, evalcontract.Abstain},
		{"pathspec_from_file_abstains", `git commit --pathspec-from-file=list.txt -m x`, evalcontract.Abstain},
		{"made_up_flag_abstains", `git commit --files a.txt -m x`, evalcontract.Abstain},
		// Path operands do not widen.
		{"file_credential_store_rejected", `git commit -F ~/.ssh/id_rsa`, evalcontract.Reject},
		{"C_credential_store_rejected", `git -C ~/.ssh commit -m x`, evalcontract.Reject},
		{"C_unknown_zone_abstains", `git -C /home/nobody/elsewhere commit -m x`, evalcontract.Abstain},
		{"git_dir_abstains", `git --git-dir=/home/nobody/.ssh commit -m x`, evalcontract.Abstain},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q = %v (%s), want %v", c.name, c.cmd, resp.Decision, resp.Reason, c.want)
		}
	}
}
