package groupsvctest

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// PR is one pull request of the fake origin.
type PR struct {
	Repo        string // nameWithOwner
	Number      int
	State       string
	HeadRefOid  string
	BaseRefOid  string
	HeadRefName string
	Gone        bool
}

// OriginCall is one `nodes(ids:)` call the fake origin saw.
type OriginCall struct {
	IDs   []string
	Class governor.Class
}

// Origin is a fake summary.BatchFetcher over a map of pull requests. A node id
// it was not told about is created on first sight as an open PR of a generic
// repository with head-1 and base-1.
type Origin struct {
	// Gov, when set, admits and settles one GraphQL point per call, as the real
	// summary fetcher does.
	Gov *governor.Governor
	// Latency, when set, runs inside every Fetch call after the call was
	// admitted and booked and before it answers: a test that models the origin's
	// round trip on a fake clock blocks in it until the clock has moved.
	Latency func()

	mu      sync.Mutex
	prs     map[string]*PR
	changes map[string]int
	next    int
	calls   []OriginCall
	gate    chan struct{}
	entered chan struct{}
	err     error
}

// NewOrigin returns an empty fake origin.
func NewOrigin() *Origin { return &Origin{prs: map[string]*PR{}} }

var _ summary.BatchFetcher = (*Origin)(nil)

// Set stores (or replaces) the PR of a node id.
func (o *Origin) Set(nodeID string, pr PR) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := pr
	o.prs[nodeID] = &p
}

// Update changes the PR of a node id in place, creating it first when unknown.
func (o *Origin) Update(nodeID string, fn func(*PR)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.changes == nil {
		o.changes = map[string]int{}
	}
	o.changes[nodeID]++
	fn(o.pr(nodeID))
}

// Changes is the number of Update calls the origin has seen for a node id: the
// changes the origin made to that PR after it was first described.
func (o *Origin) Changes(nodeID string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.changes[nodeID]
}

func (o *Origin) pr(nodeID string) *PR {
	p := o.prs[nodeID]
	if p == nil {
		o.next++
		p = &PR{
			Repo: "acme-org/widget", Number: o.next, State: "OPEN",
			HeadRefOid: "head-1", BaseRefOid: "base-1", HeadRefName: "feature",
		}
		o.prs[nodeID] = p
	}
	return p
}

// Ref returns the reference of a node id the origin knows.
func (o *Origin) Ref(nodeID string) entity.Ref {
	o.mu.Lock()
	defer o.mu.Unlock()
	p := o.pr(nodeID)
	ref, err := entity.NewRef(p.Repo, p.Number)
	if err != nil {
		panic(err)
	}
	return ref
}

// Summary builds the summary the origin answers for a node id.
func (o *Origin) Summary(nodeID string) summary.Summary {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.summaryLocked(nodeID)
}

func (o *Origin) summaryLocked(nodeID string) summary.Summary {
	p := o.pr(nodeID)
	if p.Gone {
		return summary.Summary{NodeID: nodeID, Gone: true}
	}
	return summary.Summary{
		NodeID: nodeID, Repo: p.Repo, Number: p.Number, Title: "Add the widget", URL: "https://example.invalid/pr",
		State: p.State, UpdatedAt: "2026-10-12T08:00:00Z", Mergeable: "MERGEABLE", Author: "phillipgreenii",
		HeadRefOid: p.HeadRefOid, HeadRefName: p.HeadRefName, BaseRefName: "main", BaseRefOid: p.BaseRefOid,
		ChecksState: "SUCCESS",
	}
}

// Fetch implements summary.BatchFetcher.
func (o *Origin) Fetch(_ context.Context, call ratefeed.Meta, ids []string) ([]summary.Summary, int64, error) {
	o.mu.Lock()
	o.calls = append(o.calls, OriginCall{IDs: slices.Clone(ids), Class: call.Class})
	gate, entered, gov, ferr, lat := o.gate, o.entered, o.Gov, o.err, o.Latency
	if entered != nil {
		o.entered = nil
		close(entered)
	}
	o.mu.Unlock()
	if gov != nil {
		tk, err := gov.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1})
		if err != nil {
			o.mu.Lock()
			o.calls = o.calls[:len(o.calls)-1] // refused: nothing was fetched
			o.mu.Unlock()
			return nil, 0, err
		}
		_ = tk.Settle(governor.Spend{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1, Outcome: "ok"})
	}
	if gate != nil {
		<-gate
	}
	if lat != nil {
		lat()
	}
	if ferr != nil {
		return nil, 0, ferr
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]summary.Summary, len(ids))
	for i, id := range ids {
		out[i] = o.summaryLocked(id)
	}
	return out, 1, nil
}

// Calls returns a copy of the calls seen so far.
func (o *Origin) Calls() []OriginCall {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.calls)
}

// CallCount is the number of calls seen so far.
func (o *Origin) CallCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.calls)
}

// Block makes every later call wait inside the call until the release function
// runs; entered is closed when the first blocked call has started.
func (o *Origin) Block() (release func(), entered <-chan struct{}) {
	gate, ch := make(chan struct{}), make(chan struct{})
	o.mu.Lock()
	o.gate, o.entered = gate, ch
	o.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }, ch
}

// FailWith makes every later call fail with err (nil lifts it).
func (o *Origin) FailWith(err error) {
	o.mu.Lock()
	o.err = err
	o.mu.Unlock()
}

// Resolver is a fake first-sighting resolver: it knows the references it was
// told about, records every call and puts the alias as the real one does.
type Resolver struct {
	Aliases *entity.Identity

	mu    sync.Mutex
	known map[string]string // Ref.Key() -> node id
	calls [][]entity.Ref
	err   error
}

// NewResolver returns a resolver over the alias table.
func NewResolver(aliases *entity.Identity) *Resolver {
	return &Resolver{Aliases: aliases, known: map[string]string{}}
}

// Know tells the resolver that the reference is the node id.
func (r *Resolver) Know(ref entity.Ref, nodeID string) {
	r.mu.Lock()
	r.known[ref.Key()] = nodeID
	r.mu.Unlock()
}

// FailWith makes every later call fail with err (nil lifts it).
func (r *Resolver) FailWith(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

// Calls returns a copy of the calls seen so far.
func (r *Resolver) Calls() [][]entity.Ref {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// Resolve implements groupsvc.RefResolver.
func (r *Resolver) Resolve(ctx context.Context, _ ratefeed.Meta, refs []entity.Ref) ([]entity.Resolution, int64, error) {
	r.mu.Lock()
	r.calls = append(r.calls, slices.Clone(refs))
	ferr := r.err
	r.mu.Unlock()
	if ferr != nil {
		return nil, 0, ferr
	}
	out := make([]entity.Resolution, len(refs))
	for i, ref := range refs {
		r.mu.Lock()
		node, ok := r.known[ref.Key()]
		r.mu.Unlock()
		out[i] = entity.Resolution{Requested: ref}
		if !ok {
			continue
		}
		if _, err := r.Aliases.Put(ctx, node, ref); err != nil {
			return nil, 0, fmt.Errorf("groupsvctest: %w", err)
		}
		out[i] = entity.Resolution{Requested: ref, NodeID: node, Ref: ref, Found: true}
	}
	return out, 1, nil
}
