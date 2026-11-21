package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// A runtime `cd` loses the working directory, but a well-known secret name
// read afterwards is still a Reject: the joint verdict is never looser than
// the secret read alone.
func TestUnknownCwdKeepsWellKnownSecretRejected(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	eval := func(c string) evalcontract.Decision {
		return Evaluate(evalcontract.Request{Command: c, CWD: "/repo", ProjectRoot: "/repo"}, reg, DefaultPolicies(), DefaultGraphPolicies()).Decision
	}
	if got := eval("tail -100 .ssh/id_rsa"); got != evalcontract.Reject {
		t.Fatalf("baseline verdict = %s, want reject", got)
	}
	for _, c := range []string{
		"cd \"$p\"\ntail -100 .ssh/id_rsa",
		"p=$(pg-gwm lease --name x -b feature/x --print=path) && cd \"$p\"\ntail -100 .ssh/id_rsa",
	} {
		if got := eval(c); got != evalcontract.Reject {
			t.Errorf("%q = %s, want reject", c, got)
		}
	}
	// A non-secret relative read after the same cd still abstains.
	if got := eval("cd \"$p\"\ncat notes.txt"); got != evalcontract.Abstain {
		t.Errorf("non-secret after unknown cd = %s, want abstain", got)
	}
}
