package internal

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	// searchFn/rateLimit/rateLimitErr back the List (bead pg2-2j5ac.28.1)
	// seam. rateLimit defaults to a comfortably-above-reserve value (via
	// rateLimitOrDefault below) so existing tests that never set it don't
	// need to know about rate-limit protection at all.
	searchFn     func(ctx context.Context, query string) ([]api.PR, error)
	rateLimit    int
	rateLimitErr error
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

func (f *fakeGH) SearchPRs(ctx context.Context, query string) ([]api.PR, error) {
	if f.searchFn != nil {
		return f.searchFn(ctx, query)
	}
	return nil, nil
}

// rateLimitOrDefaultReserve is fakeGH's own zero-value convenience: a
// fakeGH that never sets rateLimit answers comfortably above
// defaultRateReservePoints, so every EXISTING test in this file (written
// before List/rate-limit protection existed) keeps working unchanged.
const rateLimitOrDefaultReserve = defaultRateReservePoints + 1000

func (f *fakeGH) RateLimitRemaining(ctx context.Context) (int, error) {
	if f.rateLimitErr != nil {
		return 0, f.rateLimitErr
	}
	if f.rateLimit == 0 {
		return rateLimitOrDefaultReserve, nil
	}
	return f.rateLimit, nil
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

// TestBackend_Show_PopulatesFreshAsOfAndNeverStale proves Show's response
// carries a usable as-of/stale pair (bead pg2-681xo, INV-ASOF-1/2 parity):
// AsOf is a parseable RFC3339 timestamp taken at (or after) the call, and
// Stale is false — this backend never serves a cached copy of GitHub's PR
// facts, so every read is fresh by construction.
func TestBackend_Show_PopulatesFreshAsOfAndNeverStale(t *testing.T) {
	before := time.Now().UTC()
	gh := &fakeGH{
		pr: &api.PR{Repo: "owner/repo", Number: 1, Title: "T", State: "open"},
	}
	b := newTestBackend(t, gh)

	got, err := b.Show(context.Background(), "owner/repo#1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	after := time.Now().UTC()

	if got.Stale {
		t.Fatalf("Stale = true, want false: this backend has no local cache of GitHub PR facts")
	}
	asOf, err := time.Parse(time.RFC3339, got.AsOf)
	if err != nil {
		t.Fatalf("AsOf %q is not a valid RFC3339 timestamp: %v", got.AsOf, err)
	}
	// time.RFC3339 truncates to whole seconds, so asOf can read up to ~1s
	// earlier than the sub-second "before" captured just before the call.
	if asOf.Before(before.Truncate(time.Second)) || asOf.After(after) {
		t.Fatalf("AsOf = %v, want it between %v and %v (the call's own window)", asOf, before, after)
	}
}

func TestBackend_Show_InvalidID(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})
	_, err := b.Show(context.Background(), "not-a-valid-id")
	if err == nil {
		t.Fatal("expected an error for a malformed id")
	}
	// A malformed id is the CALLER's mistake, not this backend being
	// unhealthy (INV-ERR-2; bug pg2-r9iok) — it must not share
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
// than falling through to codeForError's "unavailable" fallback (INV-ERR-2; bug pg2-r9iok).
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
	// assert both values round-trip into the response (interfaces.md's pr op catalog) —
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
	// backend being unhealthy (INV-ERR-2; bug pg2-r9iok).
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_FeedbackSet_UnknownCommentIsNotFound(t *testing.T) {
	// A commentID that no longer exists on the PR is a well-formed
	// not_found response, not a broken call (INV-ERR-2).
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

// TestBackend_Categorize_RejectsValueOutsideVocabulary is finding A20's
// required proof: Categorize previously accepted any string unvalidated.
func TestBackend_Categorize_RejectsValueOutsideVocabulary(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})
	_, err := b.Categorize(context.Background(), "owner/repo#1", "not-a-real-category")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	// And the rejected write must not have landed in the store.
	st, getErr := b.store.Get("owner/repo#1")
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if st.Category != "" {
		t.Fatalf("Category = %q, want empty — a rejected categorize must not partially write", st.Category)
	}
}

// TestBackend_Categorize_EmptyCategoryIsDistinctNonSilentError proves an
// empty category is rejected with its own error rather than either (a)
// silently succeeding as a store.SetCategory "delete" (the pre-fix
// behavior — store.go's `omitempty` JSON tag makes an empty category
// indistinguishable on disk from "never categorized") or (b) merely
// reusing the generic vocabulary-membership error message.
func TestBackend_Categorize_EmptyCategoryIsDistinctNonSilentError(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})
	// First set a real category...
	if _, err := b.Categorize(context.Background(), "owner/repo#1", "focus"); err != nil {
		t.Fatalf("Categorize(focus): %v", err)
	}
	// ...then attempt to clear it via an empty string.
	_, err := b.Categorize(context.Background(), "owner/repo#1", "")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want a message distinctly about the empty category, not just the generic vocabulary-membership message", err)
	}
	// The earlier valid write must survive the rejected empty-category call.
	st, getErr := b.store.Get("owner/repo#1")
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if st.Category != "focus" {
		t.Fatalf("Category = %q, want focus — an empty-category call must not silently delete the existing category", st.Category)
	}
}

