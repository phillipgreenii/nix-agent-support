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
	cycleID, err := c.CreateProcessingCycle(ctx, prID, "foo/bar#8", false)
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

func TestCreateFeedback_TruncatesLongTitleButPreservesBody(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, err := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "foo/bar", PRNumber: 9})
	if err != nil {
		t.Fatalf("ensure MR: %v", err)
	}
	cycleID, err := c.CreateProcessingCycle(ctx, prID, "foo/bar#9", false)
	if err != nil {
		t.Fatalf("CreateProcessingCycle: %v", err)
	}

	// 575-char title (mirrors the actual probe-sync failure on PR #87397).
	longTitle := ""
	for range 575 {
		longTitle += "x"
	}
	// Body is independent — must round-trip in full.
	body := longTitle + "\nfollowed by a second line with actually useful detail"

	fbID, err := c.CreateFeedback(ctx, CreateFeedbackInput{
		ProcessingCycleID: cycleID,
		Kind:              FeedbackKindCommentThread,
		ExternalID:        "long-title",
		Fingerprint:       "long-title-fp",
		Title:             longTitle,
		Body:              body,
	})
	if err != nil {
		t.Fatalf("CreateFeedback returned error on long title: %v", err)
	}
	if fbID == "" {
		t.Fatalf("expected non-empty feedback ID")
	}

	// The stored title must satisfy bd's 500-char ceiling.
	got, err := c.GetFeedback(ctx, fbID)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if got == nil {
		t.Fatalf("GetFeedback returned nil")
	}
	if runeLen := len([]rune(got.Title)); runeLen > maxBdTitleLen {
		t.Errorf("stored title is %d runes, want ≤%d", runeLen, maxBdTitleLen)
	}

	// The full body must survive (bd show is the source of truth — list
	// JSON doesn't include body, so use bd show).
	c2, _ := c.Runner.Run(ctx, "show", fbID)
	if !contains(c2, "followed by a second line with actually useful detail") {
		t.Errorf("bd show output missing tail of body: %s", c2)
	}
}

func TestTruncateDescription(t *testing.T) {
	// At 64KB cap, a 70KB blob gets shortened to fit; the marker carries
	// a hint so the bead reader can tell content was dropped.
	bigBody := make([]byte, 70_000)
	for i := range bigBody {
		bigBody[i] = 'x'
	}
	got := truncateDescription(string(bigBody), maxBdDescriptionLen)
	if len(got) > maxBdDescriptionLen {
		t.Errorf("truncateDescription returned %d bytes, exceeding %d", len(got), maxBdDescriptionLen)
	}
	if !contains(got, "[truncated to fit bd description column]") {
		t.Errorf("truncated description missing marker tail: %q", got[len(got)-80:])
	}
	// Short bodies are left untouched.
	if got := truncateDescription("hello", maxBdDescriptionLen); got != "hello" {
		t.Errorf("short body modified: %q", got)
	}
}

func TestTruncateTitle(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"short stays unchanged", "hello", 500, "hello"},
		{"exact maxLen stays unchanged", "abcde", 5, "abcde"},
		// "…" is 3 bytes; budget = 5 - 3 = 2, so we keep "ab" + "…" = 5 bytes.
		{"longer than maxLen ends in ellipsis", "abcdef", 5, "ab…"},
		// 🎯 is 4 bytes; budget = 20 - 3 = 17 → four 🎯 (16 bytes) fit, fifth doesn't.
		{"unicode title respects byte budget", "🎯🎯🎯🎯🎯🎯", 20, "🎯🎯🎯🎯…"},
		// Snap to rune boundary when the byte cutoff splits a multi-byte rune.
		{"snaps to rune boundary", "a🎯b", 5, "a…"},
		{"empty title stays empty", "", 500, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateTitle(tc.in, tc.maxLen)
			if got != tc.want {
				t.Errorf("truncateTitle(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
			}
			if len(got) > tc.maxLen {
				t.Errorf("truncateTitle(%q, %d) returned %d bytes, exceeding limit", tc.in, tc.maxLen, len(got))
			}
		})
	}
}

// contains is a tiny strings.Contains stand-in to avoid pulling strings
// into the package's existing test imports.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestFindFeedbackByFingerprint_DedupAcrossRuns(t *testing.T) {
	ctx := context.Background()
	c, _ := newBDWorkspace(t)

	prID, _, _ := c.EnsureMergeRequest(ctx, "", MergeRequestFields{Repo: "x/y", PRNumber: 3})
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "x/y#3", false)
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
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "a/b#5", false)
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
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "g/h#4", false)
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
	} else if got.Fields.Kind != string(FeedbackKindCommentThread) {
		t.Fatalf("kind: got %q", got.Fields.Kind)
	} else if got.Fields.ExternalID != "PRRT_abc" {
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
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "r/d#1", false)
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
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "r/x#2", false)
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
	cycleID, _ := c.CreateProcessingCycle(ctx, prID, "r/p#9", false)

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
