package beads

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptedRunner returns canned output keyed on the bd subcommand, so a test
// can inject a transient failure on the dependency query while the task list
// still returns an open process-feedback cycle.
type scriptedRunner struct {
	taskListJSON string // returned for `bd list ...`
	depListErr   error  // returned for `bd dep list ...` when non-nil
}

func (r *scriptedRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "dep" && args[1] == "list" {
		if r.depListErr != nil {
			return "", r.depListErr
		}
		return "[]", nil
	}
	if len(args) >= 1 && args[0] == "list" {
		return r.taskListJSON, nil
	}
	return "", nil
}

// TestFindOpenProcessingCycle_FailsSafeOnDepError pins the duplicate-cycle
// fix: when the dependency query errors (a transient bd/dolt failure), Find
// must return an error — NOT a silent (",", false, nil). A silent not-found
// makes the sync caller create a second cycle for a PR that already has one,
// which is exactly how 48 cycles accumulated for 27 PRs.
func TestFindOpenProcessingCycle_FailsSafeOnDepError(t *testing.T) {
	ctx := context.Background()
	r := &scriptedRunner{
		taskListJSON: `[{"id":"cyc-1","title":"process-feedback: foo/bar#7","status":"open","issue_type":"task"}]`,
		depListErr:   errors.New("dial tcp 127.0.0.1:24158: connect: connection refused"),
	}
	c := NewClientWithRunner(r)

	_, found, err := c.FindOpenProcessingCycle(ctx, "pr-1")
	if err == nil {
		t.Fatalf("expected an error when the dependency query fails (got found=%v, err=nil) — a silent not-found causes duplicate-cycle creation", found)
	}
}

func TestCreateProcessingCycle_CreatesAndLinks(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 7})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}

	cycleID, err := c.CreateProcessingCycle(ctx, prID, "foo/bar#7")
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	if cycleID == "" {
		t.Fatalf("expected non-empty cycle ID")
	}

	// Verify the open-cycle finder picks it up.
	id, found, err := c.FindOpenProcessingCycle(ctx, prID)
	if err != nil {
		t.Fatalf("FindOpenProcessingCycle: %v", err)
	}
	if !found {
		t.Fatalf("expected to find open cycle for %s", prID)
	}
	if id != cycleID {
		t.Fatalf("expected %s, got %s", cycleID, id)
	}
}

func TestFindOpenProcessingCycle_NoneOpen(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 9})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	id, found, err := c.FindOpenProcessingCycle(ctx, prID)
	if err != nil {
		t.Fatalf("FindOpenProcessingCycle: %v", err)
	}
	if found || id != "" {
		t.Fatalf("expected no open cycle, got id=%q found=%v", id, found)
	}
}

func TestFindOpenProcessingCycle_AfterClose(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 11})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, prID, "foo/bar#11")
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	if err := c.CloseProcessingCycle(ctx, cycleID, "done"); err != nil {
		t.Fatalf("CloseProcessingCycle: %v", err)
	}

	_, found, err := c.FindOpenProcessingCycle(ctx, prID)
	if err != nil {
		t.Fatalf("FindOpenProcessingCycle: %v", err)
	}
	if found {
		t.Fatalf("expected no open cycle after close")
	}
}

func TestExtractIDs(t *testing.T) {
	// Synthetic shape: a mix of "id":"foo" and other keys.
	in := `{"id":"beads_pg2-aaa","other":"x"}, {"id":"beads_pg2-bbb"}`
	got := extractIDs(in)
	want := []string{"beads_pg2-aaa", "beads_pg2-bbb"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestListChildrenOfPR(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 21})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, prID, "foo/bar#21")
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}

	children, err := c.ListChildrenOfPR(ctx, prID)
	if err != nil {
		t.Fatalf("ListChildrenOfPR: %v", err)
	}
	found := false
	for _, id := range children {
		if id == cycleID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cycle %s in children, got %v", cycleID, children)
	}
}

func TestProcessingCycleTitleConvention(t *testing.T) {
	// Sanity check: the public prefix is stable so the sync engine can
	// detect cycles deterministically.
	if !strings.HasPrefix(processingCycleTitlePrefix, "process-feedback:") {
		t.Fatalf("processing-cycle title prefix changed: %q", processingCycleTitlePrefix)
	}
}
