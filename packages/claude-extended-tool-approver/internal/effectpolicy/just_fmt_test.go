package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestJustFmt pins bead tc-k0r5o (absorbing Bash(just --fmt:*)). `just --fmt
// --check` prints a diff and writes nothing; plain `just --fmt` overwrites the
// justfile in place, a bounded write that approves only inside the trusted
// zone. A recipe run, an unmodeled flag and a justfile outside the zone must
// not widen.
func TestJustFmt(t *testing.T) {
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
		{"check_f_relative", `just --fmt --check --unstable -f justfile 2>&1`, evalcontract.Approve},
		{"check_f_abs_in_repo", `just --fmt --check --unstable -f ` + root + `/justfile`, evalcontract.Approve},
		{"check_no_f", `just --fmt --check --unstable`, evalcontract.Approve},
		{"write_in_repo", `just --fmt --unstable`, evalcontract.Approve},
		{"write_f_in_repo", `just --fmt --unstable -f ` + root + `/justfile`, evalcontract.Approve},
		{"recipe_run_abstains", `just build`, evalcontract.Abstain},
		{"unmodeled_flag_abstains", `just --fmt --check --bogus`, evalcontract.Abstain},
		{"check_f_unknown_zone_not_approved", `just --fmt --check --unstable -f /etc/justfile`, evalcontract.Abstain},
		{"write_f_unknown_zone_not_approved", `just --fmt --unstable -f /etc/justfile`, evalcontract.Abstain},
		{"write_f_credential_store_rejected", `just --fmt --unstable -f ~/.ssh/justfile`, evalcontract.Reject},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.cmd, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%s: %q\n  decision = %s, want %s\n  reason: %s", c.name, c.cmd, resp.Decision, c.want, resp.Reason)
		}
	}
}
