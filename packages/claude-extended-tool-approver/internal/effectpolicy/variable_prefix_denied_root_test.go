package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestShellVariablePrefixIsNotADeniedRoot pins that a path whose first segment
// is a shell variable (the variable is only known at run time) is never judged
// as an absolute path under a denied root such as /home: the engine abstains,
// while a literal path under the denied root is still rejected.
func TestShellVariablePrefixIsNotADeniedRoot(t *testing.T) {
	root, _ := fixture(t)
	t.Setenv("CETA_DENIED_ROOTS", "/home")
	reg := cmddesc.DefaultRegistry()
	cases := []struct {
		command string
		want    evalcontract.Decision
		why     string
	}{
		{"ls $Z/home/ziprecruiter/config/ceta/", evalcontract.Abstain, "unset-looking variable prefix"},
		{"ls ${Z}/home/ziprecruiter/config/ceta/", evalcontract.Abstain, "braced variable prefix"},
		{"Z=/Volumes/gitrepos/phillipg/nix-ziprecruiter; ls $Z/home/ziprecruiter/config/ceta/", evalcontract.Abstain, "variable assigned in the same command"},
		// Negative control: a literal path under the denied root is still refused.
		{"ls /home/user/x", evalcontract.Reject, "literal denied-root path"},
	}
	for _, c := range cases {
		resp := Evaluate(evalcontract.Request{Command: c.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != c.want {
			t.Errorf("%q (%s)\n  decision = %s, want %s\n  reason: %s", c.command, c.why, resp.Decision, c.want, resp.Reason)
		}
	}
}
