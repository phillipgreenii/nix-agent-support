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

// recordingRunner captures the argv of each bd call so a test can assert
// which flags CreateProcessingCycle passes to `bd create`.
type recordingRunner struct {
	createArgs []string
}

func (r *recordingRunner) Run(_ context.Context, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "create" {
		r.createArgs = args
		return "cyc-rec", nil // non-empty id so CreateProcessingCycle continues to dep-add
	}
	return "", nil // dep add and anything else
}

func TestCreateProcessingCycle_StampsMineWhenSelf(t *testing.T) {
	ctx := context.Background()
	r := &recordingRunner{}
	c := NewClientWithRunner(r)

	if _, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: "pr-1", Key: "foo/bar#7", Mine: true}); err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	joined := strings.Join(r.createArgs, " ")
	if !strings.Contains(joined, "-l mine") {
		t.Fatalf("self cycle: `bd create` args missing `-l mine`; got %q", joined)
	}
}

func TestCreateProcessingCycle_TeamCycleUnlabeled(t *testing.T) {
	ctx := context.Background()
	r := &recordingRunner{}
	c := NewClientWithRunner(r)

	if _, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: "pr-1", Key: "foo/bar#7", Mine: false}); err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	for _, a := range r.createArgs {
		if a == "mine" {
			t.Fatalf("team cycle must not be labeled mine; got args %v", r.createArgs)
		}
	}
}

func TestCreateProcessingCycle_CreatesAndLinks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 7})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}

	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "foo/bar#7", Mine: false})
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

// TestCreateProcessingCycle_WritesSupersedesEdgeForPredecessor is pg2-0waxt's
// remaining half: when the caller names a closed PredecessorID (the succession
// path in beadsbridge, after ResolveProcessingCycle reports a Closed
// predecessor), CreateProcessingCycle must record a `supersedes` edge from the
// new successor to it — the SAME discriminator the duplicate-audit's
// adjudication exclusion already reads (adjudication.go), so a genuine
// succession stops being counted as a duplicate. Direction is
// <successor> <predecessor>, matching `bd dep add`'s own documented semantics
// ("successor supersedes predecessor"), even though the audit's exclusion
// itself is direction-agnostic (adjudicatedIdentities' own doc comment).
func TestCreateProcessingCycle_WritesSupersedesEdgeForPredecessor(t *testing.T) {
	ctx := context.Background()
	r := &listRunner{}
	c := NewClientWithRunner(r)

	id, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{
		PRBeadID: "pr-1", Key: "foo/bar#7", PredecessorID: "cyc-old",
	})
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}

	var depAdds [][]string
	for _, call := range r.calls {
		if len(call) > 1 && call[0] == "dep" && call[1] == "add" {
			depAdds = append(depAdds, call)
		}
	}
	if len(depAdds) != 2 {
		t.Fatalf("expected 2 `dep add` calls (parent-child + supersedes), got %d: %v", len(depAdds), r.calls)
	}
	var supersedes []string
	for _, call := range depAdds {
		if containsStr(call, "--type=supersedes") {
			supersedes = call
		}
	}
	if supersedes == nil {
		t.Fatalf("no supersedes `dep add` call recorded: %v", r.calls)
	}
	joined := strings.Join(supersedes, " ")
	for _, want := range []string{"dep add " + id + " cyc-old", "--type=supersedes", "--no-cycle-check"} {
		if !strings.Contains(joined, want) {
			t.Errorf("supersedes dep-add args missing %q; got %q", want, joined)
		}
	}
}

// TestCreateProcessingCycle_NoPredecessorWritesNoSupersedesEdge guards the
// other side: an ordinary (non-succession) create — PredecessorID empty — must
// write only the parent-child edge, never a supersedes one.
func TestCreateProcessingCycle_NoPredecessorWritesNoSupersedesEdge(t *testing.T) {
	ctx := context.Background()
	r := &listRunner{}
	c := NewClientWithRunner(r)

	if _, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{
		PRBeadID: "pr-1", Key: "foo/bar#7",
	}); err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	for _, call := range r.calls {
		if len(call) > 1 && call[0] == "dep" && call[1] == "add" && containsStr(call, "--type=supersedes") {
			t.Fatalf("no PredecessorID was given, but a supersedes edge was written: %v", r.calls)
		}
	}
}

func TestFindOpenProcessingCycle_NoneOpen(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 11})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "foo/bar#11", Mine: false})
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
	t.Parallel()
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 21})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, CreateProcessingCycleInput{PRBeadID: prID, Key: "foo/bar#21", Mine: false})
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
