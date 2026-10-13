// Package metered makes the ported GitHub Provider (internal/github) spend
// through the daemon's budget governor. The Provider runs gh through an injected
// runner and nothing meters those calls; Runner is the runner that does: it
// admits each call to the bucket the call draws on, runs it, books its spend and
// the rate-limit reading it carried, and hands the Provider exactly the bytes it
// expects (a response body, never the --include headers).
//
// # Routing
//
// The daemon MUST make every origin call under the governor's caps, reserve and
// pauses, and MUST read the rate headers of a REST call, which only
// `gh api --include` shows. How one invocation is metered (routeFor):
//
//	gh api graphql ...           GraphQL points  admitted at 1; the response's own
//	                             rateLimit { cost } is booked (Feed.SettleGraphQL)
//	gh api <endpoint> ...        REST core       through ratefeed.Feed (gh api --include):
//	                             admitted at 1, the body returned without headers
//	gh api search/...            REST search     the same
//	gh search prs ... --limit N  REST search     ceil(N / 100) requests, run as given
//	any other gh command         REST core       1 request, run as given
//
// A porcelain command shows no rate headers, so its estimate is what is booked.
// The same rules apply to RunStdin: a GraphQL mutation (`api graphql --input -`)
// passes through, and a REST write with a JSON body (`api repos/o/r/pulls/1/reviews
// --method POST --input -`) goes through ratefeed.Feed.RESTStdin.
//
// Every call is admitted with the class and purpose its context states
// (WithCall); a context that states none is an interactive call. A refusal is
// returned as the *governor.Refusal it is, with nothing run and nothing booked.
// The Provider retries a transient read by calling Run again, and each attempt
// passes admission again: no separate retry guard is installed.
//
// # Errors
//
// gh calls every HTTP 403 an authentication failure, and a rate limit is one.
// Every error the Inner returns therefore passes through Classify, which turns a
// rate-limit answer into a *ratefeed.RateLimitError plus a pause of the
// background classes. WireError maps what the Provider returns onto the closed
// seven-value wire taxonomy, and is the one mapping every op of the daemon uses.
package metered

