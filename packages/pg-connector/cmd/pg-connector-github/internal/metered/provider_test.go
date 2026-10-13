package metered

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The conversation documents of internal/github select `rateLimit { cost }` and
// nothing else, so these canned pages carry exactly that.
const (
	threadsPage1 = `{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"reviewThreads":{"totalCount":2,` +
		`"pageInfo":{"hasNextPage":true,"endCursor":"T1"},"nodes":[{"id":"PRRT_1","isResolved":false,"isOutdated":false,` +
		`"comments":{"totalCount":1,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PRRC_1",` +
		`"author":{"login":"example-user"},"authorAssociation":"MEMBER","body":"nit","path":"a.go","line":3,"originalLine":3,` +
		`"createdAt":"2026-10-12T10:00:00Z","updatedAt":"2026-10-12T10:00:00Z","isMinimized":false,"minimizedReason":"",` +
		`"originalCommit":{"oid":"abc123"},"pullRequestReview":{"id":"PRR_1"}}]}}]}}}}}`
	threadsPage2 = `{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"reviewThreads":{"totalCount":2,` +
		`"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PRRT_2","isResolved":true,"isOutdated":false,` +
		`"comments":{"totalCount":0,"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}]}}}}}`
	issuePage1 = `{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"comments":{"totalCount":2,` +
		`"pageInfo":{"hasNextPage":true,"endCursor":"I1"},"nodes":[{"id":"IC_1","author":{"login":"example-user"},` +
		`"authorAssociation":"OWNER","body":"first","createdAt":"2026-10-12T09:00:00Z","updatedAt":"2026-10-12T09:00:00Z"}]}}}}}`
	issuePage2 = `{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"comments":{"totalCount":2,` +
		`"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"IC_2","author":{"login":"example-user"},` +
		`"authorAssociation":"OWNER","body":"second","createdAt":"2026-10-12T11:00:00Z","updatedAt":"2026-10-12T11:00:00Z"}]}}}}}`

	reviewsPage = `{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":{"reviews":{"totalCount":0,` +
		`"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`
)

// conversationPages serves ListCommentsReport's four pages: two pages of review
// threads, then two of issue comments, told apart by the document and its cursor.
func conversationPages(c innerCall) ([]byte, error) {
	line := c.line()
	switch {
	case strings.Contains(line, "reviewThreads(first") && !strings.Contains(line, "after=T1"):
		return []byte(threadsPage1), nil
	case strings.Contains(line, "reviewThreads(first"):
		return []byte(threadsPage2), nil
	case !strings.Contains(line, "after="):
		return []byte(issuePage1), nil
	default:
		return []byte(issuePage2), nil
	}
}

// Each page a paged read fetches is its own admission, booked with the class,
// purpose and group the caller stated, and the cost-only selection of these
// documents books its cost without becoming a reading of zero that would hold the
// next page below the reserve.
func TestNewProviderAdmitsAndBooksEveryPageOfAPagedRead(t *testing.T) {
	h := newHarness(t, conversationPages)
	p := NewProvider(h.runner)
	ctx := WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeRefresh, Group: "conversation", BatchSize: 1})

	res, err := p.ListCommentsReport(ctx, "o/r", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Comments) != 3 {
		t.Errorf("got %d comments, want the thread comment and both issue comments", len(res.Comments))
	}
	// Four pages: the call count equals the admission count equals the booking count.
	if h.inner.count() != 4 || len(h.events) != 4 {
		t.Fatalf("%d gh calls and %d booked spends, want 4 and 4", h.inner.count(), len(h.events))
	}
	for i, e := range h.events {
		if e.Bucket != governor.BucketGraphQL || e.Class != governor.ClassInvalidated || e.Purpose != governor.PurposeRefresh ||
			e.Group != "conversation" || e.BatchSize != 1 || e.Cost != 1 || e.Outcome != ratefeed.OutcomeOK {
			t.Errorf("spend %d = %+v", i, e)
		}
	}
	bs := h.bucket(governor.BucketGraphQL)
	if bs.Spent != 4 || bs.SpentByPurpose[governor.PurposeRefresh] != 4 || bs.InFlight != 0 || bs.Remaining != -1 {
		t.Errorf("graphql = %+v, want 4 points spent as refresh, nothing reserved, and no reading invented", bs)
	}
	// Every page went through gh api graphql with its document, as the Provider built it.
	for i, c := range h.inner.calls {
		if len(c.args) < 2 || c.args[0] != "api" || c.args[1] != "graphql" || c.hasStdin {
			t.Errorf("call %d = %q", i, c.line())
		}
	}
}

func TestNewProviderStopsPagingWhenTheGovernorRefusesAPage(t *testing.T) {
	h := newHarness(t, conversationPages)
	p := NewProvider(h.runner)
	ctx := WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh, Group: "conversation", BatchSize: 1})
	// Room for exactly the first page of background spend.
	spend(h, governor.BucketGraphQL, governor.ClassQuery, h.bucket(governor.BucketGraphQL).BackgroundCap-1)

	_, err := p.ListCommentsReport(ctx, "o/r", 5)
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != governor.RefusedBackgroundShare {
		t.Fatalf("err = %v, want the background-share refusal of the second page", err)
	}
	if h.inner.count() != 1 {
		t.Errorf("gh ran %d times, want only the page that was admitted", h.inner.count())
	}
	if got := WireError(err); !errors.Is(got, scriptout.ErrUnavailable) || !strings.Contains(got.Error(), "background share") {
		t.Errorf("WireError = %v, want unavailable naming the background share", got)
	}
}

