package internal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	pgposted "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/posted"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// pendingReply is one scripted GetPendingReview answer.
type pendingReply struct {
	data *github.PendingReviewData
	err  error
}

func (f *fakeGH) GetPendingReview(ctx context.Context, repo string, number int) (*github.PendingReviewData, error) {
	f.ops = append(f.ops, "lookup")
	if f.host != nil {
		return f.host.snapshot()
	}
	if len(f.pendingSeq) > 0 {
		i := f.pendingCalls
		if i >= len(f.pendingSeq) {
			i = len(f.pendingSeq) - 1
		}
		f.pendingCalls++
		return f.pendingSeq[i].data, f.pendingSeq[i].err
	}
	f.pendingCalls++
	if f.pendingErr != nil {
		return nil, f.pendingErr
	}
	return f.pendingData, nil
}

func pendingReq() pr.PendingReviewRequest { return pr.PendingReviewRequest{ID: "foo/bar#42"} }

// reviewBody is the body the fixture reviews carry.
const reviewBody = "finding"

func pendingReviewAt(commit string, comments ...github.PendingReviewComment) *github.PendingReviewNode {
	return &github.PendingReviewNode{
		ID: "PRR_node", DatabaseID: 5001, CommitOID: commit, Body: reviewBody, Comments: comments,
	}
}

// reviewsOf wraps one pending review as the lookup's Reviews list (none for
// nil).
func reviewsOf(rev *github.PendingReviewNode) []github.PendingReviewNode {
	if rev == nil {
		return nil
	}
	return []github.PendingReviewNode{*rev}
}

// pendingCmt is a comment of a pending review anchored to originalCommit.
func pendingCmt(id, originalCommit string) github.PendingReviewComment {
	return github.PendingReviewComment{ID: id, DatabaseID: 1, Path: "a.go", Line: 3, Body: "x", OriginalCommitOID: originalCommit}
}

// pendingRev is a pending review with the given database id, review-level commit,
// body and comments.
func pendingRev(dbID int64, commit, body string, comments ...github.PendingReviewComment) github.PendingReviewNode {
	return github.PendingReviewNode{ID: "PRR_" + string(rune('A'+dbID%26)), DatabaseID: dbID, URL: "https://example.invalid/r", CommitOID: commit, Body: body, Comments: comments}
}

func lookupPending(t *testing.T, gh *fakeGH) pr.PendingReviewResult {
	t.Helper()
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	return res
}

func TestReviewPendingNone(t *testing.T) {
	res := lookupPending(t, &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1"}})
	if res.Pending || res.Review != nil || res.HeadSHA != "head1" || res.AsOf == "" {
		t.Fatalf("want explicit none with head and as_of; got %+v", res)
	}
}

func TestReviewPendingOne(t *testing.T) {
	rev := pendingRev(5001, "head0", "summary", pendingCmt("c1", "head0"), pendingCmt("c2", "HEAD1"), pendingCmt("c3", "head1"))
	res := lookupPending(t, &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{rev}}})
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

// Several pending reviews are not an error: the lowest databaseId is described
// and the rest are counted.
func TestReviewPendingThreeReportsLowestAndCountsTheRest(t *testing.T) {
	low := pendingRev(11, "head0", "low body", pendingCmt("l1", "head0"))
	mid := pendingRev(22, "head0", "mid body", pendingCmt("m1", "head1"), pendingCmt("m2", "head1"))
	high := pendingRev(33, "head0", "high body")
	res := lookupPending(t, &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{low, mid, high}}})
	r := res.Review
	if r.DatabaseID != 11 || r.Body != "low body" || r.ExtraPendingReviews != 2 {
		t.Fatalf("want the lowest databaseId with extra 2; got %+v", r)
	}
	if r.CommentsTotal != 1 || r.CommentsAtHead != 0 {
		t.Errorf("counts must be over the described review only: total %d at_head %d", r.CommentsTotal, r.CommentsAtHead)
	}
	if !r.Stale {
		t.Errorf("the described review has nothing at the head, so it is stale: %+v", r)
	}
}

// 103 comments in one review: all of them are counted.
func TestReviewPendingManyComments(t *testing.T) {
	comments := make([]github.PendingReviewComment, 0, 103)
	for i := 0; i < 103; i++ {
		at := "head0"
		if i%2 == 0 {
			at = "head1"
		}
		comments = append(comments, pendingCmt("c"+string(rune('0'+i%10))+string(rune('a'+i/10)), at))
	}
	res := lookupPending(t, &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{pendingRev(1, "head0", "b", comments...)}}})
	if res.Review.CommentsTotal != 103 || res.Review.CommentsAtHead != 52 || len(res.Review.Comments) != 103 {
		t.Fatalf("total %d at_head %d listed %d, want 103 / 52 / 103", res.Review.CommentsTotal, res.Review.CommentsAtHead, len(res.Review.Comments))
	}
}

