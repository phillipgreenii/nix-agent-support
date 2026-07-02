package reviewsink

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/marker"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewstage"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
)

// fakeReviewer is an in-memory VCSReviewer double. It records PostReview calls
// and serves canned pending-review / existing-comment / head-SHA answers.
type fakeReviewer struct {
	hasPending    bool
	hasPendingErr error

	existing []api.Comment // returned by ListComments

	headSHA string // returned by GetPR head (empty ⇒ no PR)

	posts   []postCall
	postErr error
}

type postCall struct {
	repo     string
	pr       int
	body     string
	comments []api.Comment
}

func (f *fakeReviewer) HasPendingReviewByViewer(_ context.Context, _ string, _ int) (bool, error) {
	return f.hasPending, f.hasPendingErr
}

func (f *fakeReviewer) ListComments(_ context.Context, _ string, _ int) ([]api.Comment, error) {
	return f.existing, nil
}

func (f *fakeReviewer) PostReview(_ context.Context, repo string, pr int, body string, comments []api.Comment) (*api.Review, error) {
	f.posts = append(f.posts, postCall{repo: repo, pr: pr, body: body, comments: comments})
	if f.postErr != nil {
		return nil, f.postErr
	}
	return &api.Review{ID: "RV_1", State: "pending", Body: body}, nil
}

func (f *fakeReviewer) GetPR(_ context.Context, repo string, number int) (*api.PR, error) {
	if f.headSHA == "" {
		return nil, errors.New("not found")
	}
	return &api.PR{Repo: repo, Number: number, HeadSHA: f.headSHA}, nil
}

func stageTeamDraft(t *testing.T, dir string, d *reviewstage.Draft) {
	t.Helper()
	if _, err := reviewstage.Save(dir, d); err != nil {
		t.Fatalf("stage draft: %v", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
}

func TestApplyPendingReview_PostsPendingReview(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stageTeamDraft(t, dir, &reviewstage.Draft{
		Repo: "o/r", PR: 7, Body: "overall LGTM with nits",
		Comments: []api.Comment{{Path: "a.go", Line: 3, Body: "rename x"}},
	})
	rv := &fakeReviewer{headSHA: "h1"}
	result := reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "team", HeadSHA: "h1", BeadID: "dr-1"}

	if err := ApplyPendingReview(ctx, rv, dir, result, discardLogger()); err != nil {
		t.Fatalf("ApplyPendingReview: %v", err)
	}
	if len(rv.posts) != 1 {
		t.Fatalf("PostReview should be called once, got %d", len(rv.posts))
	}
	p := rv.posts[0]
	if p.repo != "o/r" || p.pr != 7 {
		t.Fatalf("posted to wrong PR: %s#%d", p.repo, p.pr)
	}
	// Marker MUST be present on body and each comment.
	if !marker.IsOurs(p.body) {
		t.Errorf("body must carry the agent marker, got %q", p.body)
	}
	if len(p.comments) != 1 || !marker.IsOurs(p.comments[0].Body) {
		t.Errorf("comment must carry the agent marker, got %+v", p.comments)
	}
	// Draft + Result cleared on a successful post (idempotency).
	if _, err := reviewstage.Load(dir, "o/r", 7); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Draft must be cleared after a successful post, err=%v", err)
	}
	if _, err := reviewstage.LoadResult(dir, "o/r", 7); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Result must be cleared after a successful post, err=%v", err)
	}
}

func TestApplyPendingReview_SkipsWhenPendingExists(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stageTeamDraft(t, dir, &reviewstage.Draft{Repo: "o/r", PR: 7, Body: "note"})
	if _, err := reviewstage.SaveResult(dir, &reviewstage.Result{Repo: "o/r", PR: 7, HeadSHA: "h1"}); err != nil {
		t.Fatalf("save result: %v", err)
	}
	rv := &fakeReviewer{hasPending: true, headSHA: "h1"}
	result := reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "team", HeadSHA: "h1"}

	if err := ApplyPendingReview(ctx, rv, dir, result, discardLogger()); err != nil {
		t.Fatalf("ApplyPendingReview: %v", err)
	}
	if len(rv.posts) != 0 {
		t.Fatalf("PostReview must NOT be called when a pending review already exists, got %d", len(rv.posts))
	}
	// Draft + Result must be LEFT intact on skip (do not clobber human edits).
	if _, err := reviewstage.Load(dir, "o/r", 7); err != nil {
		t.Errorf("Draft must be left intact on skip, err=%v", err)
	}
	if _, err := reviewstage.LoadResult(dir, "o/r", 7); err != nil {
		t.Errorf("Result must be left intact on skip, err=%v", err)
	}
}

