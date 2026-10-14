package conversation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// The tests of this file are copies of the old backend's Show tests
// (cmd/pg-connector-pr-github/internal/provider_test.go: TestBackend_Show_MapsGHDataToSchemaPR,
// TestBackend_Show_CarriesReviewThreadIDFlagsAndConnectionReports,
// TestBackend_Show_ReviewsCarryCommitOID), reduced to the conversation part of
// what Show builds and run against contentOf, the copy of its conversion.

func TestContentOf_NestsReviewCommentsAndKeepsTopLevelOnes(t *testing.T) {
	comments := &api.CommentsResult{Comments: []api.Comment{
		{ID: "c1", Author: "alice", Body: "top-level"},
		// ReviewID is a realistic GraphQL node-id string matching Review.ID below
		// exactly, proving the join key types agree.
		{ID: "c2", Author: "bob", Body: "inline", Path: "main.go", Line: 10, ThreadID: "t2", ReviewID: "PRR_kwDOKtdWE88AAAABL3blsA"},
	}}
	reviews := &api.ReviewsResult{Reviews: []api.Review{
		{ID: "PRR_kwDOKtdWE88AAAABL3blsA", Author: "bob", State: "CHANGES_REQUESTED", Body: "please fix", SubmittedAt: "2026-10-01T09:00:00Z"},
	}}

	got := contentOf(comments, reviews)
	if len(got.Comments) != 1 || got.Comments[0].ID != "c1" {
		t.Fatalf("top-level comments = %+v, want just c1", got.Comments)
	}
	if len(got.Reviews) != 1 || len(got.Reviews[0].Comments) != 1 || got.Reviews[0].Comments[0].ID != "c2" {
		t.Fatalf("review-nested comments mismatch: %+v", got.Reviews)
	}
	if got.Reviews[0].SubmittedAt != "2026-10-01T09:00:00Z" {
		t.Fatalf("Reviews[0].SubmittedAt = %q, want 2026-10-01T09:00:00Z", got.Reviews[0].SubmittedAt)
	}
	if got.Reviews[0].Body != "please fix" || got.Reviews[0].State != "CHANGES_REQUESTED" || got.Reviews[0].Author != "bob" {
		t.Errorf("review = %+v", got.Reviews[0])
	}
}

func TestContentOf_CarriesReviewThreadIDFlagsAndConnectionReports(t *testing.T) {
	comments := &api.CommentsResult{
		Comments: []api.Comment{
			{
				ID: "RC_root", Author: "bob", Body: "inline", Path: "main.go", Line: 3,
				ThreadID: "RC_root", ReviewThreadID: "PRRT_real", Resolved: true, ThreadIsOutdated: true,
				ReviewID: "PRR_a",
			},
			{ID: "IC_1", Author: "ci-bot[bot]", Body: "conversation"},
		},
		Threads:        api.ConnectionReport{Truncated: true, Total: 1001, Returned: 1000},
		CommentsReport: api.ConnectionReport{Total: 2, Returned: 2},
	}
	reviews := &api.ReviewsResult{
		Reviews: []api.Review{{ID: "PRR_a", Author: "bob", State: "PENDING", Body: "draft"}},
		Report:  api.ConnectionReport{Total: 1, Returned: 1},
	}

	got := contentOf(comments, reviews)
	if len(got.Reviews) != 1 || len(got.Reviews[0].Comments) != 1 {
		t.Fatalf("reviews = %+v, want the pending review carrying its inline comment", got.Reviews)
	}
	c := got.Reviews[0].Comments[0]
	if c.ReviewThreadID != "PRRT_real" || c.ThreadID != "RC_root" || !c.Resolved || !c.ThreadOutdated {
		t.Errorf("inline comment = %+v, want review_thread_id PRRT_real with thread_id unchanged and flags set", c)
	}
	if len(got.Comments) != 1 || got.Comments[0].ID != "IC_1" || got.Comments[0].ReviewThreadID != "" {
		t.Errorf("top-level comments = %+v, want just IC_1 with no thread id", got.Comments)
	}
	want := schema.PRConnections{
		Reviews:  schema.PRConnectionReport{Total: 1, Returned: 1},
		Threads:  schema.PRConnectionReport{Truncated: true, Total: 1001, Returned: 1000},
		Comments: schema.PRConnectionReport{Total: 2, Returned: 2},
	}
	if got.Connections != want {
		t.Errorf("Connections = %+v, want %+v", got.Connections, want)
	}
}

func TestContentOf_ReviewsCarryCommitOID(t *testing.T) {
	reviews := &api.ReviewsResult{Reviews: []api.Review{
		{ID: "PRR_1", Author: "bob", State: "APPROVED", CommitOID: "h1"},
		{ID: "PRR_2", Author: "carol", State: "COMMENTED", CommitOID: ""},
	}}

	got := contentOf(&api.CommentsResult{}, reviews)
	if len(got.Reviews) != 2 {
		t.Fatalf("reviews = %+v, want 2", got.Reviews)
	}
	if o := got.Reviews[0].CommitOID; o == nil || *o != "h1" {
		t.Errorf("review 0 commit_oid = %v, want h1", o)
	}
	if o := got.Reviews[1].CommitOID; o == nil || *o != "" {
		t.Errorf("review 1 commit_oid = %v, want a pointer to the empty string (commit gone), not absent", o)
	}
	raw, _ := json.Marshal(got.Reviews[1])
	if !strings.Contains(string(raw), `"commit_oid":""`) {
		t.Errorf("wire JSON %s must carry commit_oid as an empty string", raw)
	}
}

func TestContentOf_ACommentWithoutAReviewIDStaysTopLevel(t *testing.T) {
	// A thread comment GitHub left without its review id must not be dropped.
	comments := &api.CommentsResult{Comments: []api.Comment{{ID: "c1", Path: "a.go", Line: 2, ThreadID: "c1", ReviewThreadID: "PRRT_1"}}}
	got := contentOf(comments, &api.ReviewsResult{})
	if len(got.Comments) != 1 || got.Comments[0].ID != "c1" || got.Comments[0].Path != "a.go" {
		t.Errorf("comments = %+v, want c1 kept at the top level", got.Comments)
	}
}
