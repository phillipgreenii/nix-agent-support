package beads

import (
	"context"
	"strings"
	"testing"
)

// createChildBead creates a non-MR task bead and adds a dependency edge from
// the new bead to parentID. With the default `bd dep add` edge type
// ("blocks"), walking `--direction=up` from parentID reveals the child.
func createChildBead(t *testing.T, runner *CLIRunner, parentID, title string) string {
	t.Helper()
	out, err := runner.Run(context.Background(),
		"create", "--title", title, "--type", "task", "--priority", "2", "--silent")
	if err != nil {
		t.Fatalf("bd create: %v", err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		t.Fatalf("bd create returned empty id (stdout=%q)", out)
	}
	if _, err := runner.Run(context.Background(), "dep", "add", id, parentID); err != nil {
		t.Fatalf("bd dep add %s %s: %v", id, parentID, err)
	}
	return id
}

func addLabel(t *testing.T, runner *CLIRunner, id, label string) {
	t.Helper()
	if _, err := runner.Run(context.Background(), "label", "add", id, label); err != nil {
		t.Fatalf("bd label add: %v", err)
	}
}

func closeBead(t *testing.T, runner *CLIRunner, id string) {
	t.Helper()
	// --force bypasses bd's "blocked by open issues" guard, which would
	// otherwise refuse to close a bead whose parent (the merge-request) is
	// still open. The guard is fine for production but gets in the way of
	// these isolated unit tests, which only care about the status flip.
	if _, err := runner.Run(context.Background(), "close", id, "--force"); err != nil {
		t.Fatalf("bd close: %v", err)
	}
}

func TestDepTreeUp_Empty(t *testing.T) {
	c, _ := newBDWorkspace(t)
	ctx := context.Background()
	mr, _, err := c.EnsureMergeRequest(ctx, "MR-empty", MergeRequestFields{Repo: "x/y", PRNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.DepTreeUp(ctx, mr)
	if err != nil {
		t.Fatalf("DepTreeUp: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestDepTreeUp_WithChildren(t *testing.T) {
	c, runner := newBDWorkspace(t)
	ctx := context.Background()
	mr, _, err := c.EnsureMergeRequest(ctx, "MR-children", MergeRequestFields{Repo: "x/y", PRNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	a := createChildBead(t, runner, mr, "A")
	b := createChildBead(t, runner, mr, "B")
	cc := createChildBead(t, runner, mr, "C")
	addLabel(t, runner, b, "human")
	addLabel(t, runner, cc, "human")
	closeBead(t, runner, cc)

	got, err := c.DepTreeUp(ctx, mr)
	if err != nil {
		t.Fatalf("DepTreeUp: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3 — %+v", len(got), got)
	}

	// DepTreeUp no longer populates labels; overlay them via the workspace's
	// human-labeled set the same way production does.
	set, err := c.HumanLabeledBeads(ctx)
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	ApplyHumanLabels(got, set)

	byID := map[string]DepNode{}
	for _, n := range got {
		byID[n.ID] = n
	}
	if byID[a].Status == "closed" {
		t.Errorf("A should be open, got %+v", byID[a])
	}
	if !hasLabel(byID[b].Labels, "human") {
		t.Errorf("B should have human label after overlay, got %+v", byID[b].Labels)
	}
	if byID[cc].Status != "closed" {
		t.Errorf("C should be closed, got %q", byID[cc].Status)
	}

	// Root must not appear in the returned list.
	if _, ok := byID[mr]; ok {
		t.Errorf("root %s should not appear in DepTreeUp output", mr)
	}

	// AllNonClosedHumanLabeled: A is non-closed but not human-labeled → false.
	if AllNonClosedHumanLabeled(got) {
		t.Error("expected false (A is non-closed but unlabeled)")
	}

	// After labeling A as human, all non-closed deps carry the label → true.
	addLabel(t, runner, a, "human")
	got2, err := c.DepTreeUp(ctx, mr)
	if err != nil {
		t.Fatal(err)
	}
	set2, err := c.HumanLabeledBeads(ctx)
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	ApplyHumanLabels(got2, set2)
	if !AllNonClosedHumanLabeled(got2) {
		t.Errorf("expected true after labeling A; got deps=%+v", got2)
	}
}

func TestHumanLabeledBeads_EmptyWorkspace(t *testing.T) {
	c, _ := newBDWorkspace(t)
	set, err := c.HumanLabeledBeads(context.Background())
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	if len(set) != 0 {
		t.Errorf("want empty set, got %+v", set)
	}
}

func TestHumanLabeledBeads_OnlyHumanLabeled(t *testing.T) {
	c, runner := newBDWorkspace(t)
	ctx := context.Background()

	// Anchor: a merge-request bead lets us reuse createChildBead so each
	// child has a real parent edge (bd dep add requires both ids).
	mr, _, err := c.EnsureMergeRequest(ctx, "MR-anchor", MergeRequestFields{Repo: "x/y", PRNumber: 99})
	if err != nil {
		t.Fatal(err)
	}

	// Two human-labeled beads + one with a different label + one unlabeled.
	a := createChildBead(t, runner, mr, "A")
	b := createChildBead(t, runner, mr, "B")
	cc := createChildBead(t, runner, mr, "C")
	d := createChildBead(t, runner, mr, "D")
	addLabel(t, runner, a, "human")
	addLabel(t, runner, b, "human")
	addLabel(t, runner, cc, "needs-triage")

	set, err := c.HumanLabeledBeads(ctx)
	if err != nil {
		t.Fatalf("HumanLabeledBeads: %v", err)
	}
	if !set[a] {
		t.Errorf("expected %s in set", a)
	}
	if !set[b] {
		t.Errorf("expected %s in set", b)
	}
	if set[cc] {
		t.Errorf("did not expect %s in set (different label)", cc)
	}
	if set[d] {
		t.Errorf("did not expect %s in set (unlabeled)", d)
	}
}

func TestApplyHumanLabels(t *testing.T) {
	deps := []DepNode{
		{ID: "x-1", Status: "open"},
		{ID: "x-2", Status: "open", Labels: []string{"other"}},
		{ID: "x-3", Status: "closed"},
	}
	ApplyHumanLabels(deps, map[string]bool{"x-1": true, "x-2": true})
	if !hasLabel(deps[0].Labels, "human") {
		t.Errorf("x-1 should have human label, got %+v", deps[0].Labels)
	}
	if !hasLabel(deps[1].Labels, "human") {
		t.Errorf("x-2 should have human label, got %+v", deps[1].Labels)
	}
	if !hasLabel(deps[1].Labels, "other") {
		t.Errorf("x-2 should preserve existing label, got %+v", deps[1].Labels)
	}
	if hasLabel(deps[2].Labels, "human") {
		t.Errorf("x-3 not in set; should not have human label, got %+v", deps[2].Labels)
	}
}

func TestApplyHumanLabels_Idempotent(t *testing.T) {
	deps := []DepNode{{ID: "x-1", Status: "open", Labels: []string{"human"}}}
	ApplyHumanLabels(deps, map[string]bool{"x-1": true})
	count := 0
	for _, l := range deps[0].Labels {
		if l == "human" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("want exactly one human label, got %d (labels=%+v)", count, deps[0].Labels)
	}
}

func TestAllNonClosedHumanLabeled_EmptyNonClosed(t *testing.T) {
	deps := []DepNode{
		{ID: "x", Status: "closed", Labels: []string{}},
	}
	if AllNonClosedHumanLabeled(deps) {
		t.Error("expected false on empty non-closed set")
	}
}

func TestAllNonClosedHumanLabeled_NilInput(t *testing.T) {
	if AllNonClosedHumanLabeled(nil) {
		t.Error("expected false on nil input (no non-closed deps)")
	}
}

// cannedRunner is a scriptable Runner that records calls and returns a fixed
// stdout — for unit-testing bd-JSON parsing without a real bd workspace.
type cannedRunner struct {
	calls [][]string
	out   string
}

func (r *cannedRunner) Run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	return r.out, nil
}

func TestPRFeedbackInSubtree_FromDepTree(t *testing.T) {
	// Mimics `bd dep tree <pr> --direction=up --json`: a flat array whose
	// nodes share the bd list node shape (id/issue_type/status/metadata).
	fixture := `[
	  {"id":"pr-1","issue_type":"merge-request","status":"open","metadata":{"repo":"o/r"}},
	  {"id":"cyc-1","issue_type":"task","status":"open","metadata":null},
	  {"id":"fb-1","issue_type":"feedback","status":"open","metadata":{"fingerprint":"fp-aaa","kind":"comment-thread","external_id":"ext-1"}},
	  {"id":"fb-2","issue_type":"feedback","status":"closed","metadata":{"fingerprint":"fp-bbb"}},
	  {"id":"fb-3","issue_type":"feedback","status":"open","metadata":{}},
	  {"id":"act-1","issue_type":"task","status":"open","metadata":null}
	]`
	r := &cannedRunner{out: fixture}
	c := NewClientWithRunner(r)

	fbs, err := c.PRFeedbackInSubtree(context.Background(), "pr-1")
	if err != nil {
		t.Fatalf("PRFeedbackInSubtree: %v", err)
	}

	// All three feedback nodes returned (status-agnostic); non-feedback excluded.
	if len(fbs) != 3 {
		t.Fatalf("want 3 feedback nodes, got %d: %+v", len(fbs), fbs)
	}
	byID := map[string]Feedback{}
	for _, fb := range fbs {
		byID[fb.ID] = fb
	}
	if fb1, ok := byID["fb-1"]; !ok {
		t.Fatal("fb-1 missing")
	} else if fb1.Status != "open" || fb1.Fields.Fingerprint != "fp-aaa" || fb1.Fields.ExternalID != "ext-1" || fb1.Fields.Kind != "comment-thread" {
		t.Fatalf("fb-1 parsed wrong: %+v", fb1)
	}
	if fb2, ok := byID["fb-2"]; !ok || fb2.Status != "closed" || fb2.Fields.Fingerprint != "fp-bbb" {
		t.Fatalf("fb-2 parsed wrong: %+v / ok=%v", byID["fb-2"], ok)
	}
	if fb3, ok := byID["fb-3"]; !ok || fb3.Fields.Fingerprint != "" {
		t.Fatalf("fb-3 should have empty fingerprint: %+v", byID["fb-3"])
	}
	if len(r.calls) != 1 {
		t.Fatalf("want exactly 1 bd call, got %d: %v", len(r.calls), r.calls)
	}
	if got, want := strings.Join(r.calls[0], " "), "dep tree pr-1 --direction=up --json"; got != want {
		t.Fatalf("bd args: got %q want %q", got, want)
	}
}

func TestPRFeedbackInSubtree_EmptyID(t *testing.T) {
	c := NewClientWithRunner(&cannedRunner{})
	if _, err := c.PRFeedbackInSubtree(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty pr bead id")
	}
}
