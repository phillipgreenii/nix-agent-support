package effectpolicy

import (
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
)

// TestBenchCorpusHonorsWorkspaceMarkers guards pg2-82oo9: the requests
// BenchmarkEvaluate times are the SAME requests `evaluate --corpus` grades, so
// a corpus row that names workspace_markers must reach the engine from a real
// marker directory. Before the fix the benchmark replayed such a row from the
// synthetic /repo cwd, where its Exec fact can never approve, and so timed the
// abstain path instead of the approve path of every pn/pnwf row.
func TestBenchCorpusHonorsWorkspaceMarkers(t *testing.T) {
	reg := cmddesc.DefaultRegistry()
	policies := DefaultPolicies()
	graphPolicies := DefaultGraphPolicies()

	var approve, notApprove int
	for _, r := range loadBashCorpusRows(t) {
		if !r.marked {
			continue
		}
		got := Evaluate(r.req, reg, policies, graphPolicies).Decision
		switch r.expected {
		case goldencorpus.Approve:
			approve++
			if got != evalcontract.Approve {
				t.Errorf("case %q (marker row, expected approve): got %s from cwd %q", r.name, got, r.req.CWD)
			}
		default:
			notApprove++
			if got == evalcontract.Approve {
				t.Errorf("case %q (marker row, expected %s): got approve", r.name, r.expected)
			}
		}
	}
	if approve == 0 || notApprove == 0 {
		t.Fatalf("corpus has %d marker approve rows and %d marker not-approve rows; want both (the guard would be vacuous)", approve, notApprove)
	}
}
