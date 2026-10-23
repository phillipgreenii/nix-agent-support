// Package groupsvctest is a test rig for the shared group refresh service
// (pkg/connectord/groupsvc) over invented entity kinds and opaque stamps: it
// builds the service through groupsvc.New over a real store and scheduler on a
// manual clock, with a scripted describer, scripted fetchers, a scripted
// executor for the primary group (the connector's own in a real daemon), an
// in-memory alias table and a counting keep-alive stub.
//
// The rig names no real connector: the entity kind, the primary group and the
// other groups are the Names the test passes, so one test body runs against
// several invented connectors.
package groupsvctest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupsvc"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// T0 is the manual clock's start.
var T0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// Expiry is how long the rig's service serves a stored group stale.
const Expiry = time.Hour

// Names are the invented names of one connector. The test declares them: the
// rig itself holds no name, stamp or message of any connector.
type Names struct {
	// Kind is the entity kind and Primary the primary group.
	Kind, Primary string
	// Timed is a background group with a ten-minute TTL, Stamped a background
	// group with no time limit that is fresh only while the primary stamps match,
	// and Passive a group the background cycle never books.
	Timed, Stamped, Passive string
	// Stamp and Stamp2 are the opaque stamps the primary executor stores first.
	Stamp, Stamp2 string
	// NoSuch, Bad and Unknown are the sentinels wired into Config.NoSuchEntity,
	// Config.BadRef and Config.UnknownEntity; Bad is also what ParseID reports.
	NoSuch, Bad, Unknown error
}

// Spec is the registry.Spec of the names: the fields the service reads.
func (n Names) Spec() registry.Spec {
	return registry.Spec{
		EntityKind:   n.Kind,
		PrimaryGroup: n.Primary,
		Groups:       []string{n.Primary, n.Timed, n.Stamped, n.Passive},
		FoldRef:      strings.ToLower,
	}
}

// Rules is the freshness table of the names.
func (n Names) Rules() groupkit.Table {
	return groupkit.Table{Groups: []groupkit.GroupSpec{
		{Name: n.Timed, TTL: 10 * time.Minute, Background: true},
		{Name: n.Stamped, Stamped: true, Background: true},
		{Name: n.Passive, TTL: 5 * time.Minute},
	}}
}

// Groups are the non-primary groups.
func (n Names) Groups() []string { return []string{n.Timed, n.Stamped, n.Passive} }

// Content is the invented primary content "<ref>|<stamp>|<stamp2>".
func Content(ref, stamp, stamp2 string) string { return ref + "|" + stamp + "|" + stamp2 }

type describer struct{}

func (describer) Describe(content []byte) (engine.Primary, error) {
	parts := strings.Split(string(content), "|")
	if len(parts) != 3 {
		return engine.Primary{}, errors.New("test: cannot decode")
	}
	return engine.Primary{Ref: parts[0], Stamp: parts[1], Stamp2: parts[2]}, nil
}

// ParseID is the invented connectors' reference rule: an id with a "~" is a wire
// reference ("Zone-A/Item-B~7", malformed when the part after the "~" is not a
// number); anything else is a store id, malformed when empty or holding a blank
// or a slash.
func (n Names) ParseID(id string) (string, bool, error) {
	_, num, found := strings.Cut(id, "~")
	if found {
		if _, err := strconv.Atoi(num); err != nil {
			return id, true, n.Bad
		}
		return id, true, nil
	}
	if id == "" || strings.ContainsAny(id, " \t\r\n/") {
		return id, false, n.Bad
	}
	return id, false, nil
}

// Aliases is an in-memory query.Aliases keyed by the case-folded reference.
type Aliases struct {
	mu    sync.Mutex
	byRef map[string]query.Alias
	byID  map[string]string
}

var _ query.Aliases = (*Aliases)(nil)

// NewAliases returns an empty alias table.
func NewAliases() *Aliases {
	return &Aliases{byRef: map[string]query.Alias{}, byID: map[string]string{}}
}

// Put records that id is ref.
func (a *Aliases) Put(_ context.Context, id, ref string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byRef[strings.ToLower(ref)] = query.Alias{ID: id, CanonicalRef: ref}
	a.byID[id] = ref
	return nil
}

// Resolve answers the alias of each reference that has one.
func (a *Aliases) Resolve(_ context.Context, refs []string) (map[string]query.Alias, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]query.Alias{}
	for _, r := range refs {
		if al, ok := a.byRef[strings.ToLower(r)]; ok {
			out[r] = al
		}
	}
	return out, nil
}

