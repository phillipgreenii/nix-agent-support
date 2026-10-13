// Package groupsvctest is the test support of internal/groupsvc and of the group
// packets built on it: a counting fake groups.Fetcher, a fake origin for the
// summary group, a fake first-sighting resolver and a Rig that wires a real
// store, scheduler, governor clock and summary service around them. Nothing here
// is used outside tests.
package groupsvctest

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Call is one Fetch call the fake saw.
type Call struct {
	Group     string
	Targets   []groups.Target
	Class     governor.Class
	Purpose   governor.Purpose
	BatchSize int
}

// NodeIDs returns the node ids of the call's targets.
func (c Call) NodeIDs() []string {
	out := make([]string, len(c.Targets))
	for i, t := range c.Targets {
		out[i] = t.NodeID
	}
	return out
}

// Fetcher is a counting fake groups.Fetcher. By default it answers every target
// with a small JSON document that changes on every call, stamped with the
// target's head and base. It can block, fail, say a PR is gone, and admit and
// settle one GraphQL point per call through a governor as the real fetchers do.
type Fetcher struct {
	// Name is the group the fake produces.
	Name string
	// Batch is MaxBatch; zero means 74.
	Batch int
	// Gov, when set, admits and settles one GraphQL point per Fetch call as the
	// call's class; a refusal fails every target of the call with it.
	Gov *governor.Governor

	mu      sync.Mutex
	cond    *sync.Cond
	calls   []Call
	started int
	seq     int

	gate    chan struct{}
	barrier *Barrier
	err     error
	gone    map[string]bool
	lastErr string
	outcome func(t groups.Target, n int) groups.Outcome
	ctxErr  error
}

// NewFetcher returns a fake for the group.
func NewFetcher(group string) *Fetcher {
	f := &Fetcher{Name: group, gone: map[string]bool{}}
	f.cond = sync.NewCond(&f.mu)
	return f
}

// Group implements groups.Fetcher.
func (f *Fetcher) Group() string { return f.Name }

// MaxBatch implements groups.Fetcher.
func (f *Fetcher) MaxBatch() int {
	if f.Batch > 0 {
		return f.Batch
	}
	return 74
}

// Spend implements groups.Fetcher: one GraphQL point per call.
func (f *Fetcher) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Bucket: governor.BucketGraphQL, Class: class, Cost: 1}
}

// Fetch implements groups.Fetcher.
func (f *Fetcher) Fetch(ctx context.Context, call ratefeed.Meta, targets []groups.Target) []groups.Outcome {
	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Group: f.Name, Targets: slices.Clone(targets), Class: call.Class, Purpose: call.Purpose, BatchSize: call.BatchSize,
	})
	f.started++
	n := f.started
	f.cond.Broadcast()
	barrier, gate, ferr, lastErr, outcome := f.barrier, f.gate, f.err, f.lastErr, f.outcome
	f.mu.Unlock()
	if barrier != nil {
		barrier.arrive()
	}

	if f.Gov != nil {
		tk, err := f.Gov.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1})
		if err != nil {
			f.mu.Lock()
			f.calls = f.calls[:len(f.calls)-1] // refused: nothing was fetched
			f.started--
			f.mu.Unlock()
			return failAll(len(targets), err)
		}
		_ = tk.Settle(governor.Spend{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1, Outcome: "ok"})
	}
	if gate != nil {
		<-gate
		f.mu.Lock()
		f.ctxErr = ctx.Err()
		f.mu.Unlock()
	}
	if ferr != nil {
		return failAll(len(targets), ferr)
	}
	out := make([]groups.Outcome, len(targets))
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range targets {
		switch {
		case f.gone[t.NodeID]:
			out[i] = groups.Outcome{Err: groups.ErrGone}
		case outcome != nil:
			out[i] = outcome(t, n)
		default:
			f.seq++
			content, _ := json.Marshal(map[string]any{
				"group": f.Name, "node": t.NodeID, "head": t.Stamps.Head, "base": t.Stamps.Base, "fetch": f.seq,
			})
			out[i] = groups.Outcome{Content: content, Stamps: t.Stamps, Cost: 1, LastError: lastErr}
		}
	}
	return out
}

func failAll(n int, err error) []groups.Outcome {
	out := make([]groups.Outcome, n)
	for i := range out {
		out[i] = groups.Outcome{Err: err}
	}
	return out
}

// Calls returns a copy of the calls seen so far.
func (f *Fetcher) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// CallCount is the number of Fetch calls seen so far.
func (f *Fetcher) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TargetCount is the number of targets fetched so far, over every call.
func (f *Fetcher) TargetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += len(c.Targets)
	}
	return n
}

// Block makes every later Fetch call wait inside the call until the returned
// release function runs (it is safe to call more than once).
func (f *Fetcher) Block() (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// Barrier is a rendezvous shared by several fakes: a Fetch call joined to it
// waits until n calls (across every fake joined) have arrived. It proves that
// several groups are fetched in parallel, because a serial engine never lets the
// second call start while the first waits.
type Barrier struct {
	mu      sync.Mutex
	cond    *sync.Cond
	n       int
	arrived int
}

// NewBarrier returns a barrier that opens when n calls have arrived.
func NewBarrier(n int) *Barrier {
	b := &Barrier{n: n}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *Barrier) arrive() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.arrived++
	b.cond.Broadcast()
	for b.arrived < b.n {
		b.cond.Wait()
	}
}

// Join makes every later Fetch call of the fake wait at the barrier (nil lifts
// it).
func (f *Fetcher) Join(b *Barrier) {
	f.mu.Lock()
	f.barrier = b
	f.mu.Unlock()
}

// CtxErr is the context error the last unblocked Fetch call saw, to show that a
// caller's deadline never reaches a fetch.
func (f *Fetcher) CtxErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxErr
}

// WaitStarted blocks until at least n calls have started, or the timeout passes;
// it reports whether they did.
func (f *Fetcher) WaitStarted(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.started < n {
		if time.Until(deadline) <= 0 {
			return false
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-time.After(10 * time.Millisecond):
			case <-done:
			}
			f.mu.Lock()
			f.cond.Broadcast()
			f.mu.Unlock()
		}()
		f.cond.Wait()
		close(done)
	}
	return true
}

// FailWith makes every later call fail with err (nil lifts it).
func (f *Fetcher) FailWith(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// SetGone makes the node id answer groups.ErrGone (or stop doing so).
func (f *Fetcher) SetGone(nodeID string, gone bool) {
	f.mu.Lock()
	f.gone[nodeID] = gone
	f.mu.Unlock()
}

// SetLastError makes later successful outcomes carry a non-fatal LastError.
func (f *Fetcher) SetLastError(s string) {
	f.mu.Lock()
	f.lastErr = s
	f.mu.Unlock()
}

// SetOutcome replaces the default answer; n is the 1-based number of the call.
func (f *Fetcher) SetOutcome(fn func(t groups.Target, n int) groups.Outcome) {
	f.mu.Lock()
	f.outcome = fn
	f.mu.Unlock()
}

// String describes the fake for failure messages.
func (f *Fetcher) String() string {
	return fmt.Sprintf("fake %s fetcher (%d calls)", f.Name, f.CallCount())
}
