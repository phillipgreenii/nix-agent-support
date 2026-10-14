package pending

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// These tests are the old backend's review_pending tests
// (cmd/pg-connector-pr-github/internal/review_pending_test.go) re-run against
// stored data: the data is encoded and decoded through the stored form before
// Compute reads it.

var fetchedAt = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// stored round-trips data through the stored content, as a group row holds it.
func stored(t *testing.T, data *github.PendingReviewData) *github.PendingReviewData {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func compute(t *testing.T, data *github.PendingReviewData, liveHead string, last *pr.LastAppend) pr.PendingReviewResult {
	t.Helper()
	res, err := Compute(stored(t, data), liveHead, "foo/bar", 42, fetchedAt, last)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return res
}

func pendingCmt(id, originalCommit string) github.PendingReviewComment {
	return github.PendingReviewComment{ID: id, DatabaseID: 1, Path: "a.go", Line: 3, Body: "x", OriginalCommitOID: originalCommit}
}

func pendingRev(dbID int64, commit, body string, comments ...github.PendingReviewComment) github.PendingReviewNode {
	return github.PendingReviewNode{ID: "PRR_" + string(rune('A'+dbID%26)), DatabaseID: dbID, URL: "https://example.invalid/r", CommitOID: commit, Body: body, Comments: comments}
}

func TestComputeNone(t *testing.T) {
	res := compute(t, &github.PendingReviewData{HeadSHA: "head1"}, "", nil)
	if res.Pending || res.Review != nil || res.HeadSHA != "head1" || res.AsOf != "2026-10-12T12:00:00Z" {
		t.Fatalf("want explicit none with the head and the group's fetched_at as as_of; got %+v", res)
	}
}

func TestComputeOne(t *testing.T) {
	rev := pendingRev(5001, "head0", "summary", pendingCmt("c1", "head0"), pendingCmt("c2", "HEAD1"), pendingCmt("c3", "head1"))
	res := compute(t, &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{rev}}, "", nil)
	r := res.Review
	if !res.Pending || r == nil {
		t.Fatalf("want a pending record; got %+v", res)
	}
	if r.DatabaseID != 5001 || r.State != "pending" || r.CommitSHA != "head0" || r.Body != "summary" || r.URL != "https://example.invalid/r" {
		t.Errorf("record fields = %+v", r)
	}
	if r.CommentsTotal != 3 || r.CommentsAtHead != 2 || r.ExtraPendingReviews != 0 || len(r.Comments) != 3 {
		t.Errorf("counts = total %d at_head %d extra %d (originalCommit is compared case-insensitively with the live head)", r.CommentsTotal, r.CommentsAtHead, r.ExtraPendingReviews)
	}
	if r.Comments[0].OriginalCommit != "head0" || r.Comments[0].Path != "a.go" || r.Comments[0].Line != 3 || r.Comments[0].ID != "c1" {
		t.Errorf("comment fields = %+v", r.Comments[0])
	}
	if r.Stale || r.LastAppend != nil {
		t.Errorf("two comments at the head is not stale and nothing was appended: %+v", r)
	}
}

func TestComputeThreeReportsLowestAndCountsTheRest(t *testing.T) {
	// The fetcher stores the lowest databaseId first (Lowest); Compute describes Reviews[0].
	low := pendingRev(11, "head0", "low body", pendingCmt("l1", "head0"))
	mid := pendingRev(22, "head0", "mid body", pendingCmt("m1", "head1"), pendingCmt("m2", "head1"))
	high := pendingRev(33, "head0", "high body")
	res := compute(t, &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{low, mid, high}}, "", nil)
	r := res.Review
	if r.DatabaseID != 11 || r.Body != "low body" || r.ExtraPendingReviews != 2 {
		t.Fatalf("want the lowest databaseId with extra 2; got %+v", r)
	}
	if r.CommentsTotal != 1 || r.CommentsAtHead != 0 || !r.Stale {
		t.Errorf("counts must be over the described review only and it has nothing at the head: %+v", r)
	}
}

