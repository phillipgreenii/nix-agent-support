package setup

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestLiveHook_PgGwm drives the engine the live PreToolUse hook builds
// (NewEngineForCWDLiveUnattended(...).EvaluateHookBoth, the real rule chain)
// from an auto-mode, unattended session, the shape every live pg-gwm call had.
// The golden corpus only covers mode default, attended.
func TestLiveHook_PgGwm(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	cases := []struct {
		command string
		want    hookio.Decision
	}{
		{"pg-gwm list", hookio.Approve},
		{"pg-gwm list --include-unmanaged", hookio.Approve},
		{"pg-gwm config check", hookio.Approve},
		{"pg-gwm info --name x", hookio.Approve},
		{"pg-gwm preserved list", hookio.Approve},
		{"pg-gwm reap --dry-run", hookio.Approve},
		{"pg-gwm version", hookio.Approve},
		{"pg-gwm reap", hookio.NoOpinion},
		{"pg-gwm preserved prune", hookio.NoOpinion},
		{"pg-gwm serve", hookio.NoOpinion},
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

// Raw worktree creation stays denied (the deny rule precedes the read-only
// approval, and nothing here approves it).
func TestLiveHook_PgGwmRawWorktreeNotApproved(t *testing.T) {
	withXDGConfig(t, "")
	cwd := t.TempDir()
	ti, _ := json.Marshal(map[string]string{"command": "git worktree add ../x"})
	in := &hookio.HookInput{
		SessionID: "s", CWD: cwd, ToolName: "Bash", ToolInput: ti,
		PermissionMode: "auto", HookEventName: "PreToolUse", ToolUseID: "t", Unattended: true,
	}
	verdict, final := NewEngineForCWDLiveUnattended(cwd, nil).EvaluateHookBoth(in)
	if verdict.Decision == hookio.Approve || final.Decision == hookio.Approve {
		t.Errorf("raw worktree add approved: %v / %v", verdict.Decision, final.Decision)
	}
}
