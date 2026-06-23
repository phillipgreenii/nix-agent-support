package launch

import (
	"reflect"
	"testing"
)

func TestBuildNew(t *testing.T) {
	got := BuildNew(Spec{
		ClaudeBin: "claude", ClaudeSessionID: "u1", Name: "alpha", PluginDir: "/nix/plugin", Model: "opus",
	})
	want := []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/nix/plugin", "--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestBuildNew_omitsModelWhenEmpty(t *testing.T) {
	got := BuildNew(Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", Name: "alpha", PluginDir: "/p"})
	want := []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestBuildNew_omitsNameWhenEmpty(t *testing.T) {
	got := BuildNew(Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", PluginDir: "/p"})
	want := []string{"claude", "--session-id", "u1", "--plugin-dir", "/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

// TestBuildResume_resumesByClaudeSessionID pins the ADR 0015 resume contract:
// resume by --resume <claude_session_id> (exact, never opens the picker), with
// --plugin-dir always present.
func TestBuildResume_resumesByClaudeSessionID(t *testing.T) {
	got := BuildResume(Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", PluginDir: "/p"})
	want := []string{"claude", "--resume", "u1", "--plugin-dir", "/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildResume = %v\nwant %v", got, want)
	}
}

// TestBuildLaunchFlags pins the claude launch-flag passthrough (N2). When set,
// the flags must appear in the contract order: --permission-mode <value>,
// then --effort <value>, then --model <value>; when unset they are omitted.
// Without a permission mode that bypasses prompts (bypassPermissions) the
// dispatched worker stalls on the first tool prompt, so this is correctness,
// not polish.
func TestBuildLaunchFlags(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want []string
	}{
		{
			name: "new with all launch flags in contract order",
			spec: Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", Name: "alpha", PluginDir: "/p", PermissionMode: ModeBypassPermissions, Effort: "max", Model: "opus"},
			want: []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p", "--permission-mode", "bypassPermissions", "--effort", "max", "--model", "opus"},
		},
		{
			name: "new with mode+effort but no model",
			spec: Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", Name: "alpha", PluginDir: "/p", PermissionMode: ModePlan, Effort: "max"},
			want: []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p", "--permission-mode", "plan", "--effort", "max"},
		},
		{
			name: "new omits permission-mode when empty and effort when empty",
			spec: Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", Name: "alpha", PluginDir: "/p", Model: "opus"},
			want: []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p", "--model", "opus"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BuildNew(tt.spec); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("BuildNew = %v\nwant %v", got, tt.want)
			}
		})
	}
}

// TestBuildNew_emitsEachPermissionMode pins that every valid mode is emitted
// verbatim as its --permission-mode <value> argument.
func TestBuildNew_emitsEachPermissionMode(t *testing.T) {
	for _, mode := range ValidPermissionModes() {
		t.Run(string(mode), func(t *testing.T) {
			got := BuildNew(Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", Name: "alpha", PluginDir: "/p", PermissionMode: mode})
			want := []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p", "--permission-mode", string(mode)}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("BuildNew(%q) = %v\nwant %v", mode, got, want)
			}
		})
	}
}

func TestBuildResume_includesLaunchFlags(t *testing.T) {
	got := BuildResume(Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", PluginDir: "/p", PermissionMode: ModeBypassPermissions, Effort: "max", Model: "opus"})
	want := []string{"claude", "--resume", "u1", "--plugin-dir", "/p", "--permission-mode", "bypassPermissions", "--effort", "max", "--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildResume = %v\nwant %v", got, want)
	}
}

func TestBuildNew_emitsAllowedToolsAfterPermissionMode(t *testing.T) {
	got := BuildNew(Spec{
		ClaudeBin: "claude", ClaudeSessionID: "u1", PluginDir: "/p",
		PermissionMode: ModeDontAsk, AllowedTools: "Bash(git *),Edit", Effort: "max",
	})
	want := []string{
		"claude", "--session-id", "u1", "--plugin-dir", "/p",
		"--permission-mode", "dontAsk", "--allowed-tools", "Bash(git *),Edit", "--effort", "max",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestAppendFlags_omitsAllowedToolsWhenEmpty(t *testing.T) {
	got := BuildResume(Spec{ClaudeBin: "claude", ClaudeSessionID: "u1", PluginDir: "/p", PermissionMode: ModeDontAsk})
	want := []string{"claude", "--resume", "u1", "--plugin-dir", "/p", "--permission-mode", "dontAsk"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildResume = %v\nwant %v", got, want)
	}
}

func TestPermissionMode_Valid(t *testing.T) {
	for _, mode := range ValidPermissionModes() {
		if !mode.Valid() {
			t.Errorf("%q should be a valid permission mode", mode)
		}
	}
	if PermissionMode("").Valid() {
		t.Error("empty permission mode should not be reported valid")
	}
	if PermissionMode("nope").Valid() {
		t.Error("unknown permission mode should not be reported valid")
	}
}
