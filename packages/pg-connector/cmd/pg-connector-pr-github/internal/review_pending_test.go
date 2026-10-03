package internal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
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

// markedBody is a body carrying the backend's own marker.
var markedBody = "finding\n" + github.BotMarker

func pendingReviewAt(commit string, comments ...github.PendingReviewComment) *github.PendingReviewNode {
	return &github.PendingReviewNode{
		ID: "PRR_node", DatabaseID: 5001, CommitOID: commit, Body: markedBody, Comments: comments,
	}
}

func TestReviewPendingNone(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1"}}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	if res.Pending || res.Review != nil || res.HeadSHA != "head1" || res.AsOf == "" {
		t.Fatalf("want explicit none with head and as_of; got %+v", res)
	}
}

func TestReviewPendingSameHead(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{
		HeadSHA: "head1",
		Review: pendingReviewAt("HEAD1", // case-insensitive SHA comparison
			github.PendingReviewComment{ID: "c1", Path: "a.go", Line: 3, Body: "x " + github.BotMarker},
			github.PendingReviewComment{ID: "c2", Path: "b.go", Line: 9, Body: "y " + github.BotMarker}),
	}}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	r := res.Review
	if !res.Pending || r == nil {
		t.Fatalf("want a pending record; got %+v", res)
	}
	if r.ReviewID != "PRR_node" || r.DatabaseID != 5001 || r.State != "pending" || r.CommitSHA != "HEAD1" || r.Body != markedBody {
		t.Errorf("record fields = %+v", r)
	}
	if r.Stale {
		t.Errorf("same head must not be stale: %+v", r)
	}
	if !r.BodyMarked || !r.AllMarked || len(r.Comments) != 2 || !r.Comments[0].Marked || !r.Comments[1].Marked {
		t.Errorf("all content is marked, got %+v", r)
	}
	if r.Comments[0].Path != "a.go" || r.Comments[0].Line != 3 || r.Comments[0].ID != "c1" {
		t.Errorf("comment fields = %+v", r.Comments[0])
	}
}

func TestReviewPendingDifferentHeadIsStale(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{
		HeadSHA: "head2",
		Review:  pendingReviewAt("head1", github.PendingReviewComment{ID: "c1", Body: "z " + github.BotMarker}),
	}}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	if !res.Review.Stale || res.Review.CommitSHA != "head1" || res.HeadSHA != "head2" {
		t.Fatalf("a different review commit must be stale; got %+v", res)
	}
}

func TestReviewPendingNullCommitIsStale(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Review: pendingReviewAt("")}}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	if !res.Review.Stale || res.Review.CommitSHA != "" {
		t.Fatalf("a review with no commit must be stale; got %+v", res.Review)
	}
}

func TestReviewPendingPartlyUnmarked(t *testing.T) {
	cases := map[string]*github.PendingReviewNode{
		"unmarked comment": {
			ID: "R", Body: markedBody, CommitOID: "head1",
			Comments: []github.PendingReviewComment{
				{ID: "c1", Body: "ok " + github.BotMarker},
				{ID: "c2", Body: "a human added this"},
			},
		},
		"unmarked body": {
			ID: "R", Body: "human wrote this", CommitOID: "head1",
			Comments: []github.PendingReviewComment{{ID: "c1", Body: "ok " + github.BotMarker}},
		},
	}
	for name, rev := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Review: rev}}
			res, err := New(gh).PendingReview(context.Background(), pendingReq())
			if err != nil {
				t.Fatalf("PendingReview: %v", err)
			}
			if res.Review.AllMarked {
				t.Fatalf("AllMarked must be false: %+v", res.Review)
			}
		})
	}
	// per-element flags are individually correct
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Review: cases["unmarked comment"]}}
	res, _ := New(gh).PendingReview(context.Background(), pendingReq())
	if !res.Review.BodyMarked || !res.Review.Comments[0].Marked || res.Review.Comments[1].Marked {
		t.Errorf("per-element marker flags wrong: %+v", res.Review)
	}
}

func TestReviewPendingLegacyPGPRMarkerCounts(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Review: &github.PendingReviewNode{
		ID: "R", CommitOID: "head1", Body: legacyPGPRMarker + "\nbody",
		Comments: []github.PendingReviewComment{{ID: "c1", Body: legacyPGPRMarker + "\nc"}},
	}}}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	if !res.Review.AllMarked {
		t.Fatalf("a pg-pr marker must count as marked: %+v", res.Review)
	}
}

