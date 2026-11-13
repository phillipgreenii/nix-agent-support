package legacyextract

import (
	"reflect"
	"testing"
)

// TestWorkspaceMarkersFor pins which extracted rows are run from a real
// pn-workspace.toml directory: only a Bash command whose FIRST word is pn or
// pnwf (the tools that run workspace code and are meant to be used from a
// workforest set directory). Everything else keeps the synthetic cwd.
func TestWorkspaceMarkersFor(t *testing.T) {
	marker := []string{"pn-workspace.toml"}
	for _, tc := range []struct {
		tool, cmd string
		want      []string
	}{
		{"Bash", "pnwf sync-fetch", marker},
		{"Bash", "pnwf cleanup my-branch --set", marker},
		{"Bash", "pn workspace build", marker},
		{"Bash", "  pnwf resolve", marker},
		{"Bash", "go test ./...", nil},
		{"Bash", "git status", nil},
		{"Bash", "echo pnwf", nil},
		{"Bash", "cd /x && pnwf resolve --set", nil},
		{"Bash", "pnwfx foo", nil},
		{"Bash", "", nil},
		{"Write", "pnwf resolve", nil},
	} {
		if got := workspaceMarkersFor(tc.tool, tc.cmd); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("workspaceMarkersFor(%q, %q) = %v, want %v", tc.tool, tc.cmd, got, tc.want)
		}
	}
}
