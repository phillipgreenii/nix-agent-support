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

func (f *fakeGH) FindPendingReview(ctx context.Context, repo string, number int) (int64, bool, error) {
	return f.pendingID, f.pendingFound, f.findErr
}

func (f *fakeGH) DeleteReview(ctx context.Context, repo string, number int, reviewID int64) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, reviewID)
	return nil
}

func (f *fakeGH) PostPendingReview(ctx context.Context, repo string, number int, commitID, body string, comments []github.ReviewSubmitComment) (*api.Review, error) {
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

func TestReviewSubmitPendingOnly(t *testing.T) {
	gh := &fakeGH{}
	res, err := New(gh).SubmitReview(context.Background(), baseSubmitReq())
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if res.State != "pending" || res.ReviewID != "PRR_node" || res.HeadSHA != "deadbeef" || res.AsOf == "" {
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

func TestReviewSubmitHeadMovedIsInvalidArgument(t *testing.T) {
	cases := map[string]error{
		"head moved":      errors.New("github: post review: gh api failed: gh: Unprocessable Entity (HTTP 422)"),
		"non-head 422":    errors.New("github: post review: gh: Validation Failed: line must be part of the diff (HTTP 422)"),
		"upper-case 422":  errors.New("HTTP 422: Unprocessable Entity"),
		"wrapped message": errors.New("github: post review: exit 1: stderr: gh: Unprocessable Entity (HTTP 422)"),
	}
	for name, postErr := range cases {
		t.Run(name, func(t *testing.T) {
			gh := &fakeGH{postErr: postErr}
			_, err := New(gh).SubmitReview(context.Background(), baseSubmitReq())
			if !errors.Is(err, scriptout.ErrInvalidArgument) {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
		})
	}
}

func TestReviewSubmitErrorTaxonomy(t *testing.T) {
	gh := &fakeGH{postErr: errors.New("gh: Not Found (HTTP 404)")}
	if _, err := New(gh).SubmitReview(context.Background(), baseSubmitReq()); !errors.Is(err, scriptout.ErrNotFound) {
		t.Errorf("404: err = %v, want not_found", err)
	}
	gh = &fakeGH{postErr: errors.Join(github.ErrGHAuthInvalid, errors.New("gh: Forbidden (HTTP 403)"))}
	if _, err := New(gh).SubmitReview(context.Background(), baseSubmitReq()); !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Errorf("403: err = %v, want unauthenticated", err)
	}
	gh = &fakeGH{postErr: errors.New("gh: Bad Gateway (HTTP 502)")}
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
			gh := &fakeGH{}
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
	gh := &fakeGH{}
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

func TestReviewSubmitSupersedeDeletesPriorPending(t *testing.T) {
	gh := &fakeGH{pendingID: 777, pendingFound: true}
	req := baseSubmitReq()
	req.SupersedePending = true
	res, err := New(gh).SubmitReview(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if len(gh.deleted) != 1 || gh.deleted[0] != 777 {
		t.Fatalf("deleted = %v, want [777]", gh.deleted)
	}
	if res.Supersede == nil || !res.Supersede.Attempted || !res.Supersede.Deleted || res.Supersede.Error != "" {
		t.Fatalf("supersede outcome = %+v, want attempted+deleted", res.Supersede)
	}
	if len(gh.posts) != 1 {
		t.Errorf("review must still post after supersede; posts = %d", len(gh.posts))
	}
}

func TestReviewSubmitSupersedeNothingToDelete(t *testing.T) {
	gh := &fakeGH{}
	req := baseSubmitReq()
	req.SupersedePending = true
	res, err := New(gh).SubmitReview(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	if res.Supersede == nil || res.Supersede.Attempted || res.Supersede.Deleted {
		t.Fatalf("supersede outcome = %+v, want zero-valued (nothing attempted)", res.Supersede)
	}
}

// TestReviewSubmitSupersedeFailureReportedInBody: a failed delete (or lookup)
// never fails the op; the review posts and the outcome carries the error.
func TestReviewSubmitSupersedeFailureReportedInBody(t *testing.T) {
	for name, gh := range map[string]*fakeGH{
		"delete fails": {pendingID: 5, pendingFound: true, deleteErr: errors.New("gh: Forbidden (HTTP 403)")},
		"lookup fails": {findErr: errors.New("gh: Bad Gateway (HTTP 502)")},
	} {
		t.Run(name, func(t *testing.T) {
			req := baseSubmitReq()
			req.SupersedePending = true
			res, err := New(gh).SubmitReview(context.Background(), req)
			if err != nil {
				t.Fatalf("a failed supersede must not fail the op; got %v", err)
			}
			if res.Supersede == nil || !res.Supersede.Attempted || res.Supersede.Deleted || res.Supersede.Error == "" {
				t.Fatalf("supersede outcome = %+v, want attempted, not deleted, error set", res.Supersede)
			}
			if len(gh.posts) != 1 || res.ReviewID == "" {
				t.Errorf("review must still post; posts=%d result=%+v", len(gh.posts), res)
			}
		})
	}
}
