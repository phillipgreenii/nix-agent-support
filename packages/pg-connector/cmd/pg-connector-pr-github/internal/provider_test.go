package internal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/eventlog"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// fakeGH is a minimal ghProvider double so provider.go's tests never spawn
// a real `gh` subprocess.
type fakeGH struct {
	pr          *api.PR
	comments    []api.Comment
	reviews     []api.Review
	getPRErr    error
	commentsErr error
	// commentsFn answers ListComments per repo/number (list_activity's
	// pr.commented kind); it takes precedence over comments/commentsErr.
	commentsFn func(ctx context.Context, repo string, number int) ([]api.Comment, error)
	// commentsReportFn/reviewsReport let a test supply the truncation reports
	// Show copies onto the response; unset, both reports are zero.
	commentsReportFn func(ctx context.Context, repo string, number int) (*api.CommentsResult, error)
	reviewsReport    *api.ReviewsResult
	reviewsErr       error
	checkAuthErr     error

	// getPRFn lets a test answer GetPR per repo/number instead of the
	// single fixed pr/getPRErr above — needed once a test has more than
	// one distinct candidate PR in flight at a time (bead pg2-zutee's
	// realistic-scale ListAttention test).
	getPRFn func(ctx context.Context, repo string, number int) (*api.PR, error)

	// searchFn/rateLimit/rateLimitErr back the List (bead pg2-2j5ac.28.1)
	// seam. rateLimit defaults to a comfortably-above-reserve value (via
	// rateLimitOrDefault below) so existing tests that never set it don't
	// need to know about rate-limit protection at all. searchFn backs
	// ghProvider.SearchPRs — List's own ids_only path, Search, and
	// ListAttention (unchanged by bead pg2-aehpr's batched-query
	// rewrite). searchEnrichedFn backs ghProvider.SearchPRsEnriched —
	// List's own non-ids_only path only (bead pg2-aehpr).
	searchFn         func(ctx context.Context, query string) ([]api.PR, error)
	searchEnrichedFn func(ctx context.Context, query string) ([]api.PR, error)
	// searchActivityFn backs ghProvider.SearchPRsActivity (list_activity).
	searchActivityFn func(ctx context.Context, query string, limit int) ([]api.PR, error)
	rateLimit        int
	rateLimitResetAt string
	rateLimitErr     error

	// files/commits/filesErr/commitsErr back the Files/Commits (bead
	// pg2-2j5ac.28.2) seam.
	files      []api.File
	commits    []api.Commit
	filesErr   error
	commitsErr error

	// viewerLogin/viewerLoginErr and reviewsWithCommitFn back
	// ListAttention's own ported mine-vs-team predicate (bead pg2-7wqkr).
	viewerLogin         string
	viewerLoginErr      error
	reviewsWithCommitFn func(ctx context.Context, repo string, number int) ([]api.Review, error)
	// reviewsSubmittedFn backs ghProvider.ListReviewsSubmitted (list_activity's
	// pr.reviewed kind).
	reviewsSubmittedFn func(ctx context.Context, repo string, number int) ([]api.Review, error)

	// review_submit seam (see review_submit_test.go). host, when set, is a
	// stateful simulation of the PR's reviews that backs GetPendingReview and
	// the write primitives. ops records the order of the host calls.
	host *fakeHost
	ops  []string

	// review_pending seam (see review_pending_test.go). pendingSeq, when set,
	// answers successive GetPendingReview calls in order (the last entry
	// repeats), so a test can model the host changing between two lookups.
	pendingData  *github.PendingReviewData
	pendingErr   error
	pendingSeq   []pendingReply
	pendingCalls int
}

func (f *fakeGH) GetPR(ctx context.Context, repo string, number int) (*api.PR, error) {
	if f.getPRFn != nil {
		return f.getPRFn(ctx, repo, number)
	}
	if f.getPRErr != nil {
		return nil, f.getPRErr
	}
	return f.pr, nil
}

