package github

import (
	"context"
	"errors"
)

// RunFunc runs `gh <args...>` and returns its standard output.
type RunFunc interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// RetryRunner is a read-only gh runner that retries transient failures with the
// package's bounded policy (retry.go): the daemon's fetchers call gh through it,
// so a dropped connection or a 502 from the edge is absorbed by the existing
// three attempts instead of failing a refresh. It never retries a failure
// the policy calls final (auth, 4xx, rate limit, not found), and a guard runs
// before every retry so a retry that would spend past the budget is abandoned.
//
// It is for reads only: a write must never be replayed.
type RetryRunner struct{ p *Provider }

// NewRetryRunner returns a RetryRunner over inner. guard, when non-nil, runs
// before every retry (Provider.SetRetryGuard); a non-nil error from it ends the
// retrying.
func NewRetryRunner(inner RunFunc, policy RetryPolicy, guard func(ctx context.Context) error) *RetryRunner {
	p := NewWithRunner(readOnlyRunner{inner}).WithRetryPolicy(policy)
	p.SetRetryGuard(guard)
	return &RetryRunner{p: p}
}

// Run runs `gh <args...>`, retrying a transient failure.
func (r *RetryRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.p.runRead(ctx, readOpts{}, args...)
}

// readOnlyRunner adapts a Run-only runner to ghRunner; stdin writes are not part
// of a read and are refused.
type readOnlyRunner struct{ RunFunc }

func (readOnlyRunner) RunStdin(context.Context, []byte, ...string) ([]byte, error) {
	return nil, errors.New("github: RetryRunner is read-only and does not feed stdin")
}
