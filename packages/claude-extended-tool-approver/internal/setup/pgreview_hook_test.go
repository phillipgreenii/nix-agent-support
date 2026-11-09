package setup

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestLiveHook_PgReviewAgentQ drives the engine the live PreToolUse hook builds
// (NewEngineForCWDLiveUnattended(...).EvaluateHookBoth, the real rule chain, no
// mock) for the exact command string `pg-review job doctor` submits to the
// approver, from a dontAsk session whose cwd is not a git repository (the
// doctor's own throwaway directory). The pg-review command spec alone reached
// only the effect engine, so the live hook abstained and the doctor's hook
// check failed with "no decision".
func TestLiveHook_PgReviewAgentQ(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		// Keep in step with doctor.HookSample in packages/pg-review.
		{"pg-review agent q ccaudit error-rate-by-tool", hookio.Approve},
		{"pg-review agent q ccaudit top-signatures --tool Bash --limit 20", hookio.Approve},
		{"pg-review agent q ccaudit q1 'a;b'", hookio.NoOpinion},
		{"pg-review agent q ccaudit q1 $(id)", hookio.NoOpinion},
		{"pg-review agent file findings.json", hookio.NoOpinion},
		{"pg-review job run --lens x", hookio.NoOpinion},
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
		})
	}
}
