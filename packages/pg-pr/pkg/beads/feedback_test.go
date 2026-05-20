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

// ----------------------------------------------------------------------
// Reply pipeline helpers (B3)
// ----------------------------------------------------------------------

func TestSetReplyDraft_RoundTrip(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "r/d", PRNumber: 1})
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "r/d#1")
	fbID, err := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "TH_1",
		Fingerprint:       "fp-rd",
		Title:             "needs reply",
	})
	if err != nil {
		t.Fatalf("create feedback: %v", err)
	}

	// Initially empty.
	if got, err := c.GetReplyDraft(ctx, fbID); err != nil || got != "" {
		t.Fatalf("initial reply_draft: got %q err=%v, want empty", got, err)
	}

	body := "thanks, fixed in deadbee"
	if err := c.SetReplyDraft(ctx, fbID, body); err != nil {
		t.Fatalf("SetReplyDraft: %v", err)
	}
	got, err := c.GetReplyDraft(ctx, fbID)
	if err != nil {
		t.Fatalf("GetReplyDraft: %v", err)
	}
	if got != body {
		t.Fatalf("reply_draft round-trip: got %q want %q", got, body)
	}

	// Other fields preserved.
	fb, err := c.GetFeedback(ctx, fbID)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if fb == nil || fb.Fields.ExternalID != "TH_1" {
		t.Fatalf("other fields lost after SetReplyDraft: %+v", fb)
	}
}

func TestSetResponseID_RoundTrip(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "r/x", PRNumber: 2})
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "r/x#2")
	fbID, _ := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "TH_X",
		Fingerprint:       "fp-resp",
		Title:             "needs response id",
	})

	if got, err := c.GetResponseID(ctx, fbID); err != nil || got != "" {
		t.Fatalf("initial response_id: got %q err=%v, want empty", got, err)
	}

	if err := c.SetResponseID(ctx, fbID, "C_RESP_123"); err != nil {
		t.Fatalf("SetResponseID: %v", err)
	}
	got, err := c.GetResponseID(ctx, fbID)
	if err != nil {
		t.Fatalf("GetResponseID: %v", err)
	}
	if got != "C_RESP_123" {
		t.Fatalf("response_id: got %q want C_RESP_123", got)
	}
}

func TestListFeedbackPendingReply_FiltersCorrectly(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "r/p", PRNumber: 9})
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "r/p#9")

	// 1. Pending: has reply_draft, no response_id.
	pendingID, _ := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "TH_PEND",
		Fingerprint:       "fp-pend",
		Title:             "pending",
	})
	if err := c.SetReplyDraft(ctx, pendingID, "queued reply"); err != nil {
		t.Fatalf("set reply_draft on pending: %v", err)
	}

	// 2. Already-replied: has both reply_draft and response_id → excluded.
	repliedID, _ := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "TH_RPLD",
		Fingerprint:       "fp-rpld",
		Title:             "replied",
	})
	_ = c.SetReplyDraft(ctx, repliedID, "old reply")
	_ = c.SetResponseID(ctx, repliedID, "C_OLD")

	// 3. No draft → excluded.
	_, _ = c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "TH_PLAIN",
		Fingerprint:       "fp-plain",
		Title:             "no draft",
	})

	// 4. Closed feedback with reply_draft and no response_id → INCLUDED.
	closedID, _ := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "TH_CLOS",
		Fingerprint:       "fp-clos",
		Title:             "closed but draft",
	})
	_ = c.SetReplyDraft(ctx, closedID, "queued just before close")
	_ = c.CloseFeedback(ctx, closedID, "manual")

	pending, err := c.ListFeedbackPendingReply(ctx)
	if err != nil {
		t.Fatalf("ListFeedbackPendingReply: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending replies, got %d: %+v", len(pending), pending)
	}
	gotIDs := map[string]bool{}
	for _, fb := range pending {
		gotIDs[fb.ID] = true
	}
	if !gotIDs[pendingID] {
		t.Fatalf("expected %s in pending set", pendingID)
	}
	if !gotIDs[closedID] {
		t.Fatalf("expected closed-with-draft %s in pending set", closedID)
	}
	if gotIDs[repliedID] {
		t.Fatalf("expected %s (already replied) to be excluded", repliedID)
	}
}

func TestReplyHelpers_Validate(t *testing.T) {
	c := NewClientWithRunner(&fakeRunner{})
	ctx := context.Background()
	if err := c.SetReplyDraft(ctx, "", "x"); err == nil {
		t.Fatalf("SetReplyDraft: expected error on empty id")
	}
	if _, err := c.GetReplyDraft(ctx, ""); err == nil {
		t.Fatalf("GetReplyDraft: expected error on empty id")
	}
	if err := c.SetResponseID(ctx, "", "x"); err == nil {
		t.Fatalf("SetResponseID: expected error on empty id")
	}
	if _, err := c.GetResponseID(ctx, ""); err == nil {
		t.Fatalf("GetResponseID: expected error on empty id")
	}
}
