package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-pr-github/internal/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

const (
	attnSelf     = "me"
	attnTeammate = "teammate"
)

// --- needsAttentionForPR: pure predicate, mirroring
// packages/pg-pr/internal/snapshot/attention_test.go's own TestNeedsAttention
// fixture style (bead pg2-7wqkr ports the CONCEPT, not the code). ---

func TestNeedsAttentionForPR(t *testing.T) {
	tests := []struct {
		name        string
		reviews     []api.Review
		self        string
		head        string
		hasConflict bool
		wantNeed    bool
		wantReason  string
	}{
		{
			name:       "nobody reviewed + I have never reviewed -> NEEDS a first review",
			head:       "h1",
			self:       attnSelf,
			wantNeed:   true,
			wantReason: attentionReasonUnreviewed,
		},
		{
			name:     "teammate approved at head -> NO attention (off the hook)",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnTeammate, State: "APPROVED", CommitOID: "h1"}},
			wantNeed: false,
		},
		{
			name:     "I reviewed the current head -> NO attention",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnSelf, State: "APPROVED", CommitOID: "h1"}},
			wantNeed: false,
		},
		{
			name:       "new commits after I approved (re-review) -> NEEDS",
			head:       "h2",
			self:       attnSelf,
			reviews:    []api.Review{{Author: attnSelf, State: "APPROVED", CommitOID: "h1"}},
			wantNeed:   true,
			wantReason: attentionReasonReReview,
		},
		{
			name: "I approved an earlier head but teammate approved latest -> NO attention",
			head: "h2",
			self: attnSelf,
			reviews: []api.Review{
				{Author: attnSelf, State: "APPROVED", CommitOID: "h1"},
				{Author: attnTeammate, State: "APPROVED", CommitOID: "h2"},
			},
			wantNeed: false,
		},
		{
			name:     "I reviewed the latest head (re-reviewed after advance) -> NO attention",
			head:     "h2",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnSelf, State: "APPROVED", CommitOID: "h2"}},
			wantNeed: false,
		},
		{
			name:        "merge conflict dampens everything -> NO attention",
			head:        "h1",
			self:        attnSelf,
			hasConflict: true,
			wantNeed:    false,
		},
		{
			name:     "teammate CHANGES_REQUESTED does not close the team edge, but my own does",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnSelf, State: "CHANGES_REQUESTED", CommitOID: "h1"}},
			wantNeed: false,
		},
		{
			name:       "dismissed approval (mine) does not stand -> re-review",
			head:       "h1",
			self:       attnSelf,
			reviews:    []api.Review{{Author: attnSelf, State: "DISMISSED", CommitOID: "h1"}},
			wantNeed:   true,
			wantReason: attentionReasonReReview,
		},
		{
			name:     "dismissed teammate approval does not close the team edge -> unreviewed",
			head:     "h1",
			self:     attnSelf,
			reviews:  []api.Review{{Author: attnTeammate, State: "DISMISSED", CommitOID: "h1"}},
			wantNeed: true, wantReason: attentionReasonUnreviewed,
		},
		{
			name: "only the latest review per author counts (older approval superseded)",
			head: "h1",
			self: attnSelf,
			reviews: []api.Review{
				{Author: attnTeammate, State: "APPROVED", CommitOID: "h1"},
				{Author: attnTeammate, State: "CHANGES_REQUESTED", CommitOID: "h1"},
			},
			wantNeed:   true,
			wantReason: attentionReasonUnreviewed,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			need, reason := needsAttentionForPR(tc.reviews, tc.self, tc.head, tc.hasConflict)
			if need != tc.wantNeed {
				t.Fatalf("need = %v, want %v", need, tc.wantNeed)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// --- Backend.ListAttention: end-to-end over fakeGH. ---

func TestBackend_ListAttention_NoQueryConfigured_ReturnsEmpty(t *testing.T) {
	b := New(&fakeGH{})
	got, err := b.ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestBackend_ListAttention_ScansConfiguredQueryAndFiltersByPredicate(t *testing.T) {
	gh := &fakeGH{
		viewerLogin: attnSelf,
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{
				{Repo: "owner/repo", Number: 1, Title: "needs review", Author: attnTeammate},
				{Repo: "owner/repo", Number: 2, Title: "already approved by teammate", Author: attnTeammate},
			}, nil
		},
		reviewsWithCommitFn: func(ctx context.Context, repo string, number int) ([]api.Review, error) {
			if number == 2 {
				return []api.Review{{Author: attnTeammate, State: "APPROVED", CommitOID: "h1"}}, nil
			}
			return nil, nil
		},
	}
	// GetPR must answer per-number since fakeGH.pr is a single fixed value;
	// override via a small wrapper.
	gh.pr = &api.PR{Repo: "owner/repo", Number: 1, Title: "needs review", HeadSHA: "h1"}

	b := New(gh)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_query":"is:open is:pr -author:@me"}`))
	got, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	// fakeGH.GetPR always answers gh.pr (repo#1) regardless of which
	// number was requested — both PR 1 and PR 2 are therefore evaluated
	// against the SAME full-PR fixture (HeadSHA "h1", no conflict), but
	// their per-number reviewsWithCommitFn answers differ, which is what
	// this test actually exercises: PR 2's teammate approval at head
	// takes it off the hook while PR 1's empty review list leaves it
	// needing a first review.
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].ID != "owner/repo#1" {
		t.Fatalf("got[0].ID = %q, want owner/repo#1", got[0].ID)
	}
}

// TestBackend_ListAttention_FillsURLFromFullPR pins INV-ATTN-URL-1 for the
// PR backend: the item's url is the PR's own page as returned by GetPR, and
// a PR GitHub returned no URL for yields an item with no url (never a
// synthesized one).
func TestBackend_ListAttention_FillsURLFromFullPR(t *testing.T) {
	for _, tc := range []struct {
		name, prURL, want string
	}{
		{"url present", "https://example.invalid/owner/repo/pull/1", "https://example.invalid/owner/repo/pull/1"},
		{"url absent", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeGH{
				viewerLogin: attnSelf,
				searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
					return []api.PR{{Repo: "owner/repo", Number: 1, Title: "needs review", Author: attnTeammate}}, nil
				},
				pr: &api.PR{Repo: "owner/repo", Number: 1, Title: "needs review", HeadSHA: "h1", URL: tc.prURL},
			}
			b := New(gh)
			ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_query":"is:open is:pr"}`))
			got, err := b.ListAttention(ctx)
			if err != nil {
				t.Fatalf("ListAttention: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
			}
			if got[0].URL != tc.want {
				t.Fatalf("got[0].URL = %q, want %q", got[0].URL, tc.want)
			}
		})
	}
}

