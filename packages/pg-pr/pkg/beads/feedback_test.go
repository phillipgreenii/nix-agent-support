package beads

import (
	"context"
	"testing"
)

func TestCreateFeedback_CreatesAndLinks(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 8})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, prID, "foo/bar#8")
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}
	fbID, err := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "IC_kw1",
		Fingerprint:       "abc123",
		AuthorRole:        AuthorRoleTeamMember,
		Title:             "Reviewer asked: add tests",
		Body:              "alice (TEAM): please add tests for the new path.",
	})
	if err != nil {
		t.Fatalf("CreateFeedback: %v", err)
	}
	if fbID == "" {
		t.Fatalf("expected non-empty feedback ID")
	}

	// Listing under the cycle returns the feedback bead.
	all, err := c.ListFeedback(ctx, cycleID, false)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 feedback under cycle, got %d", len(all))
	}
	if all[0].ID != fbID {
		t.Fatalf("expected feedback id %s, got %s", fbID, all[0].ID)
	}
	if all[0].Fields.Kind != "comment-thread" {
		t.Fatalf("kind metadata: %q", all[0].Fields.Kind)
	}
	if all[0].Fields.Fingerprint != "abc123" {
		t.Fatalf("fingerprint metadata: %q", all[0].Fields.Fingerprint)
	}
}

func TestFindFeedbackByFingerprint_DedupAcrossRuns(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "x/y", PRNumber: 3})
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "x/y#3")
	if _, err := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCIFailure,
		Fingerprint:       "fp-1",
		Title:             "CI failed: lint",
	}); err != nil {
		t.Fatalf("create feedback: %v", err)
	}

	hit, err := c.FindFeedbackByFingerprint(ctx, cycleID, "fp-1")
	if err != nil {
		t.Fatalf("FindFeedbackByFingerprint: %v", err)
	}
	if hit == nil {
		t.Fatalf("expected to find feedback for fp-1")
	}

	miss, err := c.FindFeedbackByFingerprint(ctx, cycleID, "fp-2")
	if err != nil {
		t.Fatalf("FindFeedbackByFingerprint (miss): %v", err)
	}
	if miss != nil {
		t.Fatalf("expected nil for fp-2, got %+v", miss)
	}
}

func TestMarkFeedbackResolvedUpstream(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "a/b", PRNumber: 5})
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "a/b#5")
	fbID, _ := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		Fingerprint:       "fp",
		Title:             "comment",
	})
	if err := c.MarkFeedbackResolvedUpstream(ctx, fbID); err != nil {
		t.Fatalf("MarkFeedbackResolvedUpstream: %v", err)
	}

	// After close: ListFeedback open-only should not return it.
	open, err := c.ListFeedback(ctx, cycleID, false)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	for _, fb := range open {
		if fb.ID == fbID {
			t.Fatalf("expected closed feedback %s to be excluded from open-only list", fbID)
		}
	}

	// With includeClosed=true it shows up.
	all, err := c.ListFeedback(ctx, cycleID, true)
	if err != nil {
		t.Fatalf("ListFeedback all: %v", err)
	}
	found := false
	for _, fb := range all {
		if fb.ID == fbID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected closed feedback %s in all-list", fbID)
	}
}

func TestCreateFeedback_Validates(t *testing.T) {
	ctx := context.Background()
	c := NewClientWithRunner(&fakeRunner{})
	if _, err := c.CreateFeedback(ctx, CreateFeedbackInput{}); err == nil {
		t.Fatalf("expected validation error on empty input")
	}
	if _, err := c.CreateFeedback(ctx, CreateFeedbackInput{ProcessingCycleID: "x"}); err == nil {
		t.Fatalf("expected validation error when kind/title missing")
	}
}

func TestGetFeedback_ReturnsBead(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "g/h", PRNumber: 4})
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "g/h#4")
	fbID, err := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "PRRT_abc",
		Fingerprint:       "fp-get",
		Title:             "test feedback",
	})
	if err != nil {
		t.Fatalf("create feedback: %v", err)
	}

	got, err := c.GetFeedback(ctx, fbID)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if got == nil {
		t.Fatalf("expected to find feedback %s", fbID)
	}
	if got.Fields.Kind != string(FeedbackKindCommentThread) {
		t.Fatalf("kind: got %q", got.Fields.Kind)
	}
	if got.Fields.ExternalID != "PRRT_abc" {
		t.Fatalf("external_id: got %q", got.Fields.ExternalID)
	}
}

func TestGetFeedback_NotFound(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	got, err := c.GetFeedback(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestGetFeedback_Validates(t *testing.T) {
	c := NewClientWithRunner(&fakeRunner{})
	if _, err := c.GetFeedback(context.Background(), ""); err == nil {
		t.Fatalf("expected error on empty id")
	}
}