// TestReviewPendingStaleTable covers every cell of (comments_at_head above 0 or
// 0) by (reviewed_head true or false), and the three ways reviewed_head is true
// without any comment at the head.
func TestReviewPendingStaleTable(t *testing.T) {
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
			r := lookupPending(t, &fakeGH{pendingData: &data}).Review
			if r.CommentsAtHead != c.wantAtHead || r.ReviewedHead != c.wantReviewed || r.Stale != c.wantStale {
				t.Fatalf("at_head=%d reviewed_head=%v stale=%v, want %d / %v / %v", r.CommentsAtHead, r.ReviewedHead, r.Stale, c.wantAtHead, c.wantReviewed, c.wantStale)
			}
		})
	}
}

// last_append comes from the posted-sidecar and is absent when nothing was
// appended, when no sidecar store is configured and when the file is corrupt.
func TestReviewPendingLastAppendFromSidecar(t *testing.T) {
	dir := t.TempDir()
	store := pgposted.Store{Dir: dir}
	data := &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{pendingRev(1, "head1", "s")}}

	if got := lookupPending(t, &fakeGH{pendingData: data}).Review.LastAppend; got != nil {
		t.Errorf("no store configured: last_append = %+v, want none", got)
	}
	b := New(&fakeGH{pendingData: data}).WithPostedStore(store)
	res, err := b.PendingReview(context.Background(), pendingReq())
	if err != nil || res.Review.LastAppend != nil {
		t.Fatalf("no sidecar file: %+v %v", res.Review, err)
	}

	st := pgposted.State{LastAppend: &pgposted.LastAppend{At: "2026-01-01T00:00:00Z", Added: 4, Head: "head0"}}
	if err := store.Save("foo", "bar", 42, st); err != nil {
		t.Fatal(err)
	}
	res, err = b.PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatal(err)
	}
	want := pr.LastAppend{At: "2026-01-01T00:00:00Z", Added: 4, Head: "head0"}
	if res.Review.LastAppend == nil || *res.Review.LastAppend != want {
		t.Fatalf("last_append = %+v, want %+v", res.Review.LastAppend, want)
	}

	path, _ := store.Path("foo", "bar", 42)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = b.PendingReview(context.Background(), pendingReq())
	if err != nil || res.Review.LastAppend != nil {
		t.Fatalf("corrupt sidecar must degrade to no last_append, not fail: %+v %v", res.Review, err)
	}
}

// The record carries none of the retired marker and digest fields on the wire.
func TestReviewPendingRecordCarriesExactlyTheDocumentedKeys(t *testing.T) {
	res := lookupPending(t, &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Reviews: []github.PendingReviewNode{pendingRev(1, "head1", "s", pendingCmt("c", "head1"))}}})
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
	for k := range m.Review["comments"].([]any)[0].(map[string]any) {
		switch k {
		case "id", "path", "line", "body", "original_commit":
		default:
			t.Errorf("comment carries undocumented key %q: %s", k, raw)
		}
	}
}

// A failed lookup is an error from the closed taxonomy, prefixed so a caller
// can report reason detection_failed, and is never a "none" answer.
func TestReviewPendingLookupErrorIsFailClosed(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"transport", errors.New("gh: Bad Gateway (HTTP 502)"), scriptout.ErrUnavailable},
		{"parse", errors.New("github: parse pending-review graphql response: boom"), scriptout.ErrUnavailable},
		{"auth", errors.Join(github.ErrGHAuthInvalid, errors.New("gh: Forbidden (HTTP 403)")), scriptout.ErrUnauthenticated},
		{"missing pr", errors.New("GraphQL: Could not resolve to a PullRequest with the number of 42. (repository.pullRequest)"), scriptout.ErrNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := &fakeGH{pendingErr: c.err}
			res, err := New(gh).PendingReview(context.Background(), pendingReq())
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if !strings.Contains(err.Error(), "detection_failed") {
				t.Errorf("message must carry detection_failed: %v", err)
			}
			if res.Pending || res.Review != nil || res.HeadSHA != "" {
				t.Errorf("a failed lookup must not yield a result: %+v", res)
			}
		})
	}
}

func TestReviewPendingNoHeadIsFailClosed(t *testing.T) {
	for name, data := range map[string]*github.PendingReviewData{
		"nil data": nil,
		"no head":  {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(&fakeGH{pendingData: data}).PendingReview(context.Background(), pendingReq())
			if !errors.Is(err, scriptout.ErrUnavailable) || !strings.Contains(err.Error(), "detection_failed") {
				t.Fatalf("err = %v, want unavailable detection_failed", err)
			}
		})
	}
}

func TestReviewPendingBadIDIsInvalidArgument(t *testing.T) {
	_, err := New(&fakeGH{}).PendingReview(context.Background(), pr.PendingReviewRequest{ID: "nope"})
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

// A lookup that the GitHub layer failed (a truncated connection, an unresolved
// PR, a pending review that is not the viewer's) is an error, never none.
func TestReviewPendingLookupFailureNeverReportsNone(t *testing.T) {
	gh := &fakeGH{pendingErr: errors.New("github: pending reviews truncated (100 of 150 read)")}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err == nil || res.Pending || res.Review != nil {
		t.Fatalf("want an error and no result; got %+v / %v", res, err)
	}
}
