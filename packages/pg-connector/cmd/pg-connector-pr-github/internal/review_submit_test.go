package internal

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/pr"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// postedReview records one PostPendingReview call on fakeGH.
type postedReview struct {
	repo     string
	number   int
	commitID string
	body     string
	comments []github.ReviewSubmitComment
}

func (f *fakeGH) DeleteReview(ctx context.Context, repo string, number int, reviewID int64) error {
	f.ops = append(f.ops, "delete")
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, reviewID)
	return nil
}

func (f *fakeGH) PostPendingReview(ctx context.Context, repo string, number int, commitID, body string, comments []github.ReviewSubmitComment) (*api.Review, error) {
	f.ops = append(f.ops, "post")
	if f.postErr != nil {
		return nil, f.postErr
	}
	f.posts = append(f.posts, postedReview{repo, number, commitID, body, comments})
	return &api.Review{ID: "PRR_node", State: "pending"}, nil
}

func baseSubmitReq() pr.ReviewSubmitRequest {
	return pr.ReviewSubmitRequest{
		ID:      "foo/bar#42",
		HeadSHA: "deadbeef",
		Body:    "looks mostly fine",
		Comments: []pr.ReviewComment{
			{Path: "main.go", Line: 12, Side: "RIGHT", Body: "rename x"},
		},
	}
}

// liveHeadPR is the PR the fake reports for GetPR: its head matches
// baseSubmitReq's head_sha, so the live-head pre-check passes.
func liveHeadPR() *api.PR { return &api.PR{HeadSHA: "deadbeef"} }

func TestReviewSubmitPendingOnly(t *testing.T) {
	gh := &fakeGH{pr: liveHeadPR()}
	res, err := New(gh).SubmitReview(context.Background(), baseSubmitReq())
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if res.State != "pending" || res.ReviewID != "PRR_node" || res.HeadSHA != "deadbeef" || res.AsOf == "" || res.Status != pr.StatusPosted {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.Supersede != nil {
		t.Errorf("Supersede must be nil when supersede_pending was not set; got %+v", res.Supersede)
	}
	if len(gh.posts) != 1 || gh.posts[0].commitID != "deadbeef" || gh.posts[0].repo != "foo/bar" || gh.posts[0].number != 42 {
		t.Fatalf("expected one post anchored to head_sha on foo/bar#42; got %+v", gh.posts)
	}
	if len(gh.deleted) != 0 {
		t.Errorf("nothing may be deleted without supersede_pending; got %v", gh.deleted)
	}
}

func TestReviewSubmitPost422IsInvalidArgument(t *testing.T) {
	cases := map[string]error{
		"post 422":        errors.New("github: post review: gh api failed: gh: Unprocessable Entity (HTTP 422)"),
		"non-head 422":    errors.New("github: post review: gh: Validation Failed: line must be part of the diff (HTTP 422)"),
		"upper-case 422":  errors.New("HTTP 422: Unprocessable Entity"),
		"wrapped message": errors.New("github: post review: exit 1: stderr: gh: Unprocessable Entity (HTTP 422)"),
	}
	for name, postErr := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGH{pr: liveHeadPR(), postErr: postErr}
			_, err := New(gh).SubmitReview(context.Background(), baseSubmitReq())
			if !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
		})
	}
}

func TestReviewSubmitErrorTaxonomy(t *testing.T) {
	gh := &fakeGH{pr: liveHeadPR(), postErr: errors.New("gh: Not Found (HTTP 404)")}
	if _, err := New(gh).SubmitReview(context.Background(), baseSubmitReq()); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("404: err = %v, want not_found", err)
	}
	gh = &fakeGH{pr: liveHeadPR(), postErr: errors.Join(github.ErrGHAuthInvalid, errors.New("gh: Forbidden (HTTP 403)"))}
	if _, err := New(gh).SubmitReview(context.Background(), baseSubmitReq()); !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Errorf("403: err = %v, want unauthenticated", err)
	}
	gh = &fakeGH{pr: liveHeadPR(), postErr: errors.New("gh: Bad Gateway (HTTP 502)")}
	if _, err := New(gh).SubmitReview(context.Background(), baseSubmitReq()); !errors.Is(err, scriptout.ErrUnavailable) {
		t.Errorf("502: err = %v, want unavailable", err)
	}
}

func TestReviewSubmitRejectsBadInput(t *testing.T) {
	mut := map[string]func(*pr.ReviewSubmitRequest){
		"bad id":   func(r *pr.ReviewSubmitRequest) { r.ID = "nope" },
		"no head":  func(r *pr.ReviewSubmitRequest) { r.HeadSHA = "" },
		"bad side": func(r *pr.ReviewSubmitRequest) { r.Comments[0].Side = "MIDDLE" },
		"no line":  func(r *pr.ReviewSubmitRequest) { r.Comments[0].Line = 0 },
		"no path":  func(r *pr.ReviewSubmitRequest) { r.Comments[0].Path = "" },
	}
	for name, m := range mut {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGH{pr: liveHeadPR()}
			req := baseSubmitReq()
			m(&req)
			_, err := New(gh).SubmitReview(context.Background(), req)
			if !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
			if len(gh.posts) != 0 {
				t.Errorf("nothing may be posted on invalid input; got %+v", gh.posts)
			}
		})
	}
}