func TestBackend_ListAttention_RateLimitBelowReserve(t *testing.T) {
	gh := &fakeGH{rateLimit: 1}
	b := New(gh)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_query":"is:open"}`))
	_, err := b.ListAttention(ctx)
	if err == nil {
		t.Fatal("expected an error when rate limit is below the reserve")
	}
	if !errors.Is(err, scriptout.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// inFlightTracker counts concurrently-in-flight simulated `gh` calls, so a
// test can assert BOTH that calls actually overlapped (proving real
// parallelism, not just an accidentally-fast serial run) and that the
// overlap never exceeded the bounded worker pool's own size (proving the
// pool is actually bounded, not an unbounded fan-out).
type inFlightTracker struct {
	current atomic.Int32
	max     atomic.Int32
}

// begin records one more call starting; the returned func records it
// ending. Use as `defer tracker.begin()()`.
func (tr *inFlightTracker) begin() func() {
	n := tr.current.Add(1)
	for {
		m := tr.max.Load()
		if n <= m || tr.max.CompareAndSwap(m, n) {
			break
		}
	}
	return func() { tr.current.Add(-1) }
}

// TestBackend_ListAttention_RealisticScale_CompletesWellUnderExecTimeout is
// the realistic-scale regression test bead pg2-zutee asks for: multiple
// queries (6, matching the real attention_query's queries.team list that
// triggered the regression), dozens of candidate PRs, and simulated
// per-call latency on every fakeGH hook standing in for a real `gh`
// subprocess spawn (the bead's own root-cause estimate: "a few hundred ms
// to ~1s" each). Before this bead's fix (fully sequential SearchPRs/
// GetPR/ReviewsWithCommit), the sequential sum of that many calls at that
// latency would land well past scriptout's own 30s DefaultExecTimeout
// (packages/pg-connector/pkg/scriptout/limits.go) — this test asserts the
// real wall-clock elapsed time of the NEW, parallelized implementation
// instead, so a future regression back to sequential execution fails this
// test on timing, not just on inspection.
func TestBackend_ListAttention_RealisticScale_CompletesWellUnderExecTimeout(t *testing.T) {
	const (
		numQueries = 6 // bead's own trigger: attention_query's queries.team list has 6 entries.
		numPerQ    = 7 // 6*7 = 42 unique candidates -- "considerably more" than the bead's observed 15-20.
		latency    = 400 * time.Millisecond
		execBudget = 10 * time.Second // generous, but well under scriptout's 30s DefaultExecTimeout.
	)
	numCandidates := numQueries * numPerQ

	// sequentialEstimate is what the OLD, fully-sequential implementation
	// would have taken: 1 SearchPRs per query, plus 1 GetPR + 1
	// ReviewsWithCommit per unique candidate, every one of them serial.
	// This is the number that must exceed scriptout's 30s timeout for
	// this fixture to actually exercise the regression this bead fixes.
	sequentialEstimate := time.Duration(numQueries+2*numCandidates) * latency
	if sequentialEstimate <= scriptout.DefaultExecTimeout {
		t.Fatalf("fixture too small to demonstrate the regression: sequential estimate %v is not > scriptout.DefaultExecTimeout (%v)", sequentialEstimate, scriptout.DefaultExecTimeout)
	}

	queries := make([]string, numQueries)
	for i := range queries {
		queries[i] = fmt.Sprintf("is:open is:pr team-query-%d", i)
	}
	queryJSON, err := json.Marshal(queries)
	if err != nil {
		t.Fatalf("json.Marshal(queries): %v", err)
	}

	var (
		tracker                           inFlightTracker
		searchCalls, getPRCalls, revCalls atomic.Int32
	)
	gh := &fakeGH{
		viewerLogin: attnSelf,
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			if query == ownOpenPRsQuery {
				// The CI-failing scan (bead pg2-fnqqi): the operator has
				// no open PRs in this fixture, so it finds nothing and
				// is not part of the review-scan call counts below.
				return nil, nil
			}
			defer tracker.begin()()
			time.Sleep(latency)
			idx := -1
			for i, q := range queries {
				if q == query {
					idx = i
					break
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("unexpected query %q", query)
			}
			searchCalls.Add(1)
			prs := make([]api.PR, numPerQ)
			for j := 0; j < numPerQ; j++ {
				n := idx*numPerQ + j + 1
				prs[j] = api.PR{Repo: "owner/repo", Number: n, Title: fmt.Sprintf("pr %d", n), Author: attnTeammate}
			}
			return prs, nil
		},
		getPRFn: func(ctx context.Context, repo string, number int) (*api.PR, error) {
			defer tracker.begin()()
			time.Sleep(latency)
			getPRCalls.Add(1)
			return &api.PR{Repo: repo, Number: number, Title: fmt.Sprintf("pr %d", number), HeadSHA: "h1"}, nil
		},
		reviewsWithCommitFn: func(ctx context.Context, repo string, number int) ([]api.Review, error) {
			defer tracker.begin()()
			time.Sleep(latency)
			revCalls.Add(1)
			// Every other candidate already has a standing teammate
			// approval at head (off the hook); the rest need a first
			// review -- a realistic mixed backlog, not all-or-nothing.
			if number%2 == 0 {
				return []api.Review{{Author: attnTeammate, State: "APPROVED", CommitOID: "h1"}}, nil
			}
			return nil, nil
		},
	}

	b := New(gh)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(fmt.Sprintf(`{"attention_query":%s}`, queryJSON)))

	start := time.Now()
	got, err := b.ListAttention(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if elapsed > execBudget {
		t.Fatalf("ListAttention took %v, want <= %v (scriptout's own exec timeout is %v)", elapsed, execBudget, scriptout.DefaultExecTimeout)
	}

	if max := tracker.max.Load(); max <= 1 {
		t.Fatalf("max concurrent in-flight calls = %d, want > 1 -- calls ran sequentially, not in parallel", max)
	} else if int(max) > listAttentionMaxWorkers {
		t.Fatalf("max concurrent in-flight calls = %d, exceeded the bounded worker pool size %d", max, listAttentionMaxWorkers)
	}

	if gotSearch, want := int(searchCalls.Load()), numQueries; gotSearch != want {
		t.Fatalf("searchCalls = %d, want %d", gotSearch, want)
	}
	if gotGetPR, want := int(getPRCalls.Load()), numCandidates; gotGetPR != want {
		t.Fatalf("getPRCalls = %d, want %d", gotGetPR, want)
	}
	if gotRev, want := int(revCalls.Load()), numCandidates; gotRev != want {
		t.Fatalf("reviewsWithCommit calls = %d, want %d", gotRev, want)
	}

	wantNeed := numCandidates / 2 // every odd-numbered PR (per reviewsWithCommitFn above) needs a first review.
	if len(got) != wantNeed {
		t.Fatalf("len(got) = %d, want %d: %+v", len(got), wantNeed, got)
	}
}

// --- CI-failing attention (bead pg2-fnqqi): the operator's own open PRs whose
// head-commit CI is failed. ---

func failedCheck(name string) api.Check {
	return api.Check{Name: name, Workflow: "ci", Outcome: api.CheckFailure}
}

func passedCheck(name string) api.Check {
	return api.Check{Name: name, Workflow: "ci", Outcome: api.CheckSuccess}
}

func pendingCheck(name string) api.Check {
	return api.Check{Name: name, Workflow: "ci", Outcome: api.CheckPending}
}

func TestCIFailingForPR(t *testing.T) {
	none := func(string) bool { return false }
	ownOpen := func(mutate func(*api.PR)) api.PR {
		pr := api.PR{Repo: "owner/repo", Number: 7, State: "open", Author: attnSelf}
		mutate(&pr)
		return pr
	}
	tests := []struct {
		name     string
		pr       api.PR
		excluded func(string) bool
		want     bool
	}{
		{"failed", ownOpen(func(p *api.PR) { p.Checks = []api.Check{failedCheck("unit")} }), none, true},
		{"failed alongside passed", ownOpen(func(p *api.PR) { p.Checks = []api.Check{passedCheck("lint"), failedCheck("unit")} }), none, true},
		{"failed alongside pending still failed", ownOpen(func(p *api.PR) { p.Checks = []api.Check{pendingCheck("e2e"), failedCheck("unit")} }), none, true},
		{"failed build-test-validate still counts (pg2-p2ojd exception is for reviewing others)", ownOpen(func(p *api.PR) { p.Checks = []api.Check{failedCheck("build-test-validate")} }), none, true},
		{"pending", ownOpen(func(p *api.PR) { p.Checks = []api.Check{pendingCheck("unit")} }), none, false},
		{"passed", ownOpen(func(p *api.PR) { p.Checks = []api.Check{passedCheck("unit")} }), none, false},
		{"no checks", ownOpen(func(p *api.PR) {}), none, false},
		{"draft", ownOpen(func(p *api.PR) { p.Draft = true; p.Checks = []api.Check{failedCheck("unit")} }), none, false},
		{"closed", ownOpen(func(p *api.PR) { p.State = "closed"; p.Checks = []api.Check{failedCheck("unit")} }), none, false},
		{"merged", ownOpen(func(p *api.PR) { p.State = "closed"; p.Merged = true; p.Checks = []api.Check{failedCheck("unit")} }), none, false},
		{"someone else's PR", ownOpen(func(p *api.PR) { p.Author = attnTeammate; p.Checks = []api.Check{failedCheck("unit")} }), none, false},
		{"author match is case-insensitive", ownOpen(func(p *api.PR) { p.Author = "ME"; p.Checks = []api.Check{failedCheck("unit")} }), none, true},
		{"only failure excluded by name", ownOpen(func(p *api.PR) { p.Checks = []api.Check{failedCheck("flaky"), passedCheck("unit")} }), func(n string) bool { return n == "flaky" }, false},
		{"only failure excluded by workflow", ownOpen(func(p *api.PR) { p.Checks = []api.Check{{Name: "job", Workflow: "nightly", Outcome: api.CheckFailure}} }), func(n string) bool { return n == "nightly" }, false},
		{"excluded failure does not hide another failure", ownOpen(func(p *api.PR) { p.Checks = []api.Check{failedCheck("flaky"), failedCheck("unit")} }), func(n string) bool { return n == "flaky" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ciFailingForPR(tc.pr, attnSelf, tc.excluded); got != tc.want {
				t.Fatalf("ciFailingForPR = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCIExcluderFrom(t *testing.T) {
	ex := ciExcluderFrom(json.RawMessage(`{"ci_exclude":["^flaky-","[unclosed"]}`))
	if !ex("flaky-e2e") {
		t.Fatalf("flaky-e2e should be excluded")
	}
	if ex("unit") || ex("") {
		t.Fatalf("unit/empty should not be excluded")
	}
	if none := ciExcluderFrom(nil); none("anything") {
		t.Fatalf("no config excludes nothing")
	}
}

// ciGH answers the own-PR search with ownPRs and every other search with
// nothing, and GetPR per number from byNumber.
func ciGH(ownPRs []api.PR, byNumber map[int]api.PR) *fakeGH {
	return &fakeGH{
		viewerLogin: attnSelf,
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			if query == ownOpenPRsQuery {
				return ownPRs, nil
			}
			return nil, nil
		},
		getPRFn: func(ctx context.Context, repo string, number int) (*api.PR, error) {
			pr, ok := byNumber[number]
			if !ok {
				return nil, fmt.Errorf("unexpected GetPR %s#%d", repo, number)
			}
			return &pr, nil
		},
	}
}

func TestBackend_ListAttention_CIFailing_EmitsOnlyForFailedOwnOpenPRs(t *testing.T) {
	mk := func(n int, mutate func(*api.PR)) api.PR {
		pr := api.PR{
			Repo: "owner/repo", Number: n, Title: fmt.Sprintf("pr %d", n), State: "open",
			Author: attnSelf, URL: fmt.Sprintf("https://example.invalid/owner/repo/pull/%d", n),
		}
		mutate(&pr)
		return pr
	}
	byNumber := map[int]api.PR{
		1: mk(1, func(p *api.PR) { p.Checks = []api.Check{failedCheck("unit")} }),
		2: mk(2, func(p *api.PR) { p.Checks = []api.Check{pendingCheck("unit")} }),
		3: mk(3, func(p *api.PR) { p.Checks = []api.Check{passedCheck("unit")} }),
		4: mk(4, func(p *api.PR) { p.Draft = true; p.Checks = []api.Check{failedCheck("unit")} }),
		5: mk(5, func(p *api.PR) { p.State = "closed"; p.Checks = []api.Check{failedCheck("unit")} }),
		6: mk(6, func(p *api.PR) { p.State = "closed"; p.Merged = true; p.Checks = []api.Check{failedCheck("unit")} }),
	}
	var own []api.PR
	for n := 1; n <= 6; n++ {
		// The search layer reports draft/closed/merged PRs too; the
		// predicate (not only the query) must reject them.
		own = append(own, api.PR{Repo: "owner/repo", Number: n, Draft: byNumber[n].Draft})
	}
	b := New(ciGH(own, byNumber))
	got, err := b.ListAttention(context.Background())
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	it := got[0]
	if it.Type != attentionTypeCIFailing || it.Type == "pr" {
		t.Fatalf("Type = %q, want %q (distinct from review-needed \"pr\")", it.Type, attentionTypeCIFailing)
	}
	if it.ID != "owner/repo#1" {
		t.Fatalf("ID = %q, want owner/repo#1", it.ID)
	}
	if it.Severity != schema.SeverityHigh {
		t.Fatalf("Severity = %q, want high", it.Severity)
	}
	if it.URL != "https://example.invalid/owner/repo/pull/1" {
		t.Fatalf("URL = %q, want the PR's own url", it.URL)
	}
	if want := attentionReasonCIFailing + ": pr 1"; it.Summary != want {
		t.Fatalf("Summary = %q, want %q", it.Summary, want)
	}
}

func TestBackend_ListAttention_CIFailing_AppliesConfiguredExclusions(t *testing.T) {
	byNumber := map[int]api.PR{
		1: {Repo: "owner/repo", Number: 1, Title: "t", State: "open", Author: attnSelf, Checks: []api.Check{failedCheck("flaky-e2e")}},
	}
	b := New(ciGH([]api.PR{{Repo: "owner/repo", Number: 1}}, byNumber))
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"ci_exclude":["^flaky-"]}`))
	got, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("excluded-only failure should yield no item: %+v", got)
	}
}

// A PR that needs review AND whose CI is failing yields two items with the
// same id but distinct types, so attention list's {type, id} dedup keeps both.
func TestBackend_ListAttention_CIFailing_CoexistsWithReviewNeededItem(t *testing.T) {
	own := api.PR{Repo: "owner/repo", Number: 9, Title: "mine", State: "open", Author: attnSelf, HeadSHA: "h1", Checks: []api.Check{failedCheck("unit")}}
	gh := &fakeGH{
		viewerLogin: attnSelf,
		searchFn: func(ctx context.Context, query string) ([]api.PR, error) {
			return []api.PR{{Repo: "owner/repo", Number: 9}}, nil
		},
		getPRFn: func(ctx context.Context, repo string, number int) (*api.PR, error) { return &own, nil },
	}
	b := New(gh)
	ctx := scriptout.WithConfig(context.Background(), json.RawMessage(`{"attention_query":"is:open is:pr"}`))
	got, err := b.ListAttention(ctx)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[0].Type != "pr" || got[1].Type != attentionTypeCIFailing {
		t.Fatalf("types = %q, %q; want pr then %s", got[0].Type, got[1].Type, attentionTypeCIFailing)
	}
	if got[0].ID != got[1].ID {
		t.Fatalf("ids differ: %q vs %q", got[0].ID, got[1].ID)
	}
}
