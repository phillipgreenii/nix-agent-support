package effectpolicy

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
)

// TestAssumeIsRejected pins the operator ruling of 2026-11-17 (Phillip: "yes
// reject assume --exec like assume."; "as long as assume isn't allowed to be
// called by the agent, then it would only be able to access prod systems if it
// was run in an assumed environment which is only possible if i have
// explicitly done so"). The effect engine rejects by effect: the assume schema
// states one assume-role remote effect that RemoteMutation forbids, so every
// spelling rejects, `source assume` and `. assume` included (the builtins reach
// the same schema through source's own assume subcommand). A call by absolute
// path keeps abstaining as an untrusted executable identity (ADR 0075 P4).
func TestAssumeIsRejected(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	forms := []struct {
		command string
		want    evalcontract.Decision
		why     string
	}{
		{"assume dev/example-role", evalcontract.Reject, "bare assume"},
		{"assume", evalcontract.Reject, "assume with no role"},
		{`assume dev/example-role --exec "bin/tool show x"`, evalcontract.Reject, "--exec with an approvable inner command"},
		{"assume dev/example-role --exec=ls", evalcontract.Reject, "--exec= glued"},
		{`assume --exec "ls" dev/example-role`, evalcontract.Reject, "--exec before the role"},
		{"assume dev/example-role --exec", evalcontract.Reject, "--exec with no value"},
		{"assume --frobnicate dev/example-role", evalcontract.Reject, "an unknown flag cannot remove the effect"},
		{"source assume dev/example-role", evalcontract.Reject, "source assume"},
		{`source assume dev/example-role --exec "bin/tool show x"`, evalcontract.Reject, "source assume --exec"},
		{"source assume dev/example-role --exec=ls", evalcontract.Reject, "source assume --exec="},
		{". assume dev/example-role", evalcontract.Reject, "dot assume"},
		{`. assume dev/example-role --exec "bin/tool show x"`, evalcontract.Reject, ". assume --exec"},
		{"cd /tmp && assume dev/example-role --exec ls", evalcontract.Reject, "assume later in a list"},
		{"ls | assume dev/example-role", evalcontract.Reject, "assume in a pipeline"},

		// Not assume: unchanged.
		{"echo assume", evalcontract.Approve, "assume only as an argument"},
		{"source ./env.sh", evalcontract.Abstain, "sourcing another script is unmodeled, as before"},
		{". ./env.sh", evalcontract.Abstain, "dot-sourcing another script is unmodeled, as before"},
		{"source", evalcontract.Abstain, "source with no script"},
	}
	for _, f := range forms {
		resp := Evaluate(evalcontract.Request{Command: f.command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != f.want {
			t.Errorf("%q (%s)\n  decision = %s, want %s\n  reason: %s", f.command, f.why, resp.Decision, f.want, resp.Reason)
		}
		if f.want == evalcontract.Reject && !strings.Contains(resp.Reason, "assume") {
			t.Errorf("%q: reject reason %q does not name the assume ruling", f.command, resp.Reason)
		}
	}
}

// TestAWSCredentialEnvCurrentVerdicts PINS the current verdicts of the AWS
// credential-environment forms: every one is approved today (an unclassified
// name assigned statically is Permitted by EnvAssignment; env -u only unsets).
// This is NOT a ruling that approval is right: which credential setups to
// reject is a separate operator decision, and a change to these rows must be
// that decision, made deliberately.
func TestAWSCredentialEnvCurrentVerdicts(t *testing.T) {
	root, _ := fixture(t)
	reg := cmddesc.DefaultRegistry()
	for _, command := range []string{
		"AWS_PROFILE=x ls",
		"export AWS_PROFILE=x",
		"export AWS_SESSION_TOKEN=x",
		"env -u AWS_SESSION_TOKEN AWS_PROFILE=x ls",
	} {
		resp := Evaluate(evalcontract.Request{Command: command, CWD: root, ProjectRoot: root}, reg, DefaultPolicies(), DefaultGraphPolicies())
		if resp.Decision != evalcontract.Approve {
			t.Errorf("%q: decision = %s, want approve (pinned current verdict)\n  reason: %s", command, resp.Decision, resp.Reason)
		}
	}
}
