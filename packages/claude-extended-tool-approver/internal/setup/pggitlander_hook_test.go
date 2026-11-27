package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestLiveHook_PgGitLanderStart drives the engine the live PreToolUse hook
// builds (the real rule chain): starting a land or rebase in the documented
// shape is approved bare and through bgrun (judged by the wrapped payload), and
// everything outside that shape abstains.
func TestLiveHook_PgGitLanderStart(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	land := "pg-git-lander land drain/x --repo " + cwd + " --source-worktree " + cwd + "/wt --wait 20m --json"
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		{land, hookio.Approve},
		{"bgrun pgl-land-x-y -- " + land, hookio.Approve},
		{"pg-git-lander rebase drain/x --onto main --repo " + cwd + " --source-worktree " + cwd + "/wt --json", hookio.Approve},
		{land + " --no-verify", hookio.NoOpinion},
		{"bgrun pgl-land-x-y -- " + land + " --no-verify", hookio.NoOpinion},
		{"pg-git-lander land drain/x --repo rel --source-worktree " + cwd + "/wt", hookio.NoOpinion},
		{land + " --frobnicate", hookio.NoOpinion},
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
