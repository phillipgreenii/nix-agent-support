package beads

import (
	"context"
	"strings"
	"testing"
)

func TestCreateAction_CreatesAndLinks(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 12})

	actID, err := c.CreateAction(ctx, CreateActionInput{
		MergeRequestID: prID,
		Kind:           ActionKindApplySuggestion,
		BdType:         "task",
		Title:          "Apply reviewer suggestion: rename X",
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if actID == "" {
		t.Fatalf("expected non-empty action ID")
	}

	// Verify the action is a child of the merge-request bead.
	children, err := c.ListChildrenOfPR(ctx, prID)
	if err != nil {
		t.Fatalf("ListChildrenOfPR: %v", err)
	}
	found := false
	for _, id := range children {
		if id == actID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected action %s under PR %s, got %v", actID, prID, children)
	}
}

func TestCreateAction_RejectsBadBdType(t *testing.T) {
	ctx := context.Background()
	c := NewClientWithRunner(&fakeRunner{})
	_, err := c.CreateAction(ctx, CreateActionInput{
		MergeRequestID: "x",
		Kind:           ActionKindFixCI,
		BdType:         "epic",
		Title:          "x",
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected bd type rejection, got %v", err)
	}
}

func TestCreateAction_DefaultsBdTypeToTask(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)
	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "a/b", PRNumber: 1})

	actID, err := c.CreateAction(ctx, CreateActionInput{
		MergeRequestID: prID,
		Kind:           ActionKindFixCI,
		Title:          "fix CI",
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	if actID == "" {
		t.Fatalf("expected non-empty action id")
	}
}

func TestCreateAction_ValidatesInput(t *testing.T) {
	ctx := context.Background()
	c := NewClientWithRunner(&fakeRunner{})
	cases := []CreateActionInput{
		{},
		{MergeRequestID: "x"},
		{MergeRequestID: "x", Kind: ActionKindFixCI},
	}
	for i, in := range cases {
		if _, err := c.CreateAction(ctx, in); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}
