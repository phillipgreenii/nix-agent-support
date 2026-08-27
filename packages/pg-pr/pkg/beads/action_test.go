package beads

import (
	"context"
	"strings"
	"testing"
)

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