// Ref answers the reference recorded for id.
func (a *Aliases) Ref(_ context.Context, id string) (string, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref, ok := a.byID[id]
	return ref, ok, nil
}

// KeepStub is the connector's KeepAlive: it retains the ids it was told to and
// counts every call.
type KeepStub struct {
	mu       sync.Mutex
	retain   map[string]bool
	retained []string
	accessed []string
}

var _ groupsvc.KeepAlive = (*KeepStub)(nil)

// Keep makes Retain answer true for the ids.
func (k *KeepStub) Keep(ids ...string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, id := range ids {
		k.retain[id] = true
	}
}

// Retain answers the fixed answer of id and records the call.
func (k *KeepStub) Retain(_ context.Context, id string) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.retained = append(k.retained, id)
	return k.retain[id], nil
}

// Access records the call.
func (k *KeepStub) Access(_ context.Context, id string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.accessed = append(k.accessed, id)
	return nil
}

// RetainCalls are the ids Retain was asked about, in order.
func (k *KeepStub) RetainCalls() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.retained...)
}

// AccessCalls are the ids Access was called with, in order.
func (k *KeepStub) AccessCalls() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.accessed...)
}

// ResetCalls forgets the recorded calls.
func (k *KeepStub) ResetCalls() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.retained, k.accessed = nil, nil
}

type errorPolicy struct{}

func (errorPolicy) Classify(_ *governor.Governor, _ time.Duration, err error) error { return err }
func (errorPolicy) IsRateLimit(error) bool                                          { return false }
func (errorPolicy) IsUnauthenticated(error) bool                                    { return false }

// Resolver is the first-sighting resolver: it knows the references it was told
// about and records the alias of each, as a real one does.
type Resolver struct {
	mu      sync.Mutex
	known   map[string]string // lower-cased reference to id
	calls   int
	aliases *Aliases
}

var _ groupsvc.RefResolver = (*Resolver)(nil)

// Know makes the resolver find ref as id.
func (r *Resolver) Know(ref, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.known[strings.ToLower(ref)] = id
}

// Calls is how many times Resolve ran.
func (r *Resolver) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Resolve resolves the references it knows.
func (r *Resolver) Resolve(ctx context.Context, _ groupsvc.CallMeta, refs []string) ([]groupsvc.Resolution, int64, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	out := make([]groupsvc.Resolution, 0, len(refs))
	for _, ref := range refs {
		r.mu.Lock()
		id, ok := r.known[strings.ToLower(ref)]
		r.mu.Unlock()
		out = append(out, groupsvc.Resolution{Requested: ref, NodeID: id, Ref: ref, Found: ok})
		if ok {
			if err := r.aliases.Put(ctx, id, ref); err != nil {
				return nil, 0, err
			}
		}
	}
	return out, int64(len(refs)), nil
}

// Fetcher is a counting, scriptable groupsvc.Fetcher. By default it answers
// every target with a small document that changes on every call and carries the
// target's own stamps.
type Fetcher struct {
	name string

	mu      sync.Mutex
	targets [][]groupsvc.Target
	seq     int
	err     error
	gone    map[string]bool
	gate    chan struct{}
}

var _ groupsvc.Fetcher = (*Fetcher)(nil)

// NewFetcher returns the scripted fetcher of group.
func NewFetcher(group string) *Fetcher { return &Fetcher{name: group, gone: map[string]bool{}} }

// Group is the group name.
func (f *Fetcher) Group() string { return f.name }

// MaxBatch is ten.
func (f *Fetcher) MaxBatch() int { return 10 }

// Spend is one unit of the class.
func (f *Fetcher) Spend(class governor.Class, _ int) governor.Request {
	return governor.Request{Class: class, Cost: 1}
}

// FailWith makes every later Fetch fail every target with err.
func (f *Fetcher) FailWith(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// SetGone makes the fetch of id say the entity is gone.
func (f *Fetcher) SetGone(id string) {
	f.mu.Lock()
	f.gone[id] = true
	f.mu.Unlock()
}

// Block makes every later Fetch wait inside the call until the returned release
// function runs.
func (f *Fetcher) Block() (release func()) {
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// Fetch answers the targets.
func (f *Fetcher) Fetch(_ context.Context, _ groupsvc.CallMeta, targets []groupsvc.Target) []groupsvc.Outcome {
	f.mu.Lock()
	f.targets = append(f.targets, append([]groupsvc.Target(nil), targets...))
	gate, ferr := f.gate, f.err
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	out := make([]groupsvc.Outcome, len(targets))
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range targets {
		switch {
		case ferr != nil:
			out[i] = groupsvc.Outcome{Err: ferr}
		case f.gone[t.ID]:
			out[i] = groupsvc.Outcome{Err: groupsvc.ErrGone}
		default:
			f.seq++
			out[i] = groupsvc.Outcome{
				Content: []byte(fmt.Sprintf(`{"group":%q,"id":%q,"stamp":%q,"fetch":%d}`, f.name, t.ID, t.Stamp, f.seq)),
				Stamp:   t.Stamp, Stamp2: t.Stamp2, Cost: 1,
			}
		}
	}
	return out
}

// Calls is how many Fetch calls ran.
func (f *Fetcher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.targets)
}

// Targets are the targets of every Fetch call, in order.
func (f *Fetcher) Targets() [][]groupsvc.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]groupsvc.Target(nil), f.targets...)
}

