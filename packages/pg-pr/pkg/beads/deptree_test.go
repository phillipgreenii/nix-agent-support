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
	if _, err := runner.Run(context.Background(), "close", id); err != nil {
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
	byID := map[string]DepNode{}
	for _, n := range got {
		byID[n.ID] = n
	}
	if byID[a].Status == "closed" {
		t.Errorf("A should be open, got %+v", byID[a])
	}
	if !hasLabel(byID[b].Labels, "human") {
		t.Errorf("B should have human label, got %+v", byID[b].Labels)
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
	if !AllNonClosedHumanLabeled(got2) {
		t.Errorf("expected true after labeling A; got deps=%+v", got2)
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
