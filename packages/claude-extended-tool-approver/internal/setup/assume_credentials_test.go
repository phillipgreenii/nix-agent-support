package setup

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func assumeChainVerdict(t *testing.T, cwd, command string) hookio.RuleResult {
	t.Helper()
	ti, _ := json.Marshal(map[string]string{"command": command})
	in := &hookio.HookInput{
		SessionID: "s", CWD: cwd, ToolName: "Bash", ToolInput: ti,
		PermissionMode: "dontAsk", HookEventName: "PreToolUse", ToolUseID: "t",
		Unattended: true,
	}
	verdict, _ := NewEngineForCWDLiveUnattended(cwd, nil).EvaluateHookBoth(in)
	return verdict
}

// TestLiveHook_AssumeAndAWSCredentialEnv drives the real rule chain the live
// hook builds. assume is rejected in every spelling, --exec included (operator
// ruling, Phillip, 2026-11-17: "yes reject assume --exec like assume."). The
// AWS credential-environment forms are PINNED at their verdict (approve, by
// safe-commands), which the operator ruled to keep (Phillip, 2026-11-17,
// answering "c": "Keep CETA approving them"; ADR 0129, decision 5). A change
// here needs a new operator ruling.
func TestLiveHook_AssumeAndAWSCredentialEnv(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		{"assume dev/example-role", hookio.Reject},
		{`assume dev/example-role --exec "bin/tool show x"`, hookio.Reject},
		{"assume dev/example-role --exec=ls", hookio.Reject},
		{`source assume dev/example-role --exec "bin/tool show x"`, hookio.Reject},
		{`. assume dev/example-role --exec "bin/tool show x"`, hookio.Reject},

		// Pinned current verdicts (not a ruling that they are right).
		{"AWS_PROFILE=x ls", hookio.Approve},
		{"export AWS_PROFILE=x", hookio.Approve},
		{"export AWS_SESSION_TOKEN=x", hookio.Approve},
		{"env -u AWS_SESSION_TOKEN AWS_PROFILE=x ls", hookio.Approve},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			if got := assumeChainVerdict(t, cwd, tc.command); got.Decision != tc.want {
				t.Errorf("verdict = %v (%s), want %v", got.Decision, got.Reason, tc.want)
			}
		})
	}
}