// primaryExec is the connector's own executor of the primary group: it stores a
// primary row the way a real one does (generation plus one).
type primaryExec struct {
	r *Rig

	mu     sync.Mutex
	calls  int
	stamp  string
	stamp2 string
}

func (p *primaryExec) MaxBatch() int                              { return 1 }
func (p *primaryExec) Spend(governor.Class, int) governor.Request { return governor.Request{} }

func (p *primaryExec) Execute(ctx context.Context, items []scheduler.Item) []scheduler.Result {
	res := make([]scheduler.Result, len(items))
	for i, it := range items {
		p.mu.Lock()
		p.calls++
		stamp, stamp2 := p.stamp, p.stamp2
		p.mu.Unlock()
		id, kind, group := it.Task.Entity, p.r.Names.Kind, p.r.Names.Primary
		ref, ok, err := p.r.Aliases.Ref(ctx, id)
		if err != nil {
			res[i].Err = err
			continue
		}
		if !ok {
			ref = "Zone-A/" + id + "~1"
		}
		row, _, err := p.r.Store.GetGroup(ctx, kind, id, group)
		if err != nil {
			res[i].Err = err
			continue
		}
		content := Content(ref, stamp, stamp2)
		now := p.r.Clock.Now()
		res[i].Err = p.r.Store.PutGroup(ctx, store.GroupRow{
			Kind: kind, ID: id, Group: group, Content: []byte(content), Fingerprint: groupkit.Fingerprint([]byte(content)),
			FetchedAt: now, FreshUntil: now.Add(time.Minute), Generation: row.Generation + 1,
		})
	}
	return res
}

// Rig is the shared group refresh service over a real store and scheduler on a
// manual clock.
type Rig struct {
	T        *testing.T
	Names    Names
	Store    *store.Store
	Clock    *governor.ManualClock
	Sched    *scheduler.Scheduler
	Svc      *groupsvc.Service
	Aliases  *Aliases
	Keep     *KeepStub
	Resolver *Resolver
	Fetch    map[string]*Fetcher

	prim    *primaryExec
	runOnce sync.Once
}

// New builds the rig over st (the test opens it and closes it) through
// groupsvc.New and registers a scripted fetcher for each non-primary group and
// the scripted executor of the primary group. The scheduler does not run until
// Start.
func New(t *testing.T, n Names, st *store.Store) *Rig {
	t.Helper()
	clk := governor.NewManualClock(T0)
	aliases := NewAliases()
	r := &Rig{
		T: t, Names: n, Store: st, Clock: clk, Aliases: aliases, Fetch: map[string]*Fetcher{},
		Keep:     &KeepStub{retain: map[string]bool{}},
		Resolver: &Resolver{known: map[string]string{}, aliases: aliases},
	}
	r.Sched = scheduler.New(scheduler.Options{Clock: clk, Workers: 4, PollInterval: 5 * time.Millisecond})
	var err error
	r.Svc, err = groupsvc.New(groupsvc.Config{
		Spec: n.Spec(), Describer: describer{}, Store: st, Scheduler: r.Sched, Rules: n.Rules(),
		Aliases: aliases, Resolver: r.Resolver, KeepAlive: r.Keep, Errors: errorPolicy{},
		Clock: clk, Expiry: Expiry, NoSuchEntity: n.NoSuch, BadRef: n.Bad, UnknownEntity: n.Unknown, ParseID: n.ParseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.prim = &primaryExec{r: r, stamp: n.Stamp, stamp2: n.Stamp2}
	r.Sched.Register(n.Primary, r.prim)
	for _, g := range n.Groups() {
		r.Fetch[g] = NewFetcher(g)
		r.Svc.Register(r.Fetch[g])
	}
	return r
}

// Start runs the scheduler's loop until the test ends.
func (r *Rig) Start() {
	r.runOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Sched.Run(ctx)
		}()
		r.T.Cleanup(func() {
			cancel()
			<-done
		})
	})
}

