package prread

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/conversation"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/detail"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

// annotations are the keys the daemon adds to a show answer; the golden answer
// (the old backend's show for the same data) has none of them, and as_of and
// stale are the old backend's own call-completion time and constant false.
var annotations = []string{"as_of", "stale", "served_from", "age_seconds", "groups"}

func withoutAnnotations(m map[string]any) map[string]any {
	for _, k := range annotations {
		delete(m, k)
	}
	return m
}

// TestShowEqualsTheOldBackendsAnswer: the golden answer was produced once by
// running the old backend's Show (cmd/pg-connector-pr-github/internal) in a
// throwaway test over the same canned data (generic names), as_of and stale
// blanked; the daemon's show over the equivalent stored groups equals it apart
// from the added annotations.
func TestShowEqualsTheOldBackendsAnswer(t *testing.T) {
	raw, err := os.ReadFile("testdata/show.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{res: showResult(t)}
	got, err := newHandlers(eng)["show"](context.Background(), args("octo/widgets#42", false))
	if err != nil {
		t.Fatal(err)
	}
	gotMap := asJSON(t, got)
	for _, k := range []string{"served_from", "age_seconds", "groups", "as_of"} {
		if _, ok := gotMap[k]; !ok {
			t.Errorf("the answer lacks the annotation %q", k)
		}
	}
	if !reflect.DeepEqual(withoutAnnotations(gotMap), withoutAnnotations(want)) {
		g, _ := json.MarshalIndent(withoutAnnotations(gotMap), "", " ")
		w, _ := json.MarshalIndent(withoutAnnotations(want), "", " ")
		t.Fatalf("show differs from the old backend's answer:\ngot  %s\nwant %s", g, w)
	}
}

// The tests below are copies of the old backend's toSchemaPR tests
// (provider_test.go: TestBackend_Show_MapsGHDataToSchemaPR,
// TestBackend_Show_CarriesReviewThreadIDFlagsAndConnectionReports,
// TestBackend_Show_CommentCountCountsOnlyTopLevelComments,
// TestBackend_Show_CommentCountZeroWhenNoCommentsFetched,
// TestPRShowCarriesBaseSHA, TestBackend_Show_ReviewsCarryCommitOID), run against
// ToSchemaPR over equivalent stored groups.

func assemble(sum summary.Summary, d *detail.Detail, c *conversation.Content) *schema.PR {
	return ToSchemaPR(sum.Ref(), sum, d, c, t0)
}

func TestToSchemaPR_NestsReviewCommentsAndKeepsTopLevelOnes(t *testing.T) {
	c := cannedConversation()
	d := cannedDetail()
	got := assemble(cannedSummary(), &d, &c)
	if got.ID != "octo/widgets#42" || got.Repo != "octo/widgets" || got.Number != 42 || got.Title != "Add widget cache" {
		t.Fatalf("PR identity mismatch: %+v", got)
	}
	if len(got.Comments) != 3 || got.Comments[0].ID != "IC_1" {
		t.Fatalf("top-level comments = %+v", got.Comments)
	}
	if len(got.Reviews) != 2 || len(got.Reviews[0].Comments) != 1 || got.Reviews[0].Comments[0].ID != "RC_3" {
		t.Fatalf("review-nested comments mismatch: %+v", got.Reviews)
	}
	if got.Reviews[0].SubmittedAt != "2026-10-01T09:00:00Z" {
		t.Errorf("Reviews[0].SubmittedAt = %q", got.Reviews[0].SubmittedAt)
	}
}

func TestToSchemaPR_CarriesReviewThreadIDFlagsAndConnectionReports(t *testing.T) {
	c := cannedConversation()
	got := assemble(cannedSummary(), nil, &c)
	in := got.Reviews[0].Comments[0]
	if in.ReviewThreadID != "PRRT_1" || in.ThreadID != "RC_3" || !in.Resolved || !in.ThreadOutdated {
		t.Errorf("inline comment = %+v", in)
	}
	want := schema.PRConnections{
		Reviews:  schema.PRConnectionReport{Total: 2, Returned: 2},
		Threads:  schema.PRConnectionReport{Total: 2, Returned: 2},
		Comments: schema.PRConnectionReport{Truncated: true, Total: 1001, Returned: 1000},
	}
	if got.Connections == nil || *got.Connections != want {
		t.Errorf("Connections = %+v, want %+v", got.Connections, want)
	}
}

func TestToSchemaPR_CommentCountCountsOnlyTopLevelComments(t *testing.T) {
	c := cannedConversation() // IC_1, IC_2 top-level; RC_4 inline, review not joined, lands in Comments
	got := assemble(cannedSummary(), nil, &c)
	if got.CommentCount != 2 {
		t.Fatalf("CommentCount = %d, want 2 (top-level only; the summary's 99 must be ignored)", got.CommentCount)
	}
	if len(got.Comments) != 3 {
		t.Fatalf("len(Comments) = %d, want 3 (2 top-level + 1 inline with no joined review)", len(got.Comments))
	}
}

func TestToSchemaPR_ACommentlessConversationCountsZeroAndAnAbsentOneFallsBackToTheSummary(t *testing.T) {
	empty := conversation.Content{Comments: []schema.PRComment{}, Reviews: []schema.PRReview{}}
	got := assemble(cannedSummary(), nil, &empty)
	if got.CommentCount != 0 || got.ReviewCount != 0 {
		t.Errorf("counts of an empty conversation = %d, %d, want 0, 0", got.CommentCount, got.ReviewCount)
	}
	if got.Comments != nil || got.Reviews != nil {
		t.Errorf("an empty conversation must leave comments and reviews absent: %+v", got)
	}
	absent := assemble(cannedSummary(), nil, nil)
	if absent.CommentCount != 99 || absent.ReviewCount != 99 || absent.Connections != nil {
		t.Errorf("absent conversation = comment_count %d review_count %d connections %v, want the summary's 99, 99 and none",
			absent.CommentCount, absent.ReviewCount, absent.Connections)
	}
}

func TestToSchemaPR_FieldTable(t *testing.T) {
	d := cannedDetail()
	d.Merged = true
	c := cannedConversation()
	got := assemble(cannedSummary(), &d, &c)
	checks := []struct {
		name      string
		got, want any
	}{
		{"state is lower case", got.State, "open"},
		{"branch", got.Branch, "feature/widget-cache"},
		{"base", got.Base, "main"},
		{"merged is the detail's", got.Merged, true},
		{"review_thread_count stays zero", got.ReviewThreadCount, 0},
		{"label_count is len(labels)", got.LabelCount, 2},
		{"review_count is len(reviews)", got.ReviewCount, 2},
		{"base_sha is the summary's base commit", got.BaseSHA, "2222222222222222222222222222222222222222"},
		{"head_sha", got.HeadSHA, "1111111111111111111111111111111111111111"},
		{"checks_rollup folds SUCCESS", got.ChecksRollup, "success"},
		{"merge_state_status", got.MergeStateStatus, "CLEAN"},
		{"review_requests", got.ReviewRequests, []string{"teammate", "reviewers"}},
		{"additions", got.Additions, 10},
		{"deletions", got.Deletions, 2},
		{"changed_files", got.ChangedFiles, 3},
		{"as_of is not the assembler's", got.AsOf, ""},
		{"stale is not the assembler's", got.Stale, false},
		{"node_id", got.NodeID, "PR_kwDOExample1"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, c.got, c.want)
		}
	}
	for state, want := range map[string]string{"": "none", "PENDING": "pending", "EXPECTED": "pending", "FAILURE": "failure", "ERROR": "failure"} {
		s := cannedSummary()
		s.ChecksState = state
		if got := assemble(s, nil, nil).ChecksRollup; got != want {
			t.Errorf("checks state %q folds to %q, want %q", state, got, want)
		}
	}
}

func TestToSchemaPR_BaseSHAAbsentStaysEmpty(t *testing.T) {
	s := cannedSummary()
	s.BaseRefOid = ""
	if got := assemble(s, nil, nil).BaseSHA; got != "" {
		t.Errorf("BaseSHA = %q, want empty", got)
	}
}

func TestToSchemaPR_ReviewsCarryCommitOID(t *testing.T) {
	c := cannedConversation()
	got := assemble(cannedSummary(), nil, &c)
	if o := got.Reviews[0].CommitOID; o == nil || *o != "abc123" {
		t.Errorf("review 0 commit_oid = %v, want abc123", o)
	}
	if o := got.Reviews[1].CommitOID; o == nil || *o != "" {
		t.Errorf("review 1 commit_oid = %v, want a pointer to the empty string, not absent", o)
	}
	raw, _ := json.Marshal(got.Reviews[1])
	if !strings.Contains(string(raw), `"commit_oid":""`) {
		t.Errorf("wire JSON %s must carry commit_oid as an empty string", raw)
	}
	// The answer must not alias the stored conversation.
	*got.Reviews[0].CommitOID = "changed"
	if *c.Reviews[0].CommitOID != "abc123" {
		t.Error("the assembled review aliases the stored commit_oid")
	}
}
