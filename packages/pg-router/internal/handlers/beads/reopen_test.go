package beads

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestReopenReview_SetsStatusOpenAndRefreshesMetadata: ReopenReview re-opens a
// closed review-pr bead with a SINGLE bd update that (1) flips status to open,
// (2) overwrites the head_sha + branch metadata so the worker reviews the NEW
// commit (it checks out metadata.head_sha; reopening without refreshing would
// re-review the same old commit forever), and (3) clears the assignee so a fresh
// worker can claim it.
func TestReopenReview_SetsStatusOpenAndRefreshesMetadata(t *testing.T) {
	f := &fakeRunner{}
	if err := ReopenReview(context.Background(), f, "zr-rv7", "newsha", "feat/x", "mine"); err != nil {
		t.Fatalf("ReopenReview: %v", err)
	}
	if len(f.args) != 1 {
		t.Fatalf("expected exactly one bd call, got %d: %v", len(f.args), f.args)
	}
	got := strings.Join(f.args[0], " ")
	want := "update zr-rv7 --status=open --set-metadata head_sha=newsha --set-metadata branch=feat/x --set-metadata ownership=mine --assignee="
	if got != want {
		t.Errorf("ReopenReview args:\n got=%q\nwant=%q", got, want)
	}
}

// TestReopenReview_ErrorPropagates: a runner error is wrapped and names the bead id.
func TestReopenReview_ErrorPropagates(t *testing.T) {
	f := &fakeRunner{err: errors.New("boom")}
	err := ReopenReview(context.Background(), f, "zr-rv7", "newsha", "feat/x", "team")
	if err == nil {
		t.Fatal("expected the runner error to propagate")
	}
	if !strings.Contains(err.Error(), "zr-rv7") {
		t.Errorf("error should name the bead id, got %v", err)
	}
}
