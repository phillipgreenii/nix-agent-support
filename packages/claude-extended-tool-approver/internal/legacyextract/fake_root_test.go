package legacyextract

import (
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

// TestStabilizeWorkspaceRoot pins the pg2-j6b0b normalisation: the pre-move
// workspace root is rewritten to the fixed no-zone fake root wherever it
// appears, and a string that does not name it is returned untouched.
func TestStabilizeWorkspaceRoot(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"find /Users/phillipg/phillipg_mbp -name x", "find /opt/ceta-corpus-no-zone -name x"},
		{"A=/Users/phillipg/phillipg_mbp pg-connector issue show x", "A=/opt/ceta-corpus-no-zone pg-connector issue show x"},
		{"/Users/phillipg/phillipg_mbp/sub /Users/phillipg/phillipg_mbp", "/opt/ceta-corpus-no-zone/sub /opt/ceta-corpus-no-zone"},
		{"git status", "git status"},
		{"", ""},
	} {
		if got := stabilizeWorkspaceRoot(tc.in); got != tc.want {
			t.Errorf("stabilizeWorkspaceRoot(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildRowNeverEmitsPreMoveRoot is the regression guard: whatever a legacy
// table quotes, an extracted row (command or cwd) never names the real-machine
// root, and the expected verdict is carried through unchanged.
func TestBuildRowNeverEmitsPreMoveRoot(t *testing.T) {
	cmd := "find /Users/phillipg/phillipg_mbp -name '*x*' -not -path '*/.git/*'"
	row := buildRow("internal/engine/x_test.go", "TestX", 0, "named glob", cmd, goldencorpus.NotApprove,
		hookInputMatch{toolName: "Bash", cwd: "/Users/phillipg/phillipg_mbp/sub"})
	got, _ := row.ToolInput.ToolInput["command"].(string)
	if strings.Contains(got, preMoveWorkspaceRoot) || strings.Contains(row.CWDPathState.CWD, preMoveWorkspaceRoot) {
		t.Fatalf("row still names %s: command=%q cwd=%q", preMoveWorkspaceRoot, got, row.CWDPathState.CWD)
	}
	if want := "find /opt/ceta-corpus-no-zone -name '*x*' -not -path '*/.git/*'"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	if row.CWDPathState.CWD != "/opt/ceta-corpus-no-zone/sub" {
		t.Errorf("cwd = %q, want it re-rooted", row.CWDPathState.CWD)
	}
	if row.ExpectedVerdict != goldencorpus.NotApprove {
		t.Errorf("expected verdict = %s, want %s unchanged", row.ExpectedVerdict, goldencorpus.NotApprove)
	}
}
