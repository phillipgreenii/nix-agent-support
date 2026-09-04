package schema

import (
	"encoding/json"
	"testing"
)

func TestPR_JSONRoundTrip(t *testing.T) {
	in := PR{
		ID:       "pr-1",
		Repo:     "owner/repo",
		Number:   42,
		Title:    "Add feature",
		State:    "open",
		Branch:   "feature",
		Base:     "main",
		Author:   "octocat",
		URL:      "https://example.invalid/owner/repo/pull/42",
		Draft:    false,
		Merged:   false,
		Category: "focus",
		Comments: []PRComment{
			{ID: "c1", Author: "octocat", Body: "looks good", Resolved: false, Disposition: DispositionOpen},
		},
		Reviews: []PRReview{
			{
				ID:     "r1",
				Author: "reviewer",
				State:  "CHANGES_REQUESTED",
				Comments: []PRComment{
					{ID: "c2", Author: "reviewer", Body: "fix this", ThreadID: "t1", Disposition: DispositionWillFix},
				},
			},
		},
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out PR
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.ID != in.ID || out.Category != in.Category {
		t.Fatalf("round-trip mismatch: got %+v", out)
	}
	if len(out.Comments) != 1 || out.Comments[0].ID != "c1" || out.Comments[0].Disposition != DispositionOpen {
		t.Fatalf("comments round-trip mismatch: got %+v", out.Comments)
	}
	if len(out.Reviews) != 1 || len(out.Reviews[0].Comments) != 1 || out.Reviews[0].Comments[0].ID != "c2" || out.Reviews[0].Comments[0].Disposition != DispositionWillFix {
		t.Fatalf("review-thread comments round-trip mismatch: got %+v", out.Reviews)
	}
}

func TestPR_CommentIDAndCommentIDAreStrings(t *testing.T) {
	// PR.ID and PRComment.ID (used as feedback_set's comment_id) must be
	// strings, carried over as-is from pg-pr's api.Comment.ID string field
	// [design: §9, §5.3] — a compile-time assertion that these fields are
	// string-typed, not numeric.
	var _ string = PR{}.ID
	var _ string = PRComment{}.ID
	var _ string = FeedbackSetResult{}.CommentID
}

func TestDisposition_IsValid(t *testing.T) {
	for _, d := range ValidDispositions {
		if !d.IsValid() {
			t.Errorf("%q should be valid", d)
		}
	}
	if Disposition("bogus").IsValid() {
		t.Error(`"bogus" should not be valid`)
	}
	if Disposition("").IsValid() {
		t.Error(`"" should not be valid`)
	}
}

func TestCategorizeResult_JSONShape(t *testing.T) {
	raw, err := json.Marshal(CategorizeResult{ID: "pr-1", Category: "focus"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"pr-1","category":"focus"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestFeedbackSetResult_JSONShape(t *testing.T) {
	raw, err := json.Marshal(FeedbackSetResult{ID: "pr-1", CommentID: "c1", Disposition: DispositionWontFix})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"pr-1","comment_id":"c1","disposition":"wont-fix"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}
