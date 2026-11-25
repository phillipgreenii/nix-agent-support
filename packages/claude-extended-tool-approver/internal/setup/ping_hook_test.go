package setup

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestLiveHook_Ping drives the engine the live PreToolUse hook builds (the real
// rule chain) with the cases of effectpolicy.TestPingBoundedProbe: the spec
// alone approves nothing live, so the chain must carry the ping rule.
func TestLiveHook_Ping(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		{"ping -c 2 -W 2 192.168.40.28", hookio.Approve},
		{"ping -c 3 -W 2 pve3", hookio.Approve},
		{"ping -w 5 -n example.test", hookio.Approve},
		{"ping pve3 -c 1", hookio.Approve},
		{"ping -6 -c 1 fe80::1", hookio.Approve},
		{"ping pve3", hookio.NoOpinion},
		{"ping -W 2 pve3", hookio.NoOpinion},
		{"ping -c 0 pve3", hookio.NoOpinion},
		{"ping -c 100000 pve3", hookio.NoOpinion},
		{"ping -f -c 5 pve3", hookio.NoOpinion},
		{"ping -l 100 -c 5 pve3", hookio.NoOpinion},
		{"ping -i 0.001 -c 5 pve3", hookio.NoOpinion},
		{"ping -c 1 $(cat /etc/hostname).example.test", hookio.NoOpinion},
		{"ping -c 1 \"$H\"", hookio.NoOpinion},
		{"ping -c 1 http://x/y", hookio.NoOpinion},
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
