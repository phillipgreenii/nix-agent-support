// Package ratefeed is the GitHub side of the daemon's budget governor
// (pkg/connectord/governor): it turns what GitHub reports about its rate limits
// into the governor's readings, and runs REST calls through `gh api --include`
// so the X-RateLimit-* and Retry-After headers can be read at all (`gh run list`
// and the other porcelain commands expose none).
//
// # Where the readings come from
//
//   - GraphQL: every document this binary owns folds `rateLimit { cost
//     remaining resetAt }` into its own request. ParseGraphQL reads that
//     selection with the helper the github package already carries
//     (github.FoldedRateLimit), so there is one definition of its wire shape.
//   - REST: Feed.REST runs `gh api --include <args>` through the same token-
//     protected gh gateway as every other call (github.CLI, which takes the
//     configured gh path and host), splits the response into status, headers
//     and body (ParseResponse), and reads the headers (ParseHeaders).
//
// # What it does with them
//
// A reading goes to the governor with the call's spend (Feed.SettleGraphQL,
// Feed.REST). A 403 or 429 that carries Retry-After is a secondary limit: it
// pauses the background classes of every bucket for exactly the stated time
// (governor.PauseAll) and surfaces as a *RateLimitError, which does NOT wrap the
// auth sentinel even though gh's own classifier calls every HTTP 403 an auth
// failure.
//
// Retry of transient failures stays with the caller, and each attempt is one more
// spend that MUST pass admission again: Feed.RetryGuard is the check to install
// as github.Provider.SetRetryGuard for GraphQL reads, and a REST caller asks
// governor.Admit before every attempt (Feed.REST does so itself).
package ratefeed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// GovernorConfig maps the daemon's settings (the budget rows of its Defaults
// table) onto the governor's: the GraphQL cap and the reserve, the REST caps and
// the background share. The reserve guards the point-metered bucket; the REST
// caps already leave room for other users of the token, so they carry none.
func GovernorConfig(s config.Settings) governor.Config {
	return governor.Config{
		GraphQL:         governor.Limits{Cap: int64(s.GraphQLPointsPerHour), Reserve: int64(s.RateReservePoints)},
		RESTCore:        governor.Limits{Cap: int64(s.RESTRequestsPerHour)},
		RESTSearch:      governor.Limits{Cap: int64(s.RESTSearchPerMinute)},
		BackgroundShare: s.BackgroundShare,
	}
}

// Runner runs `gh <args...>` and returns its standard output, even when gh
// exits non-zero (an HTTP error status prints its response before it fails).
// *github.CLI satisfies it.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

var _ Runner = (*github.CLI)(nil)

// Meta says what a call is for; the scheduler or query runner that makes it
// supplies it.
type Meta struct {
	Class     governor.Class
	Purpose   governor.Purpose
	Group     string
	BatchSize int
}

// Feed joins the GitHub parsing to a governor.
type Feed struct {
	gov   *governor.Governor
	gh    Runner
	clock governor.Clock
}

// New returns a Feed over gov that runs REST calls through gh. A nil clock means
// the system clock.
func New(gov *governor.Governor, gh Runner, clock governor.Clock) *Feed {
	if clock == nil {
		clock = governor.SystemClock{}
	}
	return &Feed{gov: gov, gh: gh, clock: clock}
}

// Outcome words the spend observer sees.
const (
	OutcomeOK          = "ok"
	OutcomeError       = "error"
	OutcomeRateLimited = "rate_limited"
)

// RetryGuard returns the check github.Provider.SetRetryGuard runs before every
// retry of a read: a retry is more spend, so it must pass the governor again. It
// reserves nothing; the retried call settles its own ticket.
func (f *Feed) RetryGuard(class governor.Class, bucket governor.Bucket, cost int64) func(context.Context) error {
	return func(context.Context) error {
		return f.gov.Check(governor.Request{Bucket: bucket, Class: class, Cost: cost})
	}
}

// SettleGraphQL books a finished GraphQL call against its ticket. The cost and
// the reading come from the rateLimit selection folded into the response; a
// response without one (an error before GitHub answered with data) books the
// ticket's estimate, because a request that reached GitHub may have been
// charged. A call that never ran (gh refused to start without a credential)
// releases the ticket instead. callErr is the call's own error, if any; it only
// colours the outcome.
func (f *Feed) SettleGraphQL(t *governor.Ticket, m Meta, body []byte, callErr error) error {
	if callErr != nil && len(body) == 0 && errors.Is(callErr, github.ErrGHAuthInvalid) {
		t.Release()
		return nil
	}
	outcome := OutcomeOK
	if callErr != nil {
		outcome = OutcomeError
	}
	s := governor.Spend{
		Purpose: m.Purpose, Group: m.Group, BatchSize: m.BatchSize,
		Cost: t.Request().Cost, Outcome: outcome,
	}
	if rd, found, err := ParseGraphQL(body); err == nil && found {
		s.Cost = rd.Cost
		s.Reading = &rd.Reading
	}
	return t.Settle(s)
}