func TestComputeManyComments(t *testing.T) {
	comments := make([]github.PendingReviewComment, 0, 103)
	for i := 0; i < 103; i++ {
		at := "head0"
		if i%2 == 0 {
			at = "head1"
		}
		comments = append(comments, pendingCmt("c"+string(rune('0'+i%10))+string(rune('a'+i/10)), at))
	}
	res := compute(t, &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{pendingRev(1, "head0", "b", comments...)}}, "", nil)
	if res.Review.CommentsTotal != 103 || res.Review.CommentsAtHead != 52 || len(res.Review.Comments) != 103 {
		t.Fatalf("total %d at_head %d listed %d, want 103 / 52 / 103", res.Review.CommentsTotal, res.Review.CommentsAtHead, len(res.Review.Comments))
	}
}

func TestComputeStaleTable(t *testing.T) {
	section := "summary\n<!-- pg-section head=head1000000a -->\nnew\n<!-- /pg-section -->"
	head := "head1000000a1234567890"
	cases := []struct {
		name         string
		data         github.PendingReviewData
		wantAtHead   int
		wantReviewed bool
		wantStale    bool
	}{
		{"comments at head, not reviewed", github.PendingReviewData{Reviews: []github.PendingReviewNode{pendingRev(1, "old", "s", pendingCmt("c", head))}}, 1, false, false},
		{"comments at head, reviewed", github.PendingReviewData{Reviews: []github.PendingReviewNode{pendingRev(1, head, "s", pendingCmt("c", head))}}, 1, true, false},
		{"no comments at head, not reviewed: stale", github.PendingReviewData{Reviews: []github.PendingReviewNode{pendingRev(1, "old", "s", pendingCmt("c", "old"))}}, 0, false, true},
		{"no comments at all, not reviewed: stale", github.PendingReviewData{Reviews: []github.PendingReviewNode{pendingRev(1, "old", "s")}}, 0, false, true},
		{"body section only for the head", github.PendingReviewData{Reviews: []github.PendingReviewNode{pendingRev(1, "old", section, pendingCmt("c", "old"))}}, 0, true, false},
		{"replies only: a submitted review of the viewer is at the head", github.PendingReviewData{
			Reviews:   []github.PendingReviewNode{pendingRev(1, "old", "s", pendingCmt("c", "old"))},
			Submitted: []github.SubmittedReviewNode{{ID: "S", DatabaseID: 9, CommitOID: head}},
		}, 0, true, false},
		{"review created at the head, empty", github.PendingReviewData{Reviews: []github.PendingReviewNode{pendingRev(1, head, "s")}}, 0, true, false},
		{"another pending review is at the head", github.PendingReviewData{Reviews: []github.PendingReviewNode{
			pendingRev(1, "old", "s"), pendingRev(2, head, "s"),
		}}, 0, true, false},
		{"section for another head does not count", github.PendingReviewData{Reviews: []github.PendingReviewNode{
			pendingRev(1, "old", "<!-- pg-section head=ffffffffffff -->\nx\n<!-- /pg-section -->"),
		}}, 0, false, true},
		{"submitted review of the viewer at an older head does not count", github.PendingReviewData{
			Reviews:   []github.PendingReviewNode{pendingRev(1, "old", "s")},
			Submitted: []github.SubmittedReviewNode{{ID: "S", CommitOID: "older"}},
		}, 0, false, true},
		{"a submitted review with no commit does not count", github.PendingReviewData{
			Reviews:   []github.PendingReviewNode{pendingRev(1, "old", "s")},
			Submitted: []github.SubmittedReviewNode{{ID: "S"}},
		}, 0, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := c.data
			data.HeadSHA = head
			r := compute(t, &data, "", nil).Review
			if r.CommentsAtHead != c.wantAtHead || r.ReviewedHead != c.wantReviewed || r.Stale != c.wantStale {
				t.Fatalf("at_head=%d reviewed_head=%v stale=%v, want %d / %v / %v", r.CommentsAtHead, r.ReviewedHead, r.Stale, c.wantAtHead, c.wantReviewed, c.wantStale)
			}
		})
	}
}

