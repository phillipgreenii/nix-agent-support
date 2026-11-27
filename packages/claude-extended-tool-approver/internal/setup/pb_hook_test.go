package setup

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestLiveHook_Pb drives the engine the live PreToolUse hook builds (the real
// rule chain): the pb command spec alone approves nothing live, so the chain
// must carry the pb rule.
func TestLiveHook_Pb(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		{"pb actor --role drain --scratchpad /tmp/x", hookio.Approve},
		{"pb drain isolate --bead X --repo /abs/repo --actor A", hookio.Approve},
		{"pb drain reclaim --bead X --repo /abs/repo --actor A", hookio.Approve},
		{"pb gate check --dry-run", hookio.Approve},
		{"pb gate check --stale-handler close", hookio.NoOpinion},
		{"pb completion bash", hookio.NoOpinion},
		{"pb frobnicate --bead X", hookio.NoOpinion},
		{"pb drain isolate --bead X --repo /r && pb gate check --strict", hookio.NoOpinion},
		{"pb drain isolate --bead $(id) --repo /abs/repo", hookio.NoOpinion},
	}
	for _, mode := range []string{"auto", "dontAsk"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.command, func(t *testing.T) {
				ti, _ := json.Marshal(map[string]string{"command": tc.command})
				in := &hookio.HookInput{
					SessionID: "s", CWD: cwd, ToolName: "Bash", ToolInput: ti,
					PermissionMode: mode, HookEventName: "PreToolUse", ToolUseID: "t",
					Unattended: true,
				}
				verdict, final := NewEngineForCWDLiveUnattended(cwd, nil).EvaluateHookBoth(in)
				if verdict.Decision != tc.want {
					t.Errorf("verdict = %v (%s), want %v", verdict.Decision, verdict.Reason, tc.want)
				}
				if tc.want == hookio.Approve && final.Decision != hookio.Approve {
					t.Errorf("delivered = %v, want approve", final.Decision)
				}
			})
		}
	}
}