func TestReviewSubmitLeftSideCarriedThrough(t *testing.T) {
	gh := &fakeGH{pr: liveHeadPR()}
	req := baseSubmitReq()
	req.Comments[0].Side = "left"
	if _, err := New(gh).SubmitReview(context.Background(), req); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if got := gh.posts[0].comments[0].Side; got != "LEFT" {
		t.Errorf("side = %q, want LEFT carried through", got)
	}
}

// TestReviewSubmitBotMarkerPresent: the backend stamps its marker. The stamp
// is applied by the github layer (PostPendingReview); this asserts the
// production marker function the backend relies on, end to end through the
// real github.Provider, in internal/github's tests. Here: the marker is the
// pg-connector-specific one and is idempotent.
func TestReviewSubmitBotMarkerPresent(t *testing.T) {
	stamped := github.StampBotMarker("hello")
	if !strings.Contains(stamped, github.BotMarker) || !strings.HasPrefix(stamped, "hello") {
		t.Fatalf("stamp = %q", stamped)
	}
	if github.StampBotMarker(stamped) != stamped {
		t.Errorf("StampBotMarker must be idempotent")
	}
	if strings.Contains(github.BotMarker, "pg-pr ") {
		t.Errorf("marker must be pg-connector specific, not pg-pr's: %q", github.BotMarker)
	}
	if !strings.HasPrefix(github.StampBotMarker(""), "_Posted") {
		t.Errorf("empty body must still receive the stamp")
	}
}

// TestReviewSubmitStaleHeadRejectedBeforePosting: a head_sha that is not the
// PR's current head is invalid_argument naming the current head, and nothing
// is posted or deleted (bead pg2-qr4sr).
func TestReviewSubmitStaleHeadRejectedBeforePosting(t *testing.T) {
	gh := &fakeGH{pr: &api.PR{HeadSHA: "cafef00d"}, pendingData: &github.PendingReviewData{HeadSHA: "cafef00d", Reviews: reviewsOf(postedAsBackend("old", "s", "c"))}}
	req := baseSubmitReq() // head_sha deadbeef
	req.SupersedePending = true
	_, err := New(gh).SubmitReview(context.Background(), req)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	for _, want := range []string{"head moved", "deadbeef", "cafef00d", "nothing was posted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q must mention %q", err, want)
		}
	}
	if len(gh.posts) != 0 || len(gh.deleted) != 0 || len(gh.ops) != 0 {
		t.Errorf("stale head must look up, post and delete nothing; posts=%+v deleted=%v ops=%v", gh.posts, gh.deleted, gh.ops)
	}
}

func TestReviewSubmitMatchingHeadPostsCaseInsensitive(t *testing.T) {
	gh := &fakeGH{pr: &api.PR{HeadSHA: "DEADBEEF"}}
	if _, err := New(gh).SubmitReview(context.Background(), baseSubmitReq()); err != nil {
		t.Fatalf("matching head must post; got %v", err)
	}
	if len(gh.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(gh.posts))
	}
}

// TestReviewSubmitHeadLookupFailures: the pre-check's own failures never post.
func TestReviewSubmitHeadLookupFailures(t *testing.T) {
	cases := map[string]struct {
		gh   *fakeGH
		want error
	}{
		"not found":   {&fakeGH{getPRErr: errors.New("gh: Not Found (HTTP 404)")}, scriptout.ErrNotFound},
		"unavailable": {&fakeGH{getPRErr: errors.New("gh: Bad Gateway (HTTP 502)")}, scriptout.ErrUnavailable},
		"no head":     {&fakeGH{pr: &api.PR{}}, scriptout.ErrUnavailable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(c.gh).SubmitReview(context.Background(), baseSubmitReq())
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if len(c.gh.posts) != 0 {
				t.Errorf("nothing may be posted; got %+v", c.gh.posts)
			}
		})
	}
}

const pendingExists422 = `github: post review: gh api failed: {"message":"Unprocessable Entity","errors":["User can only have one pending review per pull request"]} gh: Unprocessable Entity (HTTP 422)`

// TestReviewSubmitPendingExistsDistinguishableFromOtherRejections: both are
// invalid_argument (the closed INV-ERR-1 set), but the message tells the
// causes apart.
func TestReviewSubmitPendingExistsDistinguishableFromOtherRejections(t *testing.T) {
	_, pendErr := New(&fakeGH{pr: liveHeadPR(), postErr: errors.New(pendingExists422)}).
		SubmitReview(context.Background(), baseSubmitReq())
	_, otherErr := New(&fakeGH{pr: liveHeadPR(), postErr: errors.New("gh: Validation Failed: line must be part of the diff (HTTP 422)")}).
		SubmitReview(context.Background(), baseSubmitReq())
	for _, e := range []error{pendErr, otherErr} {
		if !errors.Is(e, scriptout.ErrInvalidArgument) {
			t.Fatalf("err = %v, want invalid_argument", e)
		}
	}
	if !strings.Contains(pendErr.Error(), "pending review already exists") || !strings.Contains(pendErr.Error(), "supersede_pending") {
		t.Errorf("pending-exists error must say so and name the remedy: %q", pendErr)
	}
	if strings.Contains(otherErr.Error(), "pending review already exists") || strings.Contains(otherErr.Error(), "head moved") {
		t.Errorf("a non-pending 422 must not claim pending-exists or head-moved: %q", otherErr)
	}
	if !strings.Contains(otherErr.Error(), "rejected the review") {
		t.Errorf("other 422 should be described as a rejected review: %q", otherErr)
	}
}