// A transient failure the Provider retries is admitted again for each attempt:
// every attempt is its own Run call, so its own admission and its own booking.
func TestEachRetryAttemptOfAReadIsAdmittedAgain(t *testing.T) {
	transient := errors.New("gh api graphql: exit status 1: error connecting to api.github.com")
	h := newHarness(t, nil)
	h.inner.reply = func(c innerCall) ([]byte, error) {
		if h.inner.count() < 3 {
			return nil, transient
		}
		return []byte(reviewsPage), nil
	}
	p := NewProvider(h.runner).WithRetryPolicy(github.RetryPolicy{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	ctx := WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassInvalidated, Purpose: governor.PurposeRefresh, Group: "detail", BatchSize: 1})

	if _, err := p.ListReviewsReport(ctx, "o/r", 5); err != nil {
		t.Fatal(err)
	}
	if h.inner.count() != 3 || len(h.events) != 3 {
		t.Fatalf("%d gh calls and %d booked spends, want one admission and booking per attempt", h.inner.count(), len(h.events))
	}
	if outcomes := []string{h.events[0].Outcome, h.events[1].Outcome, h.events[2].Outcome}; outcomes[0] != ratefeed.OutcomeError || outcomes[1] != ratefeed.OutcomeError || outcomes[2] != ratefeed.OutcomeOK {
		t.Errorf("outcomes = %v, want the two failed attempts booked as errors (a request that reached GitHub may be charged) and the third as ok", outcomes)
	}
	if bs := h.bucket(governor.BucketGraphQL); bs.Spent != 3 || bs.InFlight != 0 {
		t.Errorf("graphql = %+v, want 3 points", bs)
	}
}

// The retry's admission can refuse: a retry is more spend, and it passes the
// governor again like any other call.
func TestARetryTheGovernorRefusesIsNotRun(t *testing.T) {
	transient := errors.New("gh api graphql: exit status 1: error connecting to api.github.com")
	h := newHarness(t, nil)
	h.inner.reply = func(innerCall) ([]byte, error) {
		// Something else pauses the background while the first attempt is failing.
		h.gov.PauseAll(time.Minute, "secondary rate limit (HTTP 403, retry after 1m0s)")
		return nil, transient
	}
	p := NewProvider(h.runner).WithRetryPolicy(github.RetryPolicy{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	ctx := WithCall(context.Background(), ratefeed.Meta{Class: governor.ClassKeepAlive, Purpose: governor.PurposeRefresh, Group: "detail"})

	_, err := p.ListReviewsReport(ctx, "o/r", 5)
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != governor.RefusedPaused {
		t.Fatalf("err = %v, want the paused refusal of the second attempt", err)
	}
	if h.inner.count() != 1 {
		t.Errorf("gh ran %d times: the refused retry must not run", h.inner.count())
	}
}

// NewProvider installs the Provider's default retry policy: a read that fails
// transiently once is retried (and admitted again), where NewWithRunner alone
// would make a single attempt.
func TestNewProviderRetriesATransientReadUnderTheDefaultPolicy(t *testing.T) {
	// The default policy's first backoff is a real 500ms sleep.
	h := newHarness(t, nil)
	h.inner.reply = func(innerCall) ([]byte, error) {
		if h.inner.count() == 1 {
			return nil, errors.New("gh api graphql: exit status 1: error connecting to api.github.com")
		}
		return []byte(reviewsPage), nil
	}
	if _, err := NewProvider(h.runner).ListReviewsReport(context.Background(), "o/r", 5); err != nil {
		t.Fatal(err)
	}
	if h.inner.count() != 2 || len(h.events) != 2 {
		t.Errorf("%d gh calls and %d booked spends, want a retry that was admitted again", h.inner.count(), len(h.events))
	}

	// The same read through a bare Provider makes the one attempt.
	h2 := newHarness(t, func(innerCall) ([]byte, error) {
		return nil, errors.New("gh api graphql: exit status 1: error connecting to api.github.com")
	})
	if _, err := github.NewWithRunner(h2.runner).ListReviewsReport(context.Background(), "o/r", 5); err == nil || h2.inner.count() != 1 {
		t.Errorf("a bare Provider: err %v after %d calls, want the failure after exactly one", err, h2.inner.count())
	}
}

// A rate-limit answer is final for the Provider's own retry (nothing is retried
// into a limit), reaches the caller as the typed error rather than as an
// authentication failure, and maps to unavailable on the wire.
func TestAProviderReadSeesARateLimitAsTheTypedErrorNotAnAuthFailure(t *testing.T) {
	h := newHarness(t, func(c innerCall) ([]byte, error) { return nil, secondaryLimitText(c.args...) })
	h.runner.FallbackPause = 30 * time.Second
	p := NewProvider(h.runner).WithRetryPolicy(github.RetryPolicy{
		MaxAttempts: 3,
		Backoff:     func(int) time.Duration { return 0 },
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	_, err := p.ListReviewsReport(context.Background(), "o/r", 5)
	var rl *ratefeed.RateLimitError
	if !errors.As(err, &rl) || !rl.Secondary || rl.RetryAfter != 30*time.Second {
		t.Fatalf("err = %v, want the typed secondary error", err)
	}
	if errors.Is(err, github.ErrGHAuthInvalid) {
		t.Error("the Provider's caller sees an authentication failure")
	}
	if h.inner.count() != 1 {
		t.Errorf("gh ran %d times: a rate limit must not be retried", h.inner.count())
	}
	if got := WireError(err); !errors.Is(got, scriptout.ErrUnavailable) {
		t.Errorf("WireError = %v, want unavailable", got)
	}
}
