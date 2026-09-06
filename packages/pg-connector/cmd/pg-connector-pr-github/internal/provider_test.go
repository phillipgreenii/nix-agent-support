package internal

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeGH is a minimal ghProvider double so provider.go's tests never spawn
// a real `gh` subprocess.
type fakeGH struct {
	pr           *api.PR
	comments     []api.Comment
	reviews      []api.Review
	getPRErr     error
	commentsErr  error
	reviewsErr   error
	checkAuthErr error
}

func (f *fakeGH) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	if f.getPRErr != nil {
		return nil, f.getPRErr
	}
	return f.pr, nil
}

func (f *fakeGH) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	return f.comments, nil
}

func (f *fakeGH) ListReviews(ctx context.Context, repo string, number int) ([]api.Review, error) {
	if f.reviewsErr != nil {
		return nil, f.reviewsErr
	}
	return f.reviews, nil
}

func (f *fakeGH) CheckAuth(ctx context.Context) error {
	return f.checkAuthErr
}

func newTestBackend(t *testing.T, gh *fakeGH) *Backend {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "store.json"))
	return New(gh, store)
}

func TestBackend_Show_MapsGHDataToSchemaPR(t *testing.T) {
	gh := &fakeGH{
		pr: &api.PR{
			Repo: "owner/repo", Number: 42, Title: "Add feature", State: "open",
			Branch: "feature", Base: "main", Author: "octocat",
			URL: "https://example.invalid/owner/repo/pull/42",
		},
		comments: []api.Comment{
			{ID: "c1", Author: "alice", Body: "top-level"},
			// ReviewID is a realistic GraphQL node-id string (not a hand-typed
			// decimal like "99"), matching Review.ID below exactly — proving the
			// join key types actually agree rather than accidentally overlapping
			// [bug pg2-flaes].
			{ID: "c2", Author: "bob", Body: "inline", Path: "main.go", Line: 10, ThreadID: "t2", ReviewID: "PRR_kwDOKtdWE88AAAABL3blsA"},
		},
		reviews: []api.Review{
			{ID: "PRR_kwDOKtdWE88AAAABL3blsA", Author: "bob", State: "CHANGES_REQUESTED", Body: "please fix"},
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.Show(context.Background(), "owner/repo#42")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != "owner/repo#42" || got.Repo != "owner/repo" || got.Number != 42 || got.Title != "Add feature" {
		t.Fatalf("PR identity mismatch: %+v", got)
	}
	if len(got.Comments) != 1 || got.Comments[0].ID != "c1" {
		t.Fatalf("top-level comments = %+v, want just c1", got.Comments)
	}
	if len(got.Reviews) != 1 || len(got.Reviews[0].Comments) != 1 || got.Reviews[0].Comments[0].ID != "c2" {
		t.Fatalf("review-nested comments mismatch: %+v", got.Reviews)
	}
	// Never-written dispositions default to "open".
	if got.Comments[0].Disposition != schema.DispositionOpen || got.Reviews[0].Comments[0].Disposition != schema.DispositionOpen {
		t.Fatalf("default disposition should be open: %+v / %+v", got.Comments[0], got.Reviews[0].Comments[0])
	}
}

func TestBackend_Show_InvalidID(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})
	_, err := b.Show(context.Background(), "not-a-valid-id")
	if err == nil {
		t.Fatal("expected an error for a malformed id")
	}
	// A malformed id is the CALLER's mistake, not this backend being
	// unhealthy [design: §4.2, bug pg2-r9iok] — it must not share
	// ErrUnavailable's "this backend cannot currently be used" meaning.
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Show_AuthFailureMapsToUnauthenticated(t *testing.T) {
	gh := &fakeGH{getPRErr: github.ErrGHAuthInvalid}
	b := newTestBackend(t, gh)
	_, err := b.Show(context.Background(), "owner/repo#1")
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

// TestBackend_Show_NonexistentPR_NotFound proves the GraphQL "could not
// resolve" phrasing gh actually returns for a nonexistent PR number
// (verified empirically against real `gh` 2.99.0: `gh pr view 999999999
// --repo octocat/Hello-World` prints "GraphQL: Could not resolve to a
// PullRequest with the number of 999999999. (repository.pullRequest)",
// exit 1) is now reachable as not_found through classifyGHError, rather
// than falling through to codeForError's "unavailable" fallback [design:
// §4.5, bug pg2-r9iok].
func TestBackend_Show_NonexistentPR_NotFound(t *testing.T) {
	gh := &fakeGH{getPRErr: errors.New("gh pr view 999999999: exit status 1: GraphQL: Could not resolve to a PullRequest with the number of 999999999. (repository.pullRequest)")}
	b := newTestBackend(t, gh)
	_, err := b.Show(context.Background(), "owner/repo#999999999")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
	if errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, must NOT also be ErrUnavailable — a not_found answer must not share a code with a failure", err)
	}
}

// TestBackend_Show_NonexistentComments_NotFound covers the REST-404
// phrasing (verified empirically: `gh api
// repos/octocat/Hello-World/issues/999999999/comments` prints `gh: Not
// Found (HTTP 404)` on stderr, exit 1) that ListComments' underlying `gh
// api .../comments` call returns for a deleted/nonexistent PR.
func TestBackend_Show_NonexistentComments_NotFound(t *testing.T) {
	gh := &fakeGH{
		pr:          &api.PR{Repo: "owner/repo", Number: 1, Title: "T", State: "open"},
		commentsErr: errors.New("gh api repos/owner/repo/issues/1/comments: exit status 1: gh: Not Found (HTTP 404)"),
	}
	b := newTestBackend(t, gh)
	_, err := b.Show(context.Background(), "owner/repo#1")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

// TestBackend_Show_GenuineGHFailure_PassesThroughUnclassified proves a
// real gh failure unrelated to auth or not-found (nothing in its message
// matches isGHNotFound's patterns, including the "executable file not
// found in $PATH" false-positive risk bd.go's own classifyBDErrorMessage
// warns about for its own "not found" substring) passes through
// classifyGHError unwrapped, exactly as before this fix — the fix narrows
// not_found detection to genuine "doesn't exist" phrasing, it does not
// turn every gh error into not_found. codeForError's own wire-serialization
// fallback (pkg/scriptout, unexported) is what maps this unwrapped error to
// "unavailable" on the wire; at the Go level classifyGHError's contract is
// simply "propagate unchanged," which errors.Is against the original error
// verifies directly.
func TestBackend_Show_GenuineGHFailure_PassesThroughUnclassified(t *testing.T) {
	wantErr := errors.New(`gh pr view 1: exec: "gh": executable file not found in $PATH`)
	gh := &fakeGH{getPRErr: wantErr}
	b := newTestBackend(t, gh)
	_, err := b.Show(context.Background(), "owner/repo#1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want errors.Is(err, wantErr)", err)
	}
	for _, sentinel := range []error{scriptout.ErrNotFound, scriptout.ErrInvalidArgument, scriptout.ErrUnauthenticated} {
		if errors.Is(err, sentinel) {
			t.Fatalf("err = %v, must NOT be classified as %v", err, sentinel)
		}
	}
}

func TestBackend_RoundTrip_CategorizeAndFeedbackSetThenShow(t *testing.T) {
	// This is the packet's required round-trip test: write a category via
	// categorize and a disposition via feedback_set, then call show, and
	// assert both values round-trip into the response [design: §2, §6.1] —
	// proving the store-and-merge behavior, not just that the store accepts
	// writes.
	gh := &fakeGH{
		pr: &api.PR{Repo: "owner/repo", Number: 7, Title: "T", State: "open"},
		comments: []api.Comment{
			{ID: "c1", Author: "alice", Body: "please fix this"},
		},
	}
	b := newTestBackend(t, gh)
	id := "owner/repo#7"

	catResult, err := b.Categorize(context.Background(), id, "focus")
	if err != nil {
		t.Fatalf("Categorize: %v", err)
	}
	if catResult.Category != "focus" {
		t.Fatalf("CategorizeResult.Category = %q, want focus", catResult.Category)
	}

	fbResult, err := b.FeedbackSet(context.Background(), id, "c1", schema.DispositionWillFix)
	if err != nil {
		t.Fatalf("FeedbackSet: %v", err)
	}
	if fbResult.Disposition != schema.DispositionWillFix {
		t.Fatalf("FeedbackSetResult.Disposition = %q, want will-fix", fbResult.Disposition)
	}

	pr, err := b.Show(context.Background(), id)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if pr.Category != "focus" {
		t.Fatalf("Show did not reflect the categorize write: Category = %q", pr.Category)
	}
	if len(pr.Comments) != 1 || pr.Comments[0].Disposition != schema.DispositionWillFix {
		t.Fatalf("Show did not reflect the feedback_set write: Comments = %+v", pr.Comments)
	}
}

func TestBackend_FeedbackSet_UnknownDispositionRejected(t *testing.T) {
	gh := &fakeGH{comments: []api.Comment{{ID: "c1"}}}
	b := newTestBackend(t, gh)
	_, err := b.FeedbackSet(context.Background(), "owner/repo#1", "c1", schema.Disposition("bogus"))
	if err == nil {
		t.Fatal("expected an error for an invalid disposition")
	}
	// An invalid disposition value is the CALLER's mistake, not this
	// backend being unhealthy [design: §4.2, bug pg2-r9iok].
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_FeedbackSet_UnknownCommentIsNotFound(t *testing.T) {
	// A commentID that no longer exists on the PR is a well-formed
	// not_found response, not a broken call [design: §4.5, §6.1].
	gh := &fakeGH{comments: []api.Comment{{ID: "c1"}}}
	b := newTestBackend(t, gh)
	_, err := b.FeedbackSet(context.Background(), "owner/repo#1", "does-not-exist", schema.DispositionOpen)
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(err, ErrNotFound)", err)
	}
}

func TestBackend_CheckAuth_DelegatesToGHProvider(t *testing.T) {
	wantErr := errors.New("no token")
	b := newTestBackend(t, &fakeGH{checkAuthErr: wantErr})
	if err := b.CheckAuth(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("CheckAuth() = %v, want %v", err, wantErr)
	}
	b2 := newTestBackend(t, &fakeGH{})
	if err := b2.CheckAuth(context.Background()); err != nil {
		t.Fatalf("CheckAuth() = %v, want nil", err)
	}
}

func TestBackend_Categorize_InvalidID_IsInvalidArgument(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})
	_, err := b.Categorize(context.Background(), "not-a-valid-id", "focus")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestParsePRID_RoundTrips(t *testing.T) {
	repo, number, err := parsePRID(formatPRID("owner/repo", 42))
	if err != nil {
		t.Fatalf("parsePRID: %v", err)
	}
	if repo != "owner/repo" || number != 42 {
		t.Fatalf("got repo=%q number=%d", repo, number)
	}
}

func TestParsePRID_RejectsMalformed(t *testing.T) {
	for _, id := range []string{"", "no-hash", "owner-only#1", "owner/repo#", "owner/repo#abc", "owner/repo#0"} {
		if _, _, err := parsePRID(id); err == nil {
			t.Errorf("parsePRID(%q) should have failed", id)
		}
	}
}

func TestVocabulary_NonEmpty(t *testing.T) {
	if len(Vocabulary) == 0 {
		t.Fatal("Vocabulary must be non-empty — it backs the capabilities op's declared category vocabulary [design: §4.3, §6.1]")
	}
}
