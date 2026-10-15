package legacyextract

import (
	"reflect"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
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

// TestLegacyPnwfRowsGradeAgainstMarkerDirectory is the regression guard for
// pg2-82oo9: the legacy pnwf oracle rows that approve only from a trusted exec
// location MUST be extracted with workspace_markers and MUST agree with the new
// engine; and a marker directory must not flip any legacy not-approve/reject
// row to approve.
func TestLegacyPnwfRowsGradeAgainstMarkerDirectory(t *testing.T) {
	res, err := Run(repoRoot)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cmp, err := CompareAgainstNewEngine(res.Rows)
	if err != nil {
		t.Fatalf("CompareAgainstNewEngine: %v", err)
	}
	disagreed := map[string]Disagreement{}
	for _, d := range cmp.Disagreements {
		disagreed[d.Case] = d
	}

	const prefix = "legacy_internal_rules_pnwf_pnwf_test_"
	want := []string{
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row0", // pnwf sync-fetch
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row1", // pnwf sync-fetch --set
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row2", // pnwf update-relock
		prefix + "TestPnwf_SyncFetchUpdateRelock_Approve_row3", // pnwf update-relock --set
		prefix + "TestPnwf_Cleanup_NoForceFlag_Approve_row0",   // pnwf cleanup my-branch
	}
	byCase := map[string]goldencorpus.Row{}
	for _, r := range res.Rows {
		byCase[r.Case] = r
	}
	for _, c := range want {
		r, ok := byCase[c]
		if !ok {
			t.Errorf("case %q not extracted", c)
			continue
		}
		if r.ExpectedVerdict != goldencorpus.Approve {
			t.Errorf("case %q: legacy oracle verdict = %s, want approve", c, r.ExpectedVerdict)
		}
		if !reflect.DeepEqual(r.CWDPathState.WorkspaceMarkers, []string{"pn-workspace.toml"}) {
			t.Errorf("case %q: workspace_markers = %v, want [pn-workspace.toml]", c, r.CWDPathState.WorkspaceMarkers)
		}
		if d, bad := disagreed[c]; bad {
			t.Errorf("case %q: new engine disagrees with the legacy oracle (old=%s new=%s: %s)", c, d.OldVerdict, d.NewVerdict, d.NewReason)
		}
	}

	// Widening guard: the marker directory exists to restore the approvals the
	// legacy oracle expects, so it MUST NOT approve a marked row that the
	// same row does not approve without a marker directory (the synthetic cwd),
	// unless the legacy oracle itself approved it. Replay every marked row both ways.
	var marked, unmarked []goldencorpus.Row
	for _, r := range res.Rows {
		if len(r.CWDPathState.WorkspaceMarkers) == 0 {
			continue
		}
		marked = append(marked, r)
		r.CWDPathState.WorkspaceMarkers = nil
		unmarked = append(unmarked, r)
	}
	if len(marked) == 0 {
		t.Fatal("no row was extracted with workspace_markers")
	}
	approvesIn := func(rows []goldencorpus.Row) map[string]bool {
		c, err := CompareAgainstNewEngine(rows)
		if err != nil {
			t.Fatalf("CompareAgainstNewEngine: %v", err)
		}
		out := map[string]bool{}
		dis := map[string]Disagreement{}
		for _, d := range c.Disagreements {
			dis[d.Case] = d
		}
		for _, r := range rows {
			if d, bad := dis[r.Case]; bad {
				out[r.Case] = d.NewVerdict == goldencorpus.Approve
			} else {
				out[r.Case] = r.ExpectedVerdict == goldencorpus.Approve
			}
		}
		return out
	}
	withMarker, withoutMarker := approvesIn(marked), approvesIn(unmarked)
	for _, r := range marked {
		if r.ExpectedVerdict != goldencorpus.Approve && withMarker[r.Case] && !withoutMarker[r.Case] {
			t.Errorf("case %q: legacy %s, approves only because of the marker directory", r.Case, r.ExpectedVerdict)
		}
	}
}