// The data was fetched at head A and the PR has since moved to head B inside
// pending_ttl: the result is judged against B, and head_sha is B.
func TestComputeJudgesAgainstTheLiveHeadWhenItMoved(t *testing.T) {
	data := &github.PendingReviewData{HeadSHA: "headA", Reviews: []github.PendingReviewNode{
		pendingRev(1, "headA", "s", pendingCmt("c1", "headA"), pendingCmt("c2", "headA")),
	}}

	atFetch := compute(t, data, "", nil)
	if atFetch.Review.CommentsAtHead != 2 || atFetch.Review.Stale || atFetch.HeadSHA != "headA" {
		t.Fatalf("with no live head the data's head is used: %+v", atFetch)
	}
	if got := compute(t, data, "", nil); got.HeadSHA != "headA" {
		t.Errorf("an empty live head must fall back to the data's head, got %q", got.HeadSHA)
	}

	moved := compute(t, data, "headB", nil)
	if moved.HeadSHA != "headB" {
		t.Errorf("head_sha = %q, want the live head headB", moved.HeadSHA)
	}
	r := moved.Review
	if r.CommentsAtHead != 0 || !r.Stale || r.ReviewedHead {
		t.Errorf("no comment is originally at headB and the review commit is headA: want comments_at_head 0, reviewed_head false, stale true; got %+v", r)
	}
	// The same holds for a "no pending review" answer: it carries the live head.
	none := compute(t, &github.PendingReviewData{HeadSHA: "headA"}, "headB", nil)
	if none.Pending || none.HeadSHA != "headB" {
		t.Errorf("none = %+v, want head_sha headB", none)
	}
}

func TestComputeLastAppendPresentAndAbsent(t *testing.T) {
	data := &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{pendingRev(1, "head1", "s")}}
	if got := compute(t, data, "", nil).Review.LastAppend; got != nil {
		t.Errorf("last_append = %+v, want none", got)
	}
	want := pr.LastAppend{At: "2026-01-01T00:00:00Z", Added: 4, Head: "head0"}
	got := compute(t, data, "", &want).Review.LastAppend
	if got == nil || *got != want {
		t.Fatalf("last_append = %+v, want %+v", got, want)
	}
}

// The record carries none of the retired marker and digest fields on the wire.
func TestComputeRecordCarriesExactlyTheDocumentedKeys(t *testing.T) {
	res := compute(t, &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{pendingRev(1, "head1", "s", pendingCmt("c", "head1"))}}, "", nil)
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Review map[string]any `json:"review"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"review_id": true, "database_id": true, "url": true, "state": true, "commit_sha": true,
		"stale": true, "body": true, "comments": true, "comments_total": true, "comments_at_head": true,
		"reviewed_head": true, "extra_pending_reviews": true, "last_append": true,
	}
	for k := range m.Review {
		if !allowed[k] {
			t.Errorf("record carries undocumented key %q: %s", k, raw)
		}
	}
	for _, want := range []string{"comments_total", "comments_at_head", "reviewed_head", "extra_pending_reviews", "stale"} {
		if _, ok := m.Review[want]; !ok {
			t.Errorf("record is missing %s: %s", want, raw)
		}
	}
}

func TestComputeHeadlessDataIsFailClosed(t *testing.T) {
	for name, data := range map[string]*github.PendingReviewData{
		"nil data": nil,
		"no head":  {},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := Compute(data, "", "foo/bar", 42, fetchedAt, nil)
			if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "review_pending: detection_failed:") {
				t.Fatalf("err = %v, want unavailable review_pending: detection_failed:", err)
			}
			if res.Pending || res.Review != nil || res.HeadSHA != "" {
				t.Errorf("a refusal must not yield a result: %+v", res)
			}
		})
	}
}

// The stored shape is stable: a committed stored row decodes and computes.
func TestDecodeStoredFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/stored_two_reviews.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Compute(data, "", "foo/bar", 42, fetchedAt, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Review
	if r == nil || r.DatabaseID != 11 || r.ExtraPendingReviews != 1 || r.CommentsAtHead != 1 || r.Stale || r.Comments[0].ID != "c1" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := Decode([]byte("not json")); err == nil {
		t.Error("Decode accepted garbage")
	}
}