// PrimaryCalls is how many primary tasks the scripted primary executor ran.
func (r *Rig) PrimaryCalls() int {
	r.prim.mu.Lock()
	defer r.prim.mu.Unlock()
	return r.prim.calls
}

// SetStamps sets the stamps the primary executor stores from now on.
func (r *Rig) SetStamps(stamp, stamp2 string) {
	r.prim.mu.Lock()
	r.prim.stamp, r.prim.stamp2 = stamp, stamp2
	r.prim.mu.Unlock()
}

// SeedPrimary stores the primary row of id (and the entity, accessed now), fresh
// inside its time window or stale (past fresh_until, inside expiry).
func (r *Rig) SeedPrimary(id string, fresh bool) {
	r.T.Helper()
	ctx := context.Background()
	if err := r.Store.TouchEntity(ctx, r.Names.Kind, id, r.Clock.Now()); err != nil {
		r.T.Fatal(err)
	}
	now := r.Clock.Now()
	content := Content("Zone-A/"+id+"~1", r.Names.Stamp, r.Names.Stamp2)
	row := store.GroupRow{
		Kind: r.Names.Kind, ID: id, Group: r.Names.Primary, Content: []byte(content),
		FetchedAt: now, FreshUntil: now.Add(time.Minute), Generation: 1,
	}
	if !fresh {
		row.FetchedAt, row.FreshUntil = now.Add(-10*time.Minute), now.Add(-time.Minute)
	}
	if err := r.Store.PutGroup(ctx, row); err != nil {
		r.T.Fatal(err)
	}
}

// SeedGroup stores a group row of id as a fetch at the manual clock's now would
// leave it (fresh) or an hour ago (stale: past fresh_until, inside expiry).
func (r *Rig) SeedGroup(id, group, content string, fresh bool) {
	r.T.Helper()
	now := r.Clock.Now()
	fetched := now
	if !fresh {
		fetched = now.Add(-30 * time.Minute)
	}
	freshUntil, maxAge := r.Names.Rules().Schedule(group, false, fetched)
	row := store.GroupRow{
		Kind: r.Names.Kind, ID: id, Group: group, Content: []byte(content), Fingerprint: groupkit.Fingerprint([]byte(content)),
		FetchedAt: fetched, FreshUntil: freshUntil, MaxAgeAt: maxAge, Generation: 1,
	}
	row.SetStamps(r.Names.Stamp, r.Names.Stamp2)
	if err := r.Store.PutGroup(context.Background(), row); err != nil {
		r.T.Fatal(err)
	}
}

// Group reads a stored group row.
func (r *Rig) Group(id, group string) (store.GroupRow, bool) {
	r.T.Helper()
	row, ok, err := r.Store.GetGroup(context.Background(), r.Names.Kind, id, group)
	if err != nil {
		r.T.Fatal(err)
	}
	return row, ok
}

// Tombstone marks the entity gone.
func (r *Rig) Tombstone(id string) {
	r.T.Helper()
	ctx := context.Background()
	ent, _, err := r.Store.GetEntity(ctx, r.Names.Kind, id)
	if err != nil {
		r.T.Fatal(err)
	}
	ent.TombstonedAt = r.Clock.Now()
	if err := r.Store.PutEntity(ctx, ent); err != nil {
		r.T.Fatal(err)
	}
}

// PutQuery stores a configured query holding the ids.
func (r *Rig) PutQuery(key string, ids ...string) {
	r.T.Helper()
	if err := r.Store.PutQuery(context.Background(), store.QueryRow{Key: key, Configured: true, IDs: ids, Complete: true}); err != nil {
		r.T.Fatal(err)
	}
}

// Ensure runs Ensure with a generous deadline and fails the test on an error.
func (r *Rig) Ensure(id string, want []string, opt groupsvc.EnsureOptions) groupsvc.EnsureResult {
	r.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := r.Svc.Ensure(ctx, id, want, opt)
	if err != nil {
		r.T.Fatalf("Ensure(%s, %v) = %v", id, want, err)
	}
	return res
}

// Result picks the result of one group.
func Result(t *testing.T, res groupsvc.EnsureResult, group string) groupsvc.GroupResult {
	t.Helper()
	for _, g := range res.Groups {
		if g.Group == group {
			return g
		}
	}
	t.Fatalf("no result for group %s in %+v", group, res.Groups)
	return groupsvc.GroupResult{}
}

// Eventually waits for cond.
func Eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