// RESTCall is one REST read.
type RESTCall struct {
	Meta
	// Args are the arguments after `api --include`: the endpoint, then any
	// -f/-F/--method flags. The endpoint decides the bucket (see BucketFor).
	Args []string
}

// BucketFor names the bucket a REST endpoint draws on: search endpoints
// (`search/...`) draw on the search bucket, everything else on core.
func BucketFor(args []string) governor.Bucket {
	for _, a := range args {
		// Only an endpoint starts with "search/"; a flag value such as
		// "q=search/x" or "Accept:application/json" never does.
		if strings.HasPrefix(strings.TrimPrefix(a, "/"), "search/") {
			return governor.BucketRESTSearch
		}
	}
	return governor.BucketRESTCore
}

// RateLimitError is a rate-limit answer from the origin. It deliberately does
// not wrap gh's own error: gh's classifier calls every HTTP 403 an
// authentication failure, and a rate limit is not one (the secondary-limit row
// of the error table serves stale; the auth row answers unauthenticated).
type RateLimitError struct {
	StatusCode int
	// Secondary is true for a 403 or 429 that carried Retry-After; the
	// background classes are paused for RetryAfter. False is the primary limit
	// (nothing remaining), which resets at ResetAt.
	Secondary  bool
	RetryAfter time.Duration
	ResetAt    time.Time
	Resource   string
}

// Error describes the limit.
func (e *RateLimitError) Error() string {
	if e.Secondary {
		return fmt.Sprintf("github: secondary rate limit (HTTP %d): retry after %s", e.StatusCode, e.RetryAfter)
	}
	return fmt.Sprintf("github: primary rate limit exhausted (HTTP %d, resource %q): resets at %s",
		e.StatusCode, e.Resource, e.ResetAt.UTC().Format(time.RFC3339))
}

// REST admits one REST call, runs it through `gh api --include`, books its spend
// and reading, and pauses the background on a secondary limit.
//
// The error is a *governor.Refusal when admission was refused (nothing ran), a
// *RateLimitError for a primary or secondary limit, and otherwise gh's own error;
// the Response is returned alongside every error that carried one, so a caller
// can still read an error status's body. A call that never reached GitHub
// releases its reservation and spends nothing.
func (f *Feed) REST(ctx context.Context, call RESTCall) (*Response, error) {
	bucket := BucketFor(call.Args)
	tk, err := f.gov.Admit(governor.Request{Bucket: bucket, Class: call.Class, Cost: 1})
	if err != nil {
		return nil, err
	}
	args := append([]string{"api", "--include"}, call.Args...)
	out, runErr := f.gh.Run(ctx, args...)
	resp, perr := ParseResponse(out)
	if perr != nil {
		tk.Release()
		if runErr != nil {
			return nil, runErr
		}
		return nil, perr
	}

	hd := ParseHeaders(resp.Header, f.clock.Now())
	spend := governor.Spend{
		Bucket: bucket, Purpose: call.Purpose, Group: call.Group, BatchSize: call.BatchSize,
		Cost: 1, Outcome: OutcomeOK,
	}
	if rd, ok := hd.Reading(bucket); ok {
		spend.Bucket = rd.Bucket
		spend.Reading = &rd
	}

	var result error = runErr
	if wait, secondary := SecondaryLimit(resp.StatusCode, hd); secondary {
		f.gov.PauseAll(wait, fmt.Sprintf("secondary rate limit (HTTP %d, retry after %s)", resp.StatusCode, wait))
		spend.Outcome = OutcomeRateLimited
		result = &RateLimitError{StatusCode: resp.StatusCode, Secondary: true, RetryAfter: wait, Resource: hd.Resource}
	} else if (resp.StatusCode == 403 || resp.StatusCode == 429) && hd.Remaining == 0 {
		spend.Outcome = OutcomeRateLimited
		result = &RateLimitError{StatusCode: resp.StatusCode, ResetAt: hd.Reset, Resource: hd.Resource}
	} else if resp.StatusCode >= 400 || runErr != nil {
		spend.Outcome = OutcomeError
	}
	// A failure to persist the spend is the governor's to log
	// ("budget_persist_failed"); the call happened, so its answer is still the
	// caller's.
	_ = tk.Settle(spend)
	return resp, result
}
