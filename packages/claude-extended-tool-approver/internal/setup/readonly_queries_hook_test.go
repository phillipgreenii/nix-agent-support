package setup

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestLiveHook_ReadOnlyRecorderAndPaMonitorQueries drives the same engine
// constructor and entry facade the live PreToolUse hook uses (main.go:
// NewEngineForCWDLiveUnattended(...).EvaluateHookBoth) for a dontAsk session
// whose cwd is not a git repository. The cmddesc schemas for these forms only
// reached the effect engine, so the live hook abstained (and a dontAsk session
// then denied the call) while the effect-engine unit test passed.
func TestLiveHook_ReadOnlyRecorderAndPaMonitorQueries(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir() // not a git repository
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		{"pg-claude-code-recorder status", hookio.Approve},
		{"pg-claude-code-recorder status --json", hookio.Approve},
		{"pg-claude-code-recorder status --short", hookio.Approve},
		{"pa-monitor sessions", hookio.Approve},
		{"pa-monitor sessions --json", hookio.Approve},
		{"pa-monitor sessions --json --since 2h", hookio.Approve},
		{"pa-monitor search --json recorder", hookio.Approve},
		{"pa-monitor search --json --substring recorder", hookio.Approve},
		{"pa-monitor search --json --substring --since 1d recorder", hookio.Approve},
		// Outside the approved surface: never approved.
		{"pg-claude-control stop", hookio.NoOpinion},
		{"pg-claude-code-recorder status --watch", hookio.NoOpinion},
		{"pg-claude-code-recorder status --socket /tmp/x.sock", hookio.NoOpinion},
		{"pg-claude-code-recorder serve", hookio.NoOpinion},
		{"pg-claude-code-recorder remove", hookio.NoOpinion},
		{"pg-claude-code-recorder status extra", hookio.NoOpinion},
		{"pa-monitor search --json", hookio.NoOpinion},
		{"pa-monitor search --json a b", hookio.NoOpinion},
		{"pa-monitor sessions --since", hookio.NoOpinion},
		{"pa-monitor sessions --since=2h", hookio.NoOpinion},
		{"pa-monitor sessions --since $X", hookio.NoOpinion},
		{"pa-monitor nudge", hookio.NoOpinion},
		{"pa-monitor daemon", hookio.NoOpinion},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			ti, _ := json.Marshal(map[string]string{"command": tc.command})
			in := &hookio.HookInput{
				SessionID: "s", CWD: cwd, ToolName: "Bash", ToolInput: ti,
				PermissionMode: "dontAsk", HookEventName: "PreToolUse", ToolUseID: "t",
				Unattended: true,
			}
			verdict, final := NewEngineForCWDLiveUnattended(cwd, nil).EvaluateHookBoth(in)
			if verdict.Decision != tc.want {
				t.Errorf("verdict = %v (%s), want %v", verdict.Decision, verdict.Reason, tc.want)
			}
			if tc.want == hookio.Approve && final.Decision != hookio.Approve {
				t.Errorf("delivered = %v, want approve", final.Decision)
			}
			if tc.want != hookio.Approve && final.Decision == hookio.Approve {
				t.Errorf("delivered approve for %q", tc.command)
			}
		})
	}
}
