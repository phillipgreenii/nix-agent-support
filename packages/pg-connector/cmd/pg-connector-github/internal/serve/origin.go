package serve

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// origin is the daemon's one way to reach GitHub: every fetcher (viewer,
// query runner, summary) calls gh through it. It does three things the
// fetchers cannot do for themselves.
//
//   - It asks for the response headers (`gh api --include`) and hands the
//     caller only the body, as a plain `gh api` would, so a fetcher's own
//     parsing is unchanged.
//   - It turns a rate-limit answer into what the error table says. gh calls
//     every HTTP 403 an authentication failure, and a GraphQL call otherwise
//     never shows its Retry-After header, so a secondary limit would read as
//     "unauthenticated" and never pause the background. A 403 or 429 with
//     Retry-After pauses the background classes for exactly the stated time
//     (governor.PauseAll) and becomes a *ratefeed.RateLimitError; a 403 or 429
//     with nothing remaining is the primary limit, whose reading is fed to the
//     governor and which becomes a *ratefeed.RateLimitError too. Neither wraps
//     the auth sentinel.
//   - It retries a transient failure (a 5xx or a dropped connection) with the
//     existing bounded policy (three attempts, 500 ms then 1 s), after the
//     governor says one more spend is allowed.
type origin struct {
	run   github.RunFunc
	gov   *governor.Governor
	clock governor.Clock
}

// newOriginOnly returns the origin over gh WITHOUT the retrying runner, for a
// fetcher that retries and falls back by itself (the detail group's): the
// runner would retry a batch 502 first and the per-PR retries would multiply.
func newOriginOnly(gh github.RunFunc, gov *governor.Governor, clock governor.Clock) *origin {
	if clock == nil {
		clock = governor.SystemClock{}
	}
	return &origin{run: gh, gov: gov, clock: clock}
}

// newOrigin returns the origin over gh (the token-protected github.CLI, or a
// stand-in in a test).
func newOrigin(gh github.RunFunc, gov *governor.Governor, clock governor.Clock, policy github.RetryPolicy) *github.RetryRunner {
	if clock == nil {
		clock = governor.SystemClock{}
	}
	o := &origin{run: gh, gov: gov, clock: clock}
	guard := func(ctx context.Context) error {
		// A retry is one more spend: ask as the highest class, which only the
		// cap and the reserve can refuse.
		return gov.Check(governor.Request{Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive, Cost: 1})
	}
	return github.NewRetryRunner(o, policy, guard)
}

// Run runs `gh <args...>`; an `api` call carries --include and its answer is
// reduced to the body.
func (o *origin) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 || args[0] != "api" {
		return o.run.Run(ctx, args...)
	}
	withHeaders := append([]string{"api", "--include"}, args[1:]...)
	out, err := o.run.Run(ctx, withHeaders...)
	resp, perr := ratefeed.ParseResponse(out)
	if perr != nil {
		// No HTTP response in the output: gh failed before a request reached
		// the origin (no credential, no binary), and its own error stands.
		return out, err
	}
	hd := ratefeed.ParseHeaders(resp.Header, o.clock.Now())
	if wait, secondary := ratefeed.SecondaryLimit(resp.StatusCode, hd); secondary {
		o.gov.PauseAll(wait, fmt.Sprintf("secondary rate limit (HTTP %d, retry after %s)", resp.StatusCode, wait))
		return resp.Body, &ratefeed.RateLimitError{StatusCode: resp.StatusCode, Secondary: true, RetryAfter: wait, Resource: hd.Resource}
	}
	if (resp.StatusCode == 403 || resp.StatusCode == 429) && hd.Remaining == 0 {
		if rd, ok := hd.Reading(governor.BucketGraphQL); ok {
			_ = o.gov.Observe(rd)
		}
		return resp.Body, &ratefeed.RateLimitError{StatusCode: resp.StatusCode, ResetAt: hd.Reset, Resource: hd.Resource}
	}
	return resp.Body, err
}
