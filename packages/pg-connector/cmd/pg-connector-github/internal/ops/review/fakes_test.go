package review

import (
	"context"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/github"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/metered"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
)

// fakeGH is the Provider over a fakeHost. It records the order of the host calls
// (ops) and the class and purpose each call was made under (metas). The
// primitives are in submit_test.go, next to the simulation they drive.
type fakeGH struct {
	host  *fakeHost
	ops   []string
	metas []ratefeed.Meta
}

var _ Provider = (*fakeGH)(nil)

// note records one host call and the call metadata its context carried.
func (f *fakeGH) note(ctx context.Context, op string) {
	f.ops = append(f.ops, op)
	f.metas = append(f.metas, metered.CallFrom(ctx))
}

func (f *fakeGH) GetPendingReview(ctx context.Context, repo string, number int) (*github.PendingReviewData, error) {
	f.note(ctx, "lookup")
	return f.host.snapshot()
}

// fakeEngine is an Engine that serializes per id, waiting no longer than the context allows, and records
// the invalidations it was asked for.
type fakeEngine struct {
	mu          sync.Mutex
	locks       map[string]chan struct{}
	invalidated []string // "<id> <group>"
	failInval   error
}

var _ Engine = (*fakeEngine)(nil)

func (e *fakeEngine) SerializeWrite(ctx context.Context, id string, fn func(ctx context.Context) error) error {
	e.mu.Lock()
	if e.locks == nil {
		e.locks = map[string]chan struct{}{}
	}
	l := e.locks[id]
	if l == nil {
		l = make(chan struct{}, 1)
		e.locks[id] = l
	}
	e.mu.Unlock()
	select {
	case l <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-l }()
	return fn(ctx)
}

func (e *fakeEngine) InvalidateAndQueue(_ context.Context, id, group string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.invalidated = append(e.invalidated, id+" "+group)
	return e.failInval
}

func (e *fakeEngine) invalidations() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.invalidated...)
}
