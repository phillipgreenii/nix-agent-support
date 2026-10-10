// Package allow is the allow-side fixture of the G5 guard: nothing here may
// be reported. It is never compiled (testdata) and is scanned by
// TestG5GuardAllowSideStaysLegal.
package allow

import "os/exec"

// The words focus-item and dedup_key and exec bd appear only in this comment.
func legal() {
	_ = exec.Command("pg-desk", "issue", "refresh", "ABC-1")
	_ = exec.Command("pg-connector", "issue", "show", "ABC-1")
	_ = exec.Command("pg-connector", "issue", "list", "--query", "work-beads")
	_ = exec.Command("pg-connector", "issue", "deps", "ABC-1", "--full")
	_ = exec.Command("pg-connector", "issue", "children", "ABC-1")
	_ = []string{"issue", "refresh"}
	_ = []string{"focus", "focus_selected", "source_type", "source_id"}
	_ = []string{"create", "issue"} // the verb alone, not after "issue"
}