func TestApplyPendingReview_DedupsExistingComments(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// A previously-posted, marker-stamped comment already exists on the PR.
	existingBody := marker.Stamp("rename x")
	stageTeamDraft(t, dir, &reviewstage.Draft{
		Repo: "o/r", PR: 7,
		Comments: []api.Comment{
			{Path: "a.go", Line: 3, Body: "rename x"},     // dup of existing
			{Path: "b.go", Line: 9, Body: "handle error"}, // new
		},
	})
	rv := &fakeReviewer{
		headSHA:  "h1",
		existing: []api.Comment{{Path: "a.go", Line: 3, Body: existingBody}},
	}
	result := reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "team", HeadSHA: "h1"}

	if err := ApplyPendingReview(ctx, rv, dir, result, discardLogger()); err != nil {
		t.Fatalf("ApplyPendingReview: %v", err)
	}
	if len(rv.posts) != 1 {
		t.Fatalf("PostReview should be called once, got %d", len(rv.posts))
	}
	if len(rv.posts[0].comments) != 1 {
		t.Fatalf("only the NEW comment should post (dedup), got %d comments", len(rv.posts[0].comments))
	}
	if !strings.Contains(rv.posts[0].comments[0].Body, "handle error") {
		t.Fatalf("expected the new comment to survive dedup, got %q", rv.posts[0].comments[0].Body)
	}
}

func TestApplyPendingReview_NotProduced_NoOp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir() // no staged Draft
	rv := &fakeReviewer{headSHA: "h1"}
	result := reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "team", HeadSHA: "h1"}

	if err := ApplyPendingReview(ctx, rv, dir, result, discardLogger()); err != nil {
		t.Fatalf("ApplyPendingReview must no-op (nil err) when no Draft is staged, got %v", err)
	}
	if len(rv.posts) != 0 {
		t.Fatalf("no PostReview when the review was not produced, got %d", len(rv.posts))
	}
}

func TestApplyPendingReview_StaleHead_WarnsButPosts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stageTeamDraft(t, dir, &reviewstage.Draft{Repo: "o/r", PR: 7, Body: "note"})
	rv := &fakeReviewer{headSHA: "h2"} // live head advanced past the reviewed SHA
	result := reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "team", HeadSHA: "h1"}

	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if err := ApplyPendingReview(ctx, rv, dir, result, log); err != nil {
		t.Fatalf("stale head must WARN, not block: %v", err)
	}
	if len(rv.posts) != 1 {
		t.Fatalf("stale head must still post (warn, not block), got %d posts", len(rv.posts))
	}
	if !strings.Contains(logbuf.String(), "stale") {
		t.Fatalf("expected a stale-head WARN log, got %q", logbuf.String())
	}
}

func TestApplyPendingReview_PostFails_LeavesDraft(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stageTeamDraft(t, dir, &reviewstage.Draft{Repo: "o/r", PR: 7, Body: "note"})
	rv := &fakeReviewer{headSHA: "h1", postErr: errors.New("gh 500")}
	result := reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "team", HeadSHA: "h1"}

	if err := ApplyPendingReview(ctx, rv, dir, result, discardLogger()); err == nil {
		t.Fatalf("ApplyPendingReview must return the PostReview error")
	}
	// Draft must survive a failed post so a retry can re-post.
	if _, err := reviewstage.Load(dir, "o/r", 7); err != nil {
		t.Errorf("Draft must be left intact when the post fails, err=%v", err)
	}
}

func TestApplyPendingReview_HasPendingError_Propagates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stageTeamDraft(t, dir, &reviewstage.Draft{Repo: "o/r", PR: 7, Body: "note"})
	rv := &fakeReviewer{hasPendingErr: errors.New("gh graphql boom")}
	result := reviewstage.Result{Repo: "o/r", PR: 7, Ownership: "team", HeadSHA: "h1"}

	if err := ApplyPendingReview(ctx, rv, dir, result, discardLogger()); err == nil {
		t.Fatalf("must propagate the HasPendingReviewByViewer error (fail closed)")
	}
	if len(rv.posts) != 0 {
		t.Fatalf("must NOT post when the pending-detection query failed")
	}
}