import (
	"context"
	"errors"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Inner runs gh. *github.CLI, the token-protected gateway, satisfies it.
type Inner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
	RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

var _ Inner = (*github.CLI)(nil)

// DefaultFallbackPause is how long the background classes are paused when a
// rate-limit answer carries no Retry-After the runner can read, which is every
// GraphQL and porcelain answer: sixty seconds, long enough to ride out a burst
// limit and short enough that background work resumes promptly (the interactive
// classes are never paused).
const DefaultFallbackPause = 60 * time.Second

// Runner is the Provider's gh runner, metered. Governor and Feed MUST be the pair
// the daemon built together, and Feed MUST be built over the same raw Inner, never
// over the Runner itself (which would recurse through admission).
type Runner struct {
	// Inner runs gh once a call is admitted.
	Inner Inner
	// Governor admits every call and is paused by a rate-limit answer.
	Governor *governor.Governor
	// Feed runs and books the REST calls and books the GraphQL ones.
	Feed *ratefeed.Feed
	// FallbackPause is the pause for a rate-limit answer without a readable
	// Retry-After. Zero means DefaultFallbackPause.
	FallbackPause time.Duration
}

type callKey struct{}

// WithCall states what the calls made under the returned context are for: the
// priority class, the purpose, and the group and batch size the spend is
// recorded with.
func WithCall(ctx context.Context, m ratefeed.Meta) context.Context {
	return context.WithValue(ctx, callKey{}, m)
}

// CallFrom returns what WithCall stated for ctx. A context that states nothing is
// an interactive call: governor.ClassInteractive with governor.PurposeInteractive.
func CallFrom(ctx context.Context) ratefeed.Meta {
	if m, ok := ctx.Value(callKey{}).(ratefeed.Meta); ok {
		return m
	}
	return ratefeed.Meta{Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive}
}

// NewProvider returns the GitHub Provider over r with the default retry policy.
// Each attempt of a retried read is its own Run call, so its own admission.
func NewProvider(r *Runner) *github.Provider {
	return github.NewWithRunner(r).WithRetryPolicy(github.DefaultRetryPolicy())
}

// Run runs `gh <args...>` under the governor and returns its standard output.
func (r *Runner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.run(ctx, nil, false, args)
}

// RunStdin runs `gh <args...>` with stdin on its standard input, under the
// governor, and returns its standard output.
func (r *Runner) RunStdin(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return r.run(ctx, stdin, true, args)
}

// invoker runs gh through the Inner with the arguments it is given.
type invoker func(ctx context.Context, args ...string) ([]byte, error)

// viaFeedKey marks the context of a call the Feed is running on the Runner's
// behalf. A Feed built over the Runner itself would call back into it (admitting
// the same call again, forever); the marker turns that wiring mistake into an
// error instead of a stack overflow.
type viaFeedKey struct{}

var errFeedOverRunner = errors.New("metered: the Feed runs gh through the Runner itself; build the Feed over the raw Inner (the one the Runner runs)")

func (r *Runner) run(ctx context.Context, stdin []byte, withStdin bool, args []string) ([]byte, error) {
	if r == nil || r.Inner == nil || r.Governor == nil || r.Feed == nil {
		return nil, errors.New("metered: a Runner needs an Inner, a Governor and a Feed")
	}
	if ctx.Value(viaFeedKey{}) != nil {
		return nil, errFeedOverRunner
	}
	call := CallFrom(ctx)
	rt := routeFor(args)
	invoke := invoker(r.Inner.Run)
	if withStdin {
		invoke = func(ctx context.Context, args ...string) ([]byte, error) {
			return r.Inner.RunStdin(ctx, stdin, args...)
		}
	}

	var (
		out []byte
		err error
	)
	switch rt.kind {
	case kindGraphQL:
		out, err = r.graphQL(ctx, call, rt, invoke, args)
	case kindREST:
		out, err = r.rest(ctx, call, rt, stdin, withStdin)
	default:
		out, err = r.cli(ctx, call, rt, invoke, args)
	}
	return out, Classify(r.Governor, r.FallbackPause, err)
}

// graphQL admits one GraphQL call at its estimate, runs it as given and settles
// the ticket with what the response says it cost.
func (r *Runner) graphQL(ctx context.Context, call ratefeed.Meta, rt route, invoke invoker, args []string) ([]byte, error) {
	tk, err := r.Governor.Admit(governor.Request{Bucket: rt.bucket, Class: call.Class, Cost: rt.cost})
	if err != nil {
		return nil, err
	}
	out, runErr := invoke(ctx, args...)
	// A failure to persist the spend is the governor's to log; the call happened,
	// so its answer is still the caller's.
	_ = r.Feed.SettleGraphQL(tk, call, out, runErr)
	return out, runErr
}

// rest runs one REST call through the Feed, which admits it, adds --include,
// books the spend and the reading and pauses on a secondary limit. The Provider
// gets the body only; a status of 400 or more returns its body beside the error.
func (r *Runner) rest(ctx context.Context, call ratefeed.Meta, rt route, stdin []byte, withStdin bool) ([]byte, error) {
	rc := ratefeed.RESTCall{Meta: call, Args: rt.restArgs}
	ctx = context.WithValue(ctx, viaFeedKey{}, true)
	var (
		resp *ratefeed.Response
		err  error
	)
	if withStdin {
		resp, err = r.Feed.RESTStdin(ctx, rc, stdin)
	} else {
		resp, err = r.Feed.REST(ctx, rc)
	}
	if resp == nil {
		return nil, err
	}
	return resp.Body, err
}

// cli admits one porcelain call at its estimate and runs it as given. Its
// headers are invisible, so the estimate is what is booked. A call that never
// reached GitHub (gh refused to start without a credential) releases its
// reservation instead.
func (r *Runner) cli(ctx context.Context, call ratefeed.Meta, rt route, invoke invoker, args []string) ([]byte, error) {
	tk, err := r.Governor.Admit(governor.Request{Bucket: rt.bucket, Class: call.Class, Cost: rt.cost})
	if err != nil {
		return nil, err
	}
	out, runErr := invoke(ctx, args...)
	outcome := ratefeed.OutcomeOK
	switch {
	case runErr == nil:
	case IsRateLimit(runErr):
		outcome = ratefeed.OutcomeRateLimited
	case len(out) == 0 && errors.Is(runErr, github.ErrGHAuthInvalid):
		tk.Release()
		return out, runErr
	default:
		outcome = ratefeed.OutcomeError
	}
	_ = tk.Settle(governor.Spend{
		Purpose: call.Purpose, Group: call.Group, BatchSize: call.BatchSize,
		Cost: rt.cost, Outcome: outcome,
	})
	return out, runErr
}