func (f *fakeGH) ListComments(ctx context.Context, repo string, number int) ([]api.Comment, error) {
	if f.commentsFn != nil {
		return f.commentsFn(ctx, repo, number)
	}
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

func (f *fakeGH) ListCommentsReport(ctx context.Context, repo string, number int) (*api.CommentsResult, error) {
	if f.commentsReportFn != nil {
		return f.commentsReportFn(ctx, repo, number)
	}
	cs, err := f.ListComments(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return &api.CommentsResult{Comments: cs}, nil
}

func (f *fakeGH) ListReviewsReport(ctx context.Context, repo string, number int) (*api.ReviewsResult, error) {
	if f.reviewsReport != nil {
		return f.reviewsReport, nil
	}
	rs, err := f.ListReviews(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return &api.ReviewsResult{Reviews: rs}, nil
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

func (f *fakeGH) SearchPRsEnriched(ctx context.Context, query string) ([]api.PR, error) {
	if f.searchEnrichedFn != nil {
		return f.searchEnrichedFn(ctx, query)
	}
	return nil, nil
}

func (f *fakeGH) SearchPRsActivity(ctx context.Context, query string, limit int) ([]api.PR, error) {
	if f.searchActivityFn != nil {
		return f.searchActivityFn(ctx, query, limit)
	}
	return nil, nil
}

// rateLimitOrDefaultReserve is fakeGH's own zero-value convenience: a
// fakeGH that never sets rateLimit answers comfortably above
// defaultRateReservePoints, so every EXISTING test in this file (written
// before List/rate-limit protection existed) keeps working unchanged.
const rateLimitOrDefaultReserve = defaultRateReservePoints + 1000

func (f *fakeGH) ReadRateLimit(ctx context.Context) (github.RateLimit, error) {
	if f.rateLimitErr != nil {
		return github.RateLimit{}, f.rateLimitErr
	}
	if f.rateLimit == 0 {
		return github.RateLimit{Remaining: rateLimitOrDefaultReserve, ResetAt: f.rateLimitResetAt}, nil
	}
	return github.RateLimit{Remaining: f.rateLimit, ResetAt: f.rateLimitResetAt}, nil
}

func (f *fakeGH) GetFiles(ctx context.Context, repo string, number int) ([]api.File, error) {
	if f.filesErr != nil {
		return nil, f.filesErr
	}
	return f.files, nil
}

func (f *fakeGH) GetCommits(ctx context.Context, repo string, number int) ([]api.Commit, error) {
	if f.commitsErr != nil {
		return nil, f.commitsErr
	}
	return f.commits, nil
}

func (f *fakeGH) ViewerLogin(ctx context.Context) (string, error) {
	if f.viewerLoginErr != nil {
		return "", f.viewerLoginErr
	}
	if f.viewerLogin != "" {
		return f.viewerLogin, nil
	}
	return "me", nil
}

func (f *fakeGH) ListReviewsSubmitted(ctx context.Context, repo string, number int) ([]api.Review, error) {
	if f.reviewsSubmittedFn != nil {
		return f.reviewsSubmittedFn(ctx, repo, number)
	}
	return nil, nil
}

func (f *fakeGH) ReviewsWithCommit(ctx context.Context, repo string, number int) ([]api.Review, error) {
	if f.reviewsWithCommitFn != nil {
		return f.reviewsWithCommitFn(ctx, repo, number)
	}
	return nil, nil
}

func newTestBackend(t *testing.T, gh *fakeGH) *Backend {
	t.Helper()
	return New(gh)
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
}

// TestBackend_Show_CarriesReviewThreadIDFlagsAndConnectionReports proves Show
// maps each inline comment's real review-thread id (distinct from the
// unchanged thread_id) and its thread flags onto the wire shape, and copies
// the per-connection truncation reports onto PR.Connections.
func TestBackend_Show_CarriesReviewThreadIDFlagsAndConnectionReports(t *testing.T) {
	gh := &fakeGH{
		pr: &api.PR{Repo: "owner/repo", Number: 7, Title: "T", State: "open"},
		commentsReportFn: func(context.Context, string, int) (*api.CommentsResult, error) {
			return &api.CommentsResult{
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
			}, nil
		},
		reviewsReport: &api.ReviewsResult{
			Reviews: []api.Review{{ID: "PRR_a", Author: "bob", State: "PENDING", Body: "draft"}},
			Report:  api.ConnectionReport{Total: 1, Returned: 1},
		},
	}

	got, err := newTestBackend(t, gh).Show(context.Background(), "owner/repo#7")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
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
	if got.CommentCount != 1 {
		t.Errorf("CommentCount = %d, want 1 (top-level only)", got.CommentCount)
	}
	if got.Connections == nil {
		t.Fatal("Show must report its connections")
	}
	want := schema.PRConnections{
		Reviews:  schema.PRConnectionReport{Total: 1, Returned: 1},
		Threads:  schema.PRConnectionReport{Truncated: true, Total: 1001, Returned: 1000},
		Comments: schema.PRConnectionReport{Total: 2, Returned: 2},
	}
	if *got.Connections != want {
		t.Errorf("Connections = %+v, want %+v", *got.Connections, want)
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

// ----------------------------------------------------------------------
// List (bead pg2-2j5ac.28.1; batched-query rewrite bead pg2-aehpr)
// ----------------------------------------------------------------------

func TestBackend_List_SingleExpr(t *testing.T) {
	gh := &fakeGH{
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			if query != "is:open author:@me" {
				t.Fatalf("query = %q", query)
			}
			return []api.PR{{Repo: "owner/repo", Number: 1, Title: "a", State: "open", HeadSHA: "deadbeef", ChecksRollup: "success"}}, nil
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.List(context.Background(), []string{"is:open author:@me"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].ID != "owner/repo#1" {
		t.Fatalf("Entities = %+v", got.Entities)
	}
	// HeadSHA/ChecksRollup now come straight off SearchPRsEnriched's own
	// already-enriched result (bead pg2-aehpr) -- there is no more
	// supplemental per-PR fetch to merge them in from.
	if got.Entities[0].HeadSHA != "deadbeef" || got.Entities[0].ChecksRollup != "success" {
		t.Fatalf("Entities[0] = %+v, want HeadSHA/ChecksRollup carried straight through from SearchPRsEnriched", got.Entities[0])
	}
	if len(got.PresentIDs) != 1 || got.PresentIDs[0] != "owner/repo#1" {
		t.Fatalf("PresentIDs = %+v", got.PresentIDs)
	}
	// Cursor is always nil now (bead pg2-aehpr, design doc section 6: "the
	// FingerprintCursor/cursor mechanism becomes vestigial") --
	// TestBackend_List_CursorAlwaysNil is this behavior's own dedicated
	// test; this test's concern is just Entities/PresentIDs.
	if got.Cursor != nil {
		t.Fatalf("Cursor = %s, want nil", got.Cursor)
	}
}

func TestBackend_List_CarriesMergeable(t *testing.T) {
	gh := &fakeGH{
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{{Repo: "owner/repo", Number: 1, State: "open", Mergeable: "UNKNOWN"}}, nil
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.List(context.Background(), []string{"is:open"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 || got.Entities[0].Mergeable != "UNKNOWN" {
		t.Fatalf("Entities = %+v, want Mergeable carried verbatim (UNKNOWN)", got.Entities)
	}
}

func TestBackend_List_MultipleExpressions_UnionDeduplicated(t *testing.T) {
	calls := 0
	gh := &fakeGH{
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			calls++
			if query == "is:open author:@me" {
				return []api.PR{{Repo: "owner/repo", Number: 1}, {Repo: "owner/repo", Number: 2}}, nil
			}
			return []api.PR{{Repo: "owner/repo", Number: 2}}, nil
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.List(context.Background(), []string{"is:open author:@me", "is:open review-requested:@me"}, false, nil)
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

// TestBackend_List_IDsOnly_OmitsEntities proves ids_only stays on the
// cheaper, unenriched ghProvider.SearchPRs (design doc section 6's own
// "the ids_only=true path is unchanged" MUST-cover point) rather than
// ever calling SearchPRsEnriched — present_ids never needs anything
// beyond identity, so paying for the enriched query's extra nested
// fields here would be pure waste.
func TestBackend_List_IDsOnly_OmitsEntities(t *testing.T) {
	gh := &fakeGH{
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{{Repo: "owner/repo", Number: 1}}, nil
		},
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			t.Fatal("SearchPRsEnriched must not be called when ids_only is true")
			return nil, nil
		},
		getPRFn: func(ctx context.Context, repo string, number int) (*api.PR, error) {
			t.Fatal("GetPR must not be called when ids_only is true")
			return nil, nil
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.List(context.Background(), []string{"is:open"}, true, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 0 {
		t.Fatalf("Entities = %+v, want empty when ids_only is true", got.Entities)
	}
	if len(got.PresentIDs) != 1 {
		t.Fatalf("PresentIDs = %+v, want present_ids populated regardless of ids_only", got.PresentIDs)
	}
	if got.Cursor != nil {
		t.Fatalf("Cursor = %s, want nil when ids_only is true", got.Cursor)
	}
}

func TestBackend_List_SearchError_Classified(t *testing.T) {
	gh := &fakeGH{searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
		return nil, github.ErrGHAuthInvalid
	}}
	b := newTestBackend(t, gh)

	_, err := b.List(context.Background(), []string{"is:open"}, false, nil)
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

// TestBackend_List_RateLimitBelowReserve_IsUnavailable is the design
// "Rate protection" bullet's own regression proof: a rate-limit
// remainder below the reserve answers unavailable BEFORE any search is
// even attempted.
func TestBackend_List_RateLimitBelowReserve_IsUnavailable(t *testing.T) {
	searchEnrichedCalled := false
	gh := &fakeGH{
		rateLimit: 500,
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			searchEnrichedCalled = true
			return nil, nil
		},
	}
	b := newTestBackend(t, gh)

	ctx := scriptout.WithConfig(context.Background(), []byte(`{"rate_reserve_points":1000}`))
	_, err := b.List(ctx, []string{"is:open"}, false, nil)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	if searchEnrichedCalled {
		t.Fatal("SearchPRsEnriched must not be called once the rate-limit check fails")
	}
}

func TestBackend_List_RateLimitAboveReserve_Succeeds(t *testing.T) {
	gh := &fakeGH{
		rateLimit: 2000,
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{{Repo: "owner/repo", Number: 1}}, nil
		},
	}
	b := newTestBackend(t, gh)

	ctx := scriptout.WithConfig(context.Background(), []byte(`{"rate_reserve_points":1000}`))
	got, err := b.List(ctx, []string{"is:open"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("Entities = %+v", got.Entities)
	}
}

// TestBackend_RateGuard_RecordsReadingOnEvent (bead pg2-ph0o4): every guarded
// op records the rate-limit reading it took -- remaining, reset, and the
// reserve in force -- on the in-flight call's event, and the record carries
// through BOTH the pass and the below-reserve paths, since the reading is
// exactly what the "budget below reserve" alert needs.
func TestBackend_RateGuard_RecordsReadingOnEvent(t *testing.T) {
	cases := []struct {
		name         string
		remaining    int
		wantErr      bool
		wantHeadroom int
		wantBelow    bool
	}{
		{"above reserve", 2500, false, 1500, false},
		{"below reserve", 400, true, -600, true},
	}
	ops := map[string]func(b *Backend, ctx context.Context) error{
		"list": func(b *Backend, ctx context.Context) error {
			_, err := b.List(ctx, []string{"is:open"}, false, nil)
			return err
		},
		"search": func(b *Backend, ctx context.Context) error {
			_, err := b.Search(ctx, "is:open", nil)
			return err
		},
		"list_attention": func(b *Backend, ctx context.Context) error {
			_, err := b.ListAttention(ctx)
			return err
		},
	}
	for _, c := range cases {
		for opName, run := range ops {
			t.Run(c.name+"/"+opName, func(t *testing.T) {
				sink := &captureSink{}
				b := newTestBackend(t, &fakeGH{rateLimit: c.remaining, rateLimitResetAt: "2026-10-03T14:00:00Z"})
				table := eventlog.Instrument(scriptout.DispatchTable{opName: {
					SchemaVersion: 1,
					Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
						return nil, run(b, scriptout.WithConfig(ctx, []byte(`{"rate_reserve_points":1000}`)))
					},
				}}, sink, "", time.Now)
				_, err := table[opName].Handle(context.Background(), nil)
				if (err != nil) != c.wantErr {
					t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
				}
				ev := sink.last(t)
				if ev.GraphQLRemaining == nil || *ev.GraphQLRemaining != c.remaining {
					t.Errorf("graphql_remaining = %v, want %d", ev.GraphQLRemaining, c.remaining)
				}
				if ev.GraphQLResetAt == nil || *ev.GraphQLResetAt != "2026-10-03T14:00:00Z" {
					t.Errorf("graphql_reset_at = %v", ev.GraphQLResetAt)
				}
				if ev.GraphQLHeadroom == nil || *ev.GraphQLHeadroom != c.wantHeadroom {
					t.Errorf("graphql_headroom = %v, want %d", ev.GraphQLHeadroom, c.wantHeadroom)
				}
				if ev.BelowReserve != c.wantBelow {
					t.Errorf("below_reserve = %v, want %v", ev.BelowReserve, c.wantBelow)
				}
				wantCode := ""
				if c.wantErr {
					wantCode = "unavailable"
				}
				if ev.ErrorCode != wantCode {
					t.Errorf("error_code = %q, want %q", ev.ErrorCode, wantCode)
				}
			})
		}
	}
}

// TestBackend_RateGuard_ReadFailureIsClassifiedAndLeavesNoReading: when the
// rate-limit read itself fails with an auth error, the call answers
// unauthenticated (what the auth alert keys on) and has no graphql fields.
func TestBackend_RateGuard_ReadFailureIsClassifiedAndLeavesNoReading(t *testing.T) {
	sink := &captureSink{}
	b := newTestBackend(t, &fakeGH{rateLimitErr: github.ErrGHAuthInvalid})
	table := eventlog.Instrument(scriptout.DispatchTable{"list": {
		SchemaVersion: 1,
		Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
			return b.List(ctx, []string{"is:open"}, false, nil)
		},
	}}, sink, "", time.Now)
	_, err := table["list"].Handle(context.Background(), nil)
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want unauthenticated", err)
	}
	ev := sink.last(t)
	if ev.ErrorCode != "unauthenticated" || ev.Level != "error" {
		t.Errorf("event = %+v", ev)
	}
	if ev.GraphQLRemaining != nil || ev.GraphQLHeadroom != nil {
		t.Errorf("failed read left graphql fields: %+v", ev)
	}
}

// TestBackend_List_CursorAlwaysNil proves the FingerprintCursor mechanism
// is now vestigial (bead pg2-aehpr, design doc section 6): List always
// answers Cursor: nil, regardless of what the caller passed as its own
// incoming cursor, since every call already fetches every field fresh
// via SearchPRsEnriched's own batched query -- there is no more per-PR
// "did this change" decision for a cursor to inform. internal/github's
// DecodeCursor/EncodeCursor/RefreshCursor codec (fingerprint.go) was
// deleted outright as a follow-up cleanup (bead pg2-c0vs3) once this
// vestigial status was confirmed.
func TestBackend_List_CursorAlwaysNil(t *testing.T) {
	gh := &fakeGH{
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{{Repo: "owner/repo", Number: 1}}, nil
		},
	}
	b := newTestBackend(t, gh)

	incoming := json.RawMessage(`{"v":1,"fp":{"owner/repo#1":"deadbeef00000000"}}`)
	got, err := b.List(context.Background(), []string{"is:open"}, false, incoming)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Cursor != nil {
		t.Fatalf("Cursor = %s, want nil", got.Cursor)
	}
}

// ----------------------------------------------------------------------
// Search (bead pg2-8hcnx: wire the search capability's Search op onto the
// same ghProvider.SearchPRs List already uses)
// ----------------------------------------------------------------------

func TestBackend_Search_MapsGHDataToSearchResult(t *testing.T) {
	gh := &fakeGH{searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
		if query != "repo:owner/repo is:pr is:open" {
			t.Fatalf("query = %q", query)
		}
		return []api.PR{{Repo: "owner/repo", Number: 7, Title: "Add feature", URL: "https://example.invalid/owner/repo/pull/7"}}, nil
	}}
	b := newTestBackend(t, gh)

	got, err := b.Search(context.Background(), "repo:owner/repo is:pr is:open", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Search results = %+v, want exactly 1", got)
	}
	want := schema.SearchResult{Type: "pr", ID: "owner/repo#7", Title: "Add feature", URL: "https://example.invalid/owner/repo/pull/7", Source: "pg-connector-pr-github"}
	if got[0].Type != want.Type || got[0].ID != want.ID || got[0].Title != want.Title || got[0].URL != want.URL || got[0].Source != want.Source || len(got[0].Attributes) != 0 {
		t.Fatalf("Search()[0] = %+v, want %+v", got[0], want)
	}
}

func TestBackend_Search_EmptyQuery_IsInvalidArgument(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})

	_, err := b.Search(context.Background(), "   ", nil)
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Search_SearchError_Classified(t *testing.T) {
	gh := &fakeGH{searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
		return nil, github.ErrGHAuthInvalid
	}}
	b := newTestBackend(t, gh)

	_, err := b.Search(context.Background(), "is:open", nil)
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

// TestBackend_Search_RateLimitBelowReserve_IsUnavailable mirrors
// TestBackend_List_RateLimitBelowReserve_IsUnavailable: Search hits the
// same GraphQL quota via the same ghProvider.SearchPRs call, so it applies
// the identical rate-limit reserve check before ever calling SearchPRs.
func TestBackend_Search_RateLimitBelowReserve_IsUnavailable(t *testing.T) {
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
	_, err := b.Search(ctx, "is:open", nil)
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnavailable)", err)
	}
	if searchCalled {
		t.Fatal("SearchPRs must not be called once the rate-limit check fails")
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

// TestBackend_Show_MapsNewPRFactFields_v4 proves the bead pg2-2j5ac.28.2
// PR-facts fields (HeadSHA, Additions, Deletions, ChangedFiles, Mergeable,
// MergeStateStatus, ReviewRequests, ChecksRollup) flow through toSchemaPR
// from api.PR to schema.PR unchanged.
func TestBackend_Show_MapsNewPRFactFields_v4(t *testing.T) {
	gh := &fakeGH{
		pr: &api.PR{
			Repo: "owner/repo", Number: 1, Title: "T", State: "open",
			HeadSHA: "abc123", Additions: 10, Deletions: 2, ChangedFiles: 3,
			Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
			ReviewRequests: []string{"alice", "core-team"},
			ChecksRollup:   "success",
		},
	}
	b := newTestBackend(t, gh)
	got, err := b.Show(context.Background(), "owner/repo#1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.HeadSHA != "abc123" || got.Additions != 10 || got.Deletions != 2 || got.ChangedFiles != 3 {
		t.Fatalf("diff-size fields = %+v", got)
	}
	if got.Mergeable != "MERGEABLE" || got.MergeStateStatus != "CLEAN" {
		t.Fatalf("mergeability fields = %+v", got)
	}
	if len(got.ReviewRequests) != 2 || got.ReviewRequests[0] != "alice" || got.ReviewRequests[1] != "core-team" {
		t.Fatalf("ReviewRequests = %+v", got.ReviewRequests)
	}
	if got.ChecksRollup != "success" {
		t.Fatalf("ChecksRollup = %q, want success", got.ChecksRollup)
	}
}

func TestBackend_Files_MapsGHDataToSchemaPRFilesResult(t *testing.T) {
	gh := &fakeGH{files: []api.File{{Path: "a.go", Additions: 5, Deletions: 1}}}
	b := newTestBackend(t, gh)
	got, err := b.Files(context.Background(), "owner/repo#1")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if got.ID != "owner/repo#1" || len(got.Files) != 1 || got.Files[0].Path != "a.go" {
		t.Fatalf("Files result = %+v", got)
	}
}

func TestBackend_Files_InvalidID(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})
	_, err := b.Files(context.Background(), "not-a-valid-id")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Files_GHError_Classified(t *testing.T) {
	gh := &fakeGH{filesErr: github.ErrGHAuthInvalid}
	b := newTestBackend(t, gh)
	_, err := b.Files(context.Background(), "owner/repo#1")
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

func TestBackend_Commits_MapsGHDataToSchemaPRCommitsResult(t *testing.T) {
	gh := &fakeGH{commits: []api.Commit{{SHA: "abc123", Author: "alice", Message: "fix bug"}}}
	b := newTestBackend(t, gh)
	got, err := b.Commits(context.Background(), "owner/repo#1")
	if err != nil {
		t.Fatalf("Commits: %v", err)
	}
	if got.ID != "owner/repo#1" || len(got.Commits) != 1 || got.Commits[0].Author != "alice" {
		t.Fatalf("Commits result = %+v", got)
	}
}

func TestBackend_Commits_InvalidID(t *testing.T) {
	b := newTestBackend(t, &fakeGH{})
	_, err := b.Commits(context.Background(), "not-a-valid-id")
	if !errors.Is(err, scriptout.ErrInvalidArgument) {
		t.Fatalf("err = %v, want errors.Is(err, ErrInvalidArgument)", err)
	}
}

func TestBackend_Commits_GHError_Classified(t *testing.T) {
	gh := &fakeGH{commitsErr: github.ErrGHAuthInvalid}
	b := newTestBackend(t, gh)
	_, err := b.Commits(context.Background(), "owner/repo#1")
	if !errors.Is(err, scriptout.ErrUnauthenticated) {
		t.Fatalf("err = %v, want errors.Is(err, ErrUnauthenticated)", err)
	}
}

// ----------------------------------------------------------------------
// schema.PR summary fields (bead pg2-2j5ac.52.6.1): node_id, updated_at,
// review_decision, comment_count, review_count.
// ----------------------------------------------------------------------

// canonicalSummaryHash is a local copy of the rule in cmd/pg-connector's
// ledger.go canonicalHash (unexported in package main, and Go's internal
// rule stops cmd/pg-connector importing this package's internal tree): marshal
// the entity, decode it into map[string]json.RawMessage, delete "as_of" and
// "stale", re-marshal, and compare. Keep it in step with that function; it
// is deliberately NOT exported or moved. The full JSON is never compared
// directly because as_of has one-second precision, which would make a
// comparison flaky.
func canonicalSummaryHash(t *testing.T, pr schema.PR) string {
	t.Helper()
	raw, err := json.Marshal(pr)
	if err != nil {
		t.Fatalf("marshal schema.PR: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode schema.PR: %v", err)
	}
	delete(fields, "as_of")
	delete(fields, "stale")
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("re-encode schema.PR: %v", err)
	}
	return string(data)
}

// listOnePR runs Backend.List against a fakeGH whose batched search answers
// exactly the given PR, and returns the one matched entity.
func listOnePR(t *testing.T, in api.PR) schema.PR {
	t.Helper()
	gh := &fakeGH{
		searchEnrichedFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{in}, nil
		},
	}
	b := newTestBackend(t, gh)
	got, err := b.List(context.Background(), []string{"is:open"}, false, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Entities) != 1 {
		t.Fatalf("Entities = %+v, want exactly one", got.Entities)
	}
	return got.Entities[0]
}

// TestPRChangesReportsChangedWhenOnlyCommentCountMoves proves the point of
// putting comment_count on the summary entity: changes diffs list results by
// a hash of the whole summary entity (canonicalHash, minus as_of/stale), so a
// new comment must alter that hash. omitempty drops a zero count, so both
// values are non-zero. The control assertion proves two List calls over
// identical input hash equal, so the difference below cannot be noise.
func TestPRChangesReportsChangedWhenOnlyCommentCountMoves(t *testing.T) {
	base := api.PR{Repo: "owner/repo", Number: 1, Title: "T", State: "open", UpdatedAt: "2026-09-14T10:00:00Z"}

	oneA := base
	oneA.CommentCount = 1
	oneB := base
	oneB.CommentCount = 1
	two := base
	two.CommentCount = 2

	hashOneA := canonicalSummaryHash(t, listOnePR(t, oneA))
	hashOneB := canonicalSummaryHash(t, listOnePR(t, oneB))
	hashTwo := canonicalSummaryHash(t, listOnePR(t, two))

	if hashOneA != hashOneB {
		t.Fatalf("control: identical input hashed differently:\n%s\n%s", hashOneA, hashOneB)
	}
	if hashOneA == hashTwo {
		t.Fatalf("hash did not change when only comment_count moved 1 -> 2:\n%s", hashOneA)
	}
}

// TestPRSummaryIncludesNodeID proves both read paths carry the PR's own
// GraphQL node id (the rename-proof dedup/adoption key) plus the other four
// summary fields, straight from what the GitHub layer decoded: never
// synthesized from "<repo>#<number>".
func TestPRSummaryIncludesNodeID(t *testing.T) {
	summary := api.PR{
		Repo: "owner/repo", Number: 7, Title: "T", State: "open",
		NodeID:         "PR_kwDOSynthetic7",
		UpdatedAt:      "2026-09-14T10:00:00Z",
		ReviewDecision: "APPROVED",
		CommentCount:   5,
		ReviewCount:    3,
	}

	check := func(t *testing.T, path string, got schema.PR) {
		t.Helper()
		if got.NodeID != "PR_kwDOSynthetic7" {
			t.Errorf("%s: NodeID = %q, want %q", path, got.NodeID, "PR_kwDOSynthetic7")
		}
		if got.UpdatedAt != "2026-09-14T10:00:00Z" {
			t.Errorf("%s: UpdatedAt = %q", path, got.UpdatedAt)
		}
		if got.ReviewDecision != "APPROVED" {
			t.Errorf("%s: ReviewDecision = %q", path, got.ReviewDecision)
		}
		if got.ReviewCount != 3 {
			t.Errorf("%s: ReviewCount = %d, want 3", path, got.ReviewCount)
		}
	}

	t.Run("list", func(t *testing.T) {
		got := listOnePR(t, summary)
		check(t, "list", got)
		if got.CommentCount != 5 {
			t.Errorf("list: CommentCount = %d, want the list connection's own 5 (comments slice is nil there)", got.CommentCount)
		}
	})

	t.Run("show", func(t *testing.T) {
		showSummary := summary
		showSummary.CommentCount = 0 // GetPR never fills this: ghPR has no comments field
		gh := &fakeGH{pr: &showSummary, comments: []api.Comment{}}
		b := newTestBackend(t, gh)
		got, err := b.Show(context.Background(), "owner/repo#7")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		check(t, "show", *got)
	})

	t.Run("absent stays empty and is never derived from repo and number", func(t *testing.T) {
		bare := api.PR{Repo: "owner/repo", Number: 8, Title: "T", State: "open"}
		got := listOnePR(t, bare)
		if got.NodeID != "" || got.UpdatedAt != "" || got.ReviewDecision != "" {
			t.Errorf("summary fields fabricated for a bare PR: %+v", got)
		}
		if got.CommentCount != 0 || got.ReviewCount != 0 {
			t.Errorf("counts fabricated for a bare PR: %+v", got)
		}
	})
}

// TestBackend_Show_CommentCountCountsOnlyTopLevelComments proves the show
// path's comment_count is the number of top-level (issue-endpoint) PR
// comments only. GetPR cannot supply it (ghPR has no comments field), so
// toSchemaPR counts the comments it is handed: entries with no thread and no
// path. An inline review comment is excluded whether or not it nested under
// a review: an inline comment whose review could not be joined (empty
// ReviewID) lands in PR.Comments, so len(Comments) is NOT the count.
func TestBackend_Show_CommentCountCountsOnlyTopLevelComments(t *testing.T) {
	gh := &fakeGH{
		// A stale non-zero value on the api.PR must be ignored: the show
		// path was handed a comments slice, so that slice is the source.
		pr: &api.PR{Repo: "owner/repo", Number: 3, Title: "T", State: "open", CommentCount: 99},
		comments: []api.Comment{
			{ID: "c1", Author: "alice", Body: "top-level one"},
			{ID: "c2", Author: "carol", Body: "top-level two"},
			{ID: "c3", Author: "bob", Body: "inline nested", Path: "main.go", Line: 10, ThreadID: "t3", ReviewID: "PRR_kwDOSynthetic1"},
			{ID: "c4", Author: "bob", Body: "inline, review not joined", Path: "main.go", Line: 12, ThreadID: "t4"},
		},
		reviews: []api.Review{
			{ID: "PRR_kwDOSynthetic1", Author: "bob", State: "COMMENTED"},
		},
	}
	b := newTestBackend(t, gh)

	got, err := b.Show(context.Background(), "owner/repo#3")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.CommentCount != 2 {
		t.Fatalf("CommentCount = %d, want 2 (top-level only)", got.CommentCount)
	}
	if len(got.Comments) != 3 {
		t.Fatalf("len(Comments) = %d, want 3 (2 top-level + 1 inline with no joined review): %+v", len(got.Comments), got.Comments)
	}
}

// TestBackend_Show_CommentCountZeroWhenNoCommentsFetched proves an empty,
// non-nil comments slice (what ListComments answers for a PR with no
// comments) yields 0 rather than falling back to the api.PR's own count: the
// fallback is only for the list path, which passes a nil slice.
func TestBackend_Show_CommentCountZeroWhenNoCommentsFetched(t *testing.T) {
	gh := &fakeGH{
		pr:       &api.PR{Repo: "owner/repo", Number: 4, Title: "T", State: "open", CommentCount: 7},
		comments: []api.Comment{},
	}
	b := newTestBackend(t, gh)

	got, err := b.Show(context.Background(), "owner/repo#4")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.CommentCount != 0 {
		t.Fatalf("CommentCount = %d, want 0", got.CommentCount)
	}
}

// TestPRShowCarriesBaseSHA proves Show maps the GitHub layer's base commit
// into schema.PR.BaseSHA and leaves it empty when absent (bead
// pg2-2j5ac.52.6.2).
func TestPRShowCarriesBaseSHA(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"present", "0123456789abcdef0123456789abcdef01234567"},
		{"absent", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr := api.PR{Repo: "owner/repo", Number: 7, Title: "T", State: "open", BaseSHA: tc.in}
			gh := &fakeGH{pr: &pr, comments: []api.Comment{}}
			b := newTestBackend(t, gh)
			got, err := b.Show(context.Background(), "owner/repo#7")
			if err != nil {
				t.Fatalf("Show: %v", err)
			}
			if got.BaseSHA != tc.in {
				t.Errorf("BaseSHA = %q, want %q", got.BaseSHA, tc.in)
			}
		})
	}
}

// captureSink collects events for the rate-guard event tests.
type captureSink struct{ events []eventlog.Event }

func (c *captureSink) Write(ev eventlog.Event) { c.events = append(c.events, ev) }

func (c *captureSink) last(t *testing.T) eventlog.Event {
	t.Helper()
	if len(c.events) == 0 {
		t.Fatal("no event recorded")
	}
	return c.events[len(c.events)-1]
}

// guardedFakeGH is a fakeGH that, like *github.Provider, accepts a retry guard.
type guardedFakeGH struct {
	*fakeGH
	guard func(ctx context.Context) error
}

func (g *guardedFakeGH) SetRetryGuard(f func(ctx context.Context) error) { g.guard = f }

// New hands a retry-capable gh the rate-limit reserve check as its retry
// guard (bead pg2-daktd): above the reserve it passes, below it it answers the
// same unavailable verdict List does, and it reads the budget from the
// call's own config.
func TestNew_InstallsRateReserveAsRetryGuard(t *testing.T) {
	fake := &guardedFakeGH{fakeGH: &fakeGH{rateLimit: 500}}
	New(fake)
	if fake.guard == nil {
		t.Fatal("New did not install a retry guard on a gh that accepts one")
	}
	// 500 remaining is under the default reserve (1000).
	err := fake.guard(context.Background())
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("guard under reserve = %v, want an unavailable verdict", err)
	}
	fake.fakeGH.rateLimit = defaultRateReservePoints + 1
	if err := fake.guard(context.Background()); err != nil {
		t.Fatalf("guard above reserve = %v, want nil", err)
	}
	// A gh that cannot take a guard is simply left alone.
	_ = New(&fakeGH{})
}