// TestBackend_Categorize_AcceptsEveryVocabularyValue is the positive
// counterpart: every declared Vocabulary value must be accepted (a
// too-strict validator would be exactly as broken as no validator).
func TestBackend_Categorize_AcceptsEveryVocabularyValue(t *testing.T) {
	for _, cat := range Vocabulary {
		b := newTestBackend(t, &fakeGH{})
		if _, err := b.Categorize(context.Background(), "owner/repo#1", cat); err != nil {
			t.Fatalf("Categorize(%q): %v", cat, err)
		}
	}
}

func TestVocabulary_NonEmpty(t *testing.T) {
	if len(Vocabulary) == 0 {
		t.Fatal("Vocabulary must be non-empty — it backs the capabilities op's declared category vocabulary (interfaces.md's vocabulary note)")
	}
}

// ----------------------------------------------------------------------
// List (bead pg2-2j5ac.28.1)
// ----------------------------------------------------------------------

func TestBackend_List_SingleExpr(t *testing.T) {
	gh := &fakeGH{searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
		if query != "is:open author:@me" {
			t.Fatalf("query = %q", query)
		}
		return []api.PR{{Repo: "owner/repo", Number: 1, Title: "a", State: "open"}}, nil
	}}
	b := newTestBackend(t, gh)

	got, err := b.List(context.Background(), []string{"is:open author:@me"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "owner/repo#1" {
		t.Fatalf("Entities = %+v", got.Entities)
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "owner/repo#1" {
		t.Fatalf("PresentIDs = %+v", got.PresentIDs)
	}
	if got.Cursor != nil {
		t.Fatalf("Cursor = %v, want nil (always null)", got.Cursor)
	}
}

func TestBackend_List_MultipleExpressions_UnionDeduplicated(t *testing.T) {
	calls := 0
	gh := &fakeGH{searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
		calls++
		if query == "is:open author:@me" {
			return []api.PR{{Repo: "owner/repo", Number: 1}, {Repo: "owner/repo", Number: 2}}, nil
		}
		return []api.PR{{Repo: "owner/repo", Number: 2}}, nil
	}}
	b := newTestBackend(t, gh)

	got, err := b.List(context.Background(), []string{"is:open author:@me", "is:open review-requested:@me"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one per expression)", calls)
	}
	if len(got.Entities) != 2 {
		t.Fatalf("Entities = %+v, want exactly 2 after dedup by id (owner/repo#2 appeared in both)", got.Entities)
	}
}

func TestBackend_List_IDsOnly_OmitsEntities(t *testing.T) {
	gh := &fakeGH{searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
		return []api.PR{{Repo: "owner/repo", Number: 1}}, nil
	}}
	b := newTestBackend(t, gh)

	got, err := b.List(context.Background(), []string{"is:open"}, true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 0 {
		t.Fatalf("Entities = %+v, want empty when ids_only is true", got.Entities)
	}
	if len(got.PresentIDs) != 1 {
		t.Fatalf("PresentIDs = %+v, want present_ids populated regardless of ids_only", got.PresentIDs)
	}
}

func TestBackend_List_SearchError_Classified(t *testing.T) {
	gh := &fakeGH{searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
		return nil, github.ErrGHAuthInvalid
	}}
	b := newTestBackend(t, gh)

	_, err := b.List(context.Background(), []string{"is:open"}, false)
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

// TestBackend_List_RateLimitBelowReserve_IsUnavailable is the design
// "Rate protection" bullet's own regression proof: a rate-limit
// remainder below the reserve answers unavailable BEFORE any search is
// even attempted.
func TestBackend_List_RateLimitBelowReserve_IsUnavailable(t *testing.T) {
	searchCalled := false
	gh := &fakeGH{
		rateLimit: 500,
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			searchCalled = true
			return nil, nil
		},
	}
	b := newTestBackend(t, gh)

	ctx := scriptout.WithConfig(context.Background(), []byte(`{"rate_reserve_points":1000}`))
	_, err := b.List(ctx, []string{"is:open"}, false)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	if searchCalled {
		t.Fatal("SearchPRs must not be called once the rate-limit check fails")
	}
}

func TestBackend_List_RateLimitAboveReserve_Succeeds(t *testing.T) {
	gh := &fakeGH{
		rateLimit: 2000,
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{{Repo: "owner/repo", Number: 1}}, nil
		},
	}
	b := newTestBackend(t, gh)

	ctx := scriptout.WithConfig(context.Background(), []byte(`{"rate_reserve_points":1000}`))
	got, err := b.List(ctx, []string{"is:open"}, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("Entities = %+v", got.Entities)
	}
}

// TestRateReservePoints_DefaultsWhenAbsent proves rateReservePoints
// falls back to defaultRateReservePoints (1000) when no
// config, or a config with no rate_reserve_points key, is given.
func TestRateReservePoints_DefaultsWhenAbsent(t *testing.T) {
	if got := rateReservePoints(nil); got != defaultRateReservePoints {
		t.Fatalf("rateReservePoints(nil) = %d, want %d", got, defaultRateReservePoints)
	}
	if got := rateReservePoints([]byte(`{}`)); got != defaultRateReservePoints {
		t.Fatalf("rateReservePoints({}) = %d, want %d", got, defaultRateReservePoints)
	}
}

func TestRateReservePoints_ReadsConfiguredValue(t *testing.T) {
	if got := rateReservePoints([]byte(`{"rate_reserve_points":250}`)); got != 250 {
		t.Fatalf("rateReservePoints = %d, want 250", got)
	}
}
