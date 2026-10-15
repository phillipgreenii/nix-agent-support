package legacyextract

import (
	"path/filepath"
	"strings"
)

// workspaceMarkerFile is the marker effectpolicy.TrustedCheckoutExec accepts
// as a trusted exec location (a pn workspace root, or a workforest set
// directory under one).
const workspaceMarkerFile = "pn-workspace.toml"

// workspaceMarkersFor returns the corpus workspace_markers an extracted row
// needs. The legacy RuleChain approved pn/pnwf verbs from any directory, and
// the new engine approves them only when the working directory is at or under
// a pn-workspace.toml (every real use runs from a workforest set or the
// workspace root), so a Bash row whose first word is pn or pnwf is replayed
// from a real marker directory instead of the synthetic /repo cwd. Every
// other row keeps the synthetic cwd: the marker is a narrow, tool-keyed
// fixture, not a blanket trust grant for the legacy oracle.
func workspaceMarkersFor(toolName, command string) []string {
	if toolName != "Bash" {
		return nil
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil
	}
	switch filepath.Base(fields[0]) {
	case "pn", "pnwf":
		return []string{workspaceMarkerFile}
	}
	return nil
}
