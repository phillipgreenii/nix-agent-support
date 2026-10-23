package groupsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/groupkit"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The invented primary content of the Ensure tests is "<ref>|<stamp>|<stamp2>":
// a wire reference and the two opaque stamps of the entity's state.
const (
	stampA1 = "h1"
	stampA2 = "b1"
	stampB1 = "h2"
	stampB2 = "b2"
)

// stampDescriber reads that content.
type stampDescriber struct{}

func (stampDescriber) Describe(content []byte) (engine.Primary, error) {
	parts := strings.Split(string(content), "|")
	if len(parts) != 3 {
		return engine.Primary{}, errors.New("test: cannot decode")
	}
	return engine.Primary{
		Ref: parts[0], Stamp: parts[1], Stamp2: parts[2],
		Attrs: map[string]string{"label": "lbl-" + parts[0]},
	}, nil
}

// ensureParseID is the invented connector's reference rule for the Ensure tests:
// parseID, and a store id that is empty or holds a blank or a slash is malformed.
func ensureParseID(id string) (string, bool, error) {
	wire, isRef, err := parseID(id)
	if !isRef && (id == "" || strings.ContainsAny(id, " \t\r\n/")) {
		return "", false, errTestBadRef
	}
	return wire, isRef, err
}

// primaryExec is the scripted executor of the primary group: the connector's
// own, which the shared service only submits tasks for. It stores a primary row
// the way a real one does (generation plus one) and can block, fail, say the
// entity is gone, and run a hook after storing.
type primaryExec struct {
	r     *rig
	group string
	ttl   time.Duration

	mu      sync.Mutex
	classes []governor.Class
	started int
	gate    chan struct{}
	err     error
	gone    map[string]bool
	ref     string
	stamp   string
	stamp2  string
	after   func()
}

func newPrimaryExec(r *rig) *primaryExec {
	return &primaryExec{
		r: r, group: r.svc.cfg.Spec.PrimaryGroup, ttl: time.Minute, gone: map[string]bool{},
		ref: ref1, stamp: stampA1, stamp2: stampA2,
	}
}

func (p *primaryExec) MaxBatch() int                              { return 1 }
func (p *primaryExec) Spend(governor.Class, int) governor.Request { return governor.Request{} }

func (p *primaryExec) Execute(ctx context.Context, items []scheduler.Item) []scheduler.Result {
	res := make([]scheduler.Result, len(items))
	for i, it := range items {
		p.mu.Lock()
		p.classes = append(p.classes, it.Task.Class)
		p.started++
		gate, ferr, gone, after := p.gate, p.err, p.gone[it.Task.Entity], p.after
		content := fmt.Sprintf("%s|%s|%s", p.ref, p.stamp, p.stamp2)
		p.mu.Unlock()

		if gate != nil {
			<-gate
		}
		id, kind := it.Task.Entity, p.r.kind()
		switch {
		case ferr != nil:
			res[i].Err = ferr
		case gone:
			ent, _, err := p.r.st.GetEntity(ctx, kind, id)
			if err == nil {
				ent.TombstonedAt = p.r.clk.Now()
				err = p.r.st.PutEntity(ctx, ent)
			}
			res[i].Err = err
		default:
			row, _, err := p.r.st.GetGroup(ctx, kind, id, p.group)
			if err != nil {
				res[i].Err = err
				continue
			}
			now := p.r.clk.Now()
			row = store.GroupRow{
				Kind: kind, ID: id, Group: p.group, Content: []byte(content), Fingerprint: groupkit.Fingerprint([]byte(content)),
				FetchedAt: now, FreshUntil: now.Add(p.ttl), Generation: row.Generation + 1,
			}
			res[i].Err = p.r.st.PutGroup(ctx, row)
		}
		if after != nil {
			after()
		}
	}
	return res
}

func (p *primaryExec) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.classes)
}

func (p *primaryExec) callClasses() []governor.Class {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]governor.Class(nil), p.classes...)
}

func (p *primaryExec) block() (release func()) {
	gate := make(chan struct{})
	p.mu.Lock()
	p.gate = gate
	p.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (p *primaryExec) waitStarted(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		ok := p.started >= n
		p.mu.Unlock()
		if ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (p *primaryExec) failWith(err error) {
	p.mu.Lock()
	p.err = err
	p.mu.Unlock()
}

func (p *primaryExec) setGone(id string) {
	p.mu.Lock()
	p.gone[id] = true
	p.mu.Unlock()
}

func (p *primaryExec) setStamps(a, b string) {
	p.mu.Lock()
	p.stamp, p.stamp2 = a, b
	p.mu.Unlock()
}

func (p *primaryExec) setAfter(fn func()) {
	p.mu.Lock()
	p.after = fn
	p.mu.Unlock()
}

// fakeResolver is the first-sighting resolver: it knows a few references, and
// records each alias it learns the way a real one does.
type fakeResolver struct {
	mu      sync.Mutex
	known   map[string]string // lower-cased reference to id
	calls   [][]string
	aliases *fakeAliases
}

func (f *fakeResolver) Resolve(ctx context.Context, call CallMeta, refs []string) ([]Resolution, int64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), refs...))
	f.mu.Unlock()
	if call.Class != governor.ClassInteractive || call.Purpose != governor.PurposeInteractive {
		return nil, 0, fmt.Errorf("test: resolver admitted as %v/%v", call.Class, call.Purpose)
	}
	out := make([]Resolution, 0, len(refs))
	for _, ref := range refs {
		id, ok := f.known[strings.ToLower(ref)]
		out = append(out, Resolution{Requested: ref, NodeID: id, Ref: ref, Found: ok})
		if ok {
			if err := f.aliases.Put(ctx, id, ref); err != nil {
				return nil, 0, err
			}
		}
	}
	return out, int64(len(refs)), nil
}

