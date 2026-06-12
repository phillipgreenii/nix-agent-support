package launch

import (
	"reflect"
	"testing"
)

func TestBuildNew(t *testing.T) {
	got := BuildNew(Spec{
		ClaudeBin: "claude", UUID: "u1", Name: "alpha", PluginDir: "/nix/plugin", Model: "opus",
	})
	want := []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/nix/plugin", "--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestBuildNew_omitsModelWhenEmpty(t *testing.T) {
	got := BuildNew(Spec{ClaudeBin: "claude", UUID: "u1", Name: "alpha", PluginDir: "/p"})
	want := []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestBuildResume_resumesByName_alwaysHasPluginDir(t *testing.T) {
	got := BuildResume(Spec{ClaudeBin: "claude", Name: "alpha", PluginDir: "/p"})
	want := []string{"claude", "--resume", "alpha", "--plugin-dir", "/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildResume = %v\nwant %v", got, want)
	}
}

// TestBuildLaunchFlags pins the claude launch-flag passthrough (N2). When set,
// the flags must appear in the contract order: --dangerously-skip-permissions,
// then --effort <value>, then --model <value>; when unset they are omitted.
// Without --dangerously-skip-permissions the dispatched worker stalls on the
// first tool prompt, so this is correctness, not polish.
func TestBuildLaunchFlags(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want []string
	}{
		{
			name: "new with all launch flags in contract order",
			spec: Spec{ClaudeBin: "claude", UUID: "u1", Name: "alpha", PluginDir: "/p", DangerouslySkipPermissions: true, Effort: "max", Model: "opus"},
			want: []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p", "--dangerously-skip-permissions", "--effort", "max", "--model", "opus"},
		},
		{
			name: "new with skip+effort but no model",
			spec: Spec{ClaudeBin: "claude", UUID: "u1", Name: "alpha", PluginDir: "/p", DangerouslySkipPermissions: true, Effort: "max"},
			want: []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p", "--dangerously-skip-permissions", "--effort", "max"},
		},
		{
			name: "new omits skip when false and effort when empty",
			spec: Spec{ClaudeBin: "claude", UUID: "u1", Name: "alpha", PluginDir: "/p", Model: "opus"},
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

func TestBuildResume_includesLaunchFlags(t *testing.T) {
	got := BuildResume(Spec{ClaudeBin: "claude", Name: "alpha", PluginDir: "/p", DangerouslySkipPermissions: true, Effort: "max", Model: "opus"})
	want := []string{"claude", "--resume", "alpha", "--plugin-dir", "/p", "--dangerously-skip-permissions", "--effort", "max", "--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildResume = %v\nwant %v", got, want)
	}
}