func TestReviewPendingNoCommentsBodyMarkedIsAllMarked(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head1", Review: pendingReviewAt("head1")}}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	if !res.Review.AllMarked || res.Review.Comments == nil || len(res.Review.Comments) != 0 {
		t.Fatalf("a marked body with no comments is all-marked with an empty (non-nil) list: %+v", res.Review)
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

// postedAsBackend returns a pending review whose body and comments are exactly
// the text the backend would have posted (digest marker on the body, plain
// marker on each comment), anchored at commit.
func postedAsBackend(commit, body string, comments ...string) *github.PendingReviewNode {
	rev := &github.PendingReviewNode{
		ID: "PRR_node", DatabaseID: 5001, URL: "https://example.invalid/pr/42#review-5001", CommitOID: commit,
	}
	texts := make([]string, 0, len(comments))
	for i, c := range comments {
		t := github.StampBotMarker(c)
		texts = append(texts, t)
		rev.Comments = append(rev.Comments, github.PendingReviewComment{
			ID: "C" + string(rune('1'+i)), DatabaseID: int64(i + 1), Path: "a.go", Line: i + 1, Body: t,
		})
	}
	rev.Body = github.StampBodyWithDigest(body, texts)
	return rev
}

func TestReviewPendingReportsURLAndDigestState(t *testing.T) {
	gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head2", Review: postedAsBackend("head1", "summary", "one", "two")}}
	res, err := New(gh).PendingReview(context.Background(), pendingReq())
	if err != nil {
		t.Fatalf("PendingReview: %v", err)
	}
	r := res.Review
	if r.URL != "https://example.invalid/pr/42#review-5001" {
		t.Errorf("URL = %q", r.URL)
	}
	if r.DigestState != "verified" || !r.BodyMarked || !r.AllMarked {
		t.Errorf("untouched backend content must be verified and fully marked: %+v", r)
	}
}

// TestReviewPendingDigestStateTable: the digest, not the marker, is what makes
// content verified-unedited; a review with no digest is never verified.
func TestReviewPendingDigestStateTable(t *testing.T) {
	mut := func(f func(*github.PendingReviewNode)) *github.PendingReviewNode {
		r := postedAsBackend("head1", "summary", "one", "two")
		f(r)
		return r
	}
	cases := map[string]struct {
		rev        *github.PendingReviewNode
		wantDigest string
		wantAll    bool
	}{
		"untouched": {postedAsBackend("head1", "summary", "one", "two"), "verified", true},
		"CRLF-converted body and comments, text otherwise unchanged": {mut(func(r *github.PendingReviewNode) {
			r.Body = strings.ReplaceAll(r.Body, "\n", "\r\n")
			for i := range r.Comments {
				r.Comments[i].Body = strings.ReplaceAll(r.Comments[i].Body, "\n", "\r\n")
			}
		}), "verified", true},
		"marker-preserving comment text edit": {mut(func(r *github.PendingReviewNode) {
			r.Comments[0].Body = strings.Replace(r.Comments[0].Body, "one", "ONE (edited)", 1)
		}), "mismatch", true},
		"marker removed from a comment": {mut(func(r *github.PendingReviewNode) {
			r.Comments[1].Body = strings.Replace(r.Comments[1].Body, github.BotMarker, "", 1)
		}), "mismatch", false},
		"unmarked comment added": {mut(func(r *github.PendingReviewNode) {
			r.Comments = append(r.Comments, github.PendingReviewComment{ID: "CX", Body: "a human added this"})
		}), "mismatch", false},
		"body text edited": {mut(func(r *github.PendingReviewNode) {
			r.Body = strings.Replace(r.Body, "summary", "summary (edited)", 1)
		}), "mismatch", true},
		"posted before digests existed": {&github.PendingReviewNode{
			ID: "R", DatabaseID: 1, CommitOID: "head1", Body: "x\n" + github.BotMarker,
			Comments: []github.PendingReviewComment{{ID: "C", Body: "y " + github.BotMarker}},
		}, "missing", true},
		"left by pg-pr": {&github.PendingReviewNode{
			ID: "R", DatabaseID: 1, CommitOID: "head1", Body: "x <!-- pg-pr -->",
		}, "missing", true},
		"damaged digest": {mut(func(r *github.PendingReviewNode) {
			r.Body = strings.Replace(r.Body, github.DigestMarkerPrefix, github.DigestMarkerPrefix+"zz", 1)
		}), "unreadable", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGH{pendingData: &github.PendingReviewData{HeadSHA: "head2", Review: c.rev}}
			res, err := New(gh).PendingReview(context.Background(), pendingReq())
			if err != nil {
				t.Fatalf("PendingReview: %v", err)
			}
			if res.Review.DigestState != c.wantDigest || res.Review.AllMarked != c.wantAll {
				t.Errorf("digest_state=%q all_marked=%v, want %q / %v", res.Review.DigestState, res.Review.AllMarked, c.wantDigest, c.wantAll)
			}
		})
	}
}