func (f *fakeResolver) callList() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

// observerLog records Served and Revalidated calls.
type observerLog struct {
	mu     sync.Mutex
	served map[string][]time.Duration
}

func newObserverLog() *observerLog { return &observerLog{served: map[string][]time.Duration{}} }

func (o *observerLog) Served(group string, staleness time.Duration) {
	o.mu.Lock()
	o.served[group] = append(o.served[group], staleness)
	o.mu.Unlock()
}

func (o *observerLog) Revalidated(string, string) {}

func (o *observerLog) servedOf(group string) []time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]time.Duration(nil), o.served[group]...)
}

// ensureRig is an execRig whose scheduler also runs the connector's executor of
// the primary group, with four workers (a fetch of each group runs at once).
type ensureRig struct {
	*execRig
	prim     *primaryExec
	resolver *fakeResolver
	stopRun  context.CancelFunc
	runDone  chan struct{}
}

// newEnsureRig builds the rig for spec. The scheduler does not run until start.
func newEnsureRig(t *testing.T, spec registry.Spec) *ensureRig {
	t.Helper()
	x := &ensureRig{execRig: newExecRig(t, spec, nil, nil)}
	x.svc.cfg.Describer = stampDescriber{}
	x.svc.cfg.ParseID = ensureParseID
	x.sched = scheduler.New(scheduler.Options{Clock: x.clk, Workers: 4})
	x.svc.cfg.Scheduler = x.sched
	for _, g := range []string{groupSide, groupStamped, groupPassive} {
		x.sched.Register(g, &executor{s: x.svc, f: x.fetch[g]})
	}
	x.prim = newPrimaryExec(x.rig)
	x.sched.Register(spec.PrimaryGroup, x.prim)
	x.resolver = &fakeResolver{known: map[string]string{}, aliases: x.aliases}
	x.svc.cfg.Resolver = x.resolver
	t.Cleanup(x.stop)
	return x
}

// start runs the scheduler's loop.
func (x *ensureRig) start() {
	x.t.Helper()
	if x.runDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	x.stopRun, x.runDone = cancel, make(chan struct{})
	go func() {
		defer close(x.runDone)
		x.sched.Run(ctx)
	}()
}

// stop ends the loop (a test that steps the scheduler by hand calls it first).
func (x *ensureRig) stop() {
	if x.runDone == nil {
		return
	}
	x.stopRun()
	<-x.runDone
	x.runDone = nil
}

// observe installs an observer log.
func (x *ensureRig) observe() *observerLog {
	o := newObserverLog()
	x.svc.mu.Lock()
	x.svc.observer = o
	x.svc.mu.Unlock()
	return o
}

// dropFetcher removes the fetcher of a group, as if none had been registered.
func (x *ensureRig) dropFetcher(group string) {
	x.svc.mu.Lock()
	delete(x.svc.fetchers, group)
	x.svc.mu.Unlock()
}

// fetchTotal is how many Fetch calls the three fetchers have seen.
func (x *ensureRig) fetchTotal() int {
	n := 0
	for _, f := range x.fetch {
		n += f.callCount()
	}
	return n
}

// primary is the name of the primary group.
func (x *ensureRig) primary() string { return x.svc.cfg.Spec.PrimaryGroup }

// ensure runs Ensure with a generous deadline and fails the test on an error.
func (x *ensureRig) ensure(id string, want []string, opt EnsureOptions) EnsureResult {
	x.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := x.svc.Ensure(ctx, id, want, opt)
	if err != nil {
		x.t.Fatalf("Ensure(%s, %v) = %v", id, want, err)
	}
	return res
}

// seedPrim stores the entity's primary row (and the entity, accessed now): fresh
// inside its time window, or stale (past fresh_until, inside expiry).
func (x *ensureRig) seedPrim(id string, fresh bool) {
	x.t.Helper()
	x.seedEntity(id, x.clk.Now())
	now := x.clk.Now()
	row := store.GroupRow{
		ID: id, Group: x.primary(), Content: []byte(fmt.Sprintf("%s|%s|%s", ref1, stampA1, stampA2)),
		FetchedAt: now, FreshUntil: now.Add(time.Minute), Generation: 1,
	}
	if !fresh {
		row.FetchedAt, row.FreshUntil = now.Add(-10*time.Minute), now.Add(-time.Minute)
	}
	x.seedGroup(row)
}

// withStamps records the stamps a row was fetched for.
func withStamps(row store.GroupRow, a, b string) store.GroupRow {
	row.SetStamps(a, b)
	return row
}

// groupResult picks the result of one group.
func groupResult(t *testing.T, res EnsureResult, group string) GroupResult {
	t.Helper()
	for _, g := range res.Groups {
		if g.Group == group {
			return g
		}
	}
	t.Fatalf("no result for group %s in %+v", group, res.Groups)
	return GroupResult{}
}

// eventuallyTrue waits for cond.
func eventuallyTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// forEachKind runs the test body over both invented connectors.
func forEachKind(t *testing.T, body func(t *testing.T, spec registry.Spec)) {
	t.Helper()
	for _, spec := range []registry.Spec{specAlpha, specBeta} {
		t.Run(spec.EntityKind, func(t *testing.T) { body(t, spec) })
	}
}

// stampsOf is the stamps of the entity's stored primary content.
func stampsOf(x *ensureRig, id string) groupkit.Stamps {
	x.t.Helper()
	prim, _, ok, err := x.svc.loadPrimary(context.Background(), id)
	if err != nil || !ok {
		x.t.Fatalf("stored primary of %s: ok %v err %v", id, ok, err)
	}
	return groupkit.Stamps{Primary: prim.Stamp, Secondary: prim.Stamp2}
}
