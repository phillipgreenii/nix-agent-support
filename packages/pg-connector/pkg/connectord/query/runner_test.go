package query

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/registry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

// The invented registration every test of this package runs on: one entity kind
// and change kinds no real connector uses.
const (
	kindWidget     = "widget"
	changeAdded    = "added"
	changeGone     = "gone"
	changeJoined   = "joined"
	changeLeft     = "left"
	statePrefix    = "state_of:"
	bucketInvent   = governor.BucketRESTCore
	ttlMine        = 60 * time.Second
	ttlTeam        = 120 * time.Second
	ttlAdhoc       = 120 * time.Second
	queryMine      = "mine"
	queryTeam      = "team"
	exprMineOpen   = "mine-open"
	exprTeamOpen   = "team-open"
	exprTeamSecond = "team-second"
)

var t0 = time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)

// testSpec is the invented registration.
func testSpec() registry.Spec {
	return registry.Spec{
		EntityKind:   kindWidget,
		PrimaryGroup: store.SummaryGroupName,
		Groups:       []string{store.SummaryGroupName},
		Changes:      []string{changeAdded, changeGone, changeJoined, changeLeft},
		Roles:        registry.Roles{Added: changeAdded, Removed: changeGone, EnteredQuery: changeJoined, LeftQuery: changeLeft},
		Views:        []registry.View{{Name: kindWidget}},
		FoldRef:      foldCase,
		Emission:     registry.EmitAll,
		Delivery:     registry.DeliverByMembership,
		Membership:   registry.MembershipPolicy{StoreRows: true},
	}
}

// foldCase compares the part before the number sign case-insensitively.
func foldCase(ref string) string {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return strings.ToLower(ref[:i]) + ref[i:]
	}
	return strings.ToLower(ref)
}

// nopClassifier is the engine's classifier for a test that only records
// membership rows.
type nopClassifier struct{}

func (nopClassifier) Describe([]byte) (engine.Primary, error) { return engine.Primary{}, nil }
func (nopClassifier) Primary(_, _ []byte, _ int) (engine.Result, []string, error) {
	return engine.Result{}, nil, nil
}

func (nopClassifier) Group(string, []byte, []byte, int) (engine.Result, error) {
	return engine.Result{}, nil
}

func (nopClassifier) Counts(engine.Awaiting, store.GroupRow, store.PendingChange, time.Time) bool {
	return false
}

func newEngine(t testing.TB, clk governor.Clock) *engine.Engine {
	t.Helper()
	e, err := engine.New(engine.Config{
		Spec: testSpec(), Classifier: nopClassifier{}, Clock: clk,
		SettleTimeout: time.Minute, BodyCap: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// mem is one listed member of a fake listing.
type mem struct{ id, ref string }

var (
	memA = mem{"w-a", "Example-Org/Thing#3"}
	memB = mem{"w-b", "example-org/other#7"}
	memC = mem{"w-c", "Example-Org/Thing#1"}
	memD = mem{"w-d", "Example-Org/Thing#9"}
)

func listingOf(complete bool, ms ...mem) Listing {
	l := Listing{Complete: complete}
	for _, m := range ms {
		l.Members = append(l.Members, Member{ID: m.id, Ref: m.ref})
	}
	return l
}

func refs(ms ...mem) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ref
	}
	return out
}

// listCall is one List call the fake saw.
type listCall struct {
	key   Key
	exprs []string
	meta  CallMeta
}

// fakeLister answers List from a swappable function and records every call.
// answer is read by the scheduler goroutine inside List, so a test that swaps it
// while the scheduler loop is running MUST go through setAnswer, which takes mu.
type fakeLister struct {
	mu     sync.Mutex
	calls  []listCall
	answer func(c listCall) (Listing, error)
}

func (l *fakeLister) List(_ context.Context, key Key, exprs []string, meta CallMeta) (Listing, error) {
	c := listCall{key: key, exprs: slices.Clone(exprs), meta: meta}
	l.mu.Lock()
	l.calls = append(l.calls, c)
	answer := l.answer
	l.mu.Unlock()
	return answer(c)
}

func (l *fakeLister) setAnswer(answer func(c listCall) (Listing, error)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.answer = answer
}

func (l *fakeLister) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

func (l *fakeLister) callList() []listCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.calls)
}

// fakeAliases is an in-memory alias table: Put moves an id's reference in place
// (a rename) and Resolve finds the id whose reference equals the one given under
// the spec's fold.
type fakeAliases struct {
	mu       sync.Mutex
	byID     map[string]string
	resolves int
	putErr   error
}

func newAliases() *fakeAliases { return &fakeAliases{byID: map[string]string{}} }

func (a *fakeAliases) Put(_ context.Context, id, ref string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.putErr != nil {
		return a.putErr
	}
	a.byID[id] = ref
	return nil
}

func (a *fakeAliases) set(m mem) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byID[m.id] = m.ref
}

func (a *fakeAliases) resolveCalls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resolves
}

func (a *fakeAliases) Ref(_ context.Context, id string) (string, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ref, ok := a.byID[id]
	return ref, ok, nil
}

func (a *fakeAliases) Resolve(_ context.Context, refs []string) (map[string]Alias, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolves++
	out := map[string]Alias{}
	for _, ref := range refs {
		for id, stored := range a.byID {
			if foldCase(stored) == foldCase(ref) {
				out[ref] = Alias{ID: id, CanonicalRef: stored}
			}
		}
	}
	return out, nil
}

// fakeSettings is the connector's own configuration.
type fakeSettings struct {
	queries map[string][]string
	ttl     map[string]time.Duration
}

func (s fakeSettings) Expr(name string) ([]string, bool) {
	e, ok := s.queries[name]
	return e, ok
}
func (fakeSettings) Watched(string) bool { return false }
func (s fakeSettings) Names() []string {
	names := make([]string, 0, len(s.queries))
	for n := range s.queries {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func (s fakeSettings) TTL(key Key) time.Duration {
	if key.Adhoc() {
		return ttlAdhoc
	}
	return s.ttl[key.Name]
}

// fakeTracker records what the executor tracked.
type fakeTracker struct {
	mu      sync.Mutex
	tracked [][]string
	err     error
}

func (f *fakeTracker) Track(_ context.Context, refs []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tracked = append(f.tracked, slices.Clone(refs))
	return nil, f.err
}

func (f *fakeTracker) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tracked)
}

// fakeAuth is the typed unauthenticated state.
type fakeAuth struct {
	mu  sync.Mutex
	bad error
}

func (a *fakeAuth) Unauthenticated() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bad != nil, a.bad
}

func (a *fakeAuth) set(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bad = err
}

type rig struct {
	st      *store.Store
	clk     *governor.ManualClock
	gov     *governor.Governor
	sched   *scheduler.Scheduler
	lister  *fakeLister
	aliases *fakeAliases
	tracker *fakeTracker
	auth    *fakeAuth
	runner  *Runner
}

func newRig(t *testing.T) *rig { return newRigWith(t) }

// newRigWith is newRig with each opt applied to the runner's Config before the
// runner is built.
func newRigWith(t *testing.T, opts ...func(*Config)) *rig {
	t.Helper()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)
	gov, err := governor.New(governor.Config{
		GraphQL:         governor.Limits{Cap: 1500, Reserve: 100},
		RESTCore:        governor.Limits{Cap: 3000},
		RESTSearch:      governor.Limits{Cap: 20},
		BackgroundShare: 0.9,
	}, governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	lister := &fakeLister{answer: func(listCall) (Listing, error) { return listingOf(true, memA, memB, memC), nil }}
	sched := scheduler.New(scheduler.Options{Clock: clk, Gate: gov, Workers: 2, PollInterval: 5 * time.Millisecond, RetryDelay: time.Minute})
	al := newAliases()
	tr := &fakeTracker{}
	auth := &fakeAuth{}
	rc := Config{
		Spec:             testSpec(),
		StateFieldPrefix: statePrefix,
		UnauthError:      func(cause error) error { return fmt.Errorf("%w: %v", errUnauthenticated, cause) },
		Bucket:           bucketInvent,
		Settings: fakeSettings{
			queries: map[string][]string{
				queryMine: {exprMineOpen},
				queryTeam: {exprTeamOpen, exprTeamSecond},
			},
			ttl: map[string]time.Duration{queryMine: ttlMine, queryTeam: ttlTeam},
		},
		Store: st, Aliases: al, Tracker: tr, Lister: lister, Scheduler: sched, Auth: auth, Clock: clk,
	}
	for _, o := range opts {
		o(&rc)
	}
	runner, err := New(rc)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{st: st, clk: clk, gov: gov, sched: sched, lister: lister, aliases: al, tracker: tr, auth: auth, runner: runner}
}

var errUnauthenticated = errors.New("not signed in")

// run starts the scheduler loop for the test.
func (r *rig) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.sched.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (r *rig) row(t *testing.T, key string) store.QueryRow {
	t.Helper()
	row, ok, err := r.st.GetQuery(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("query row %q: ok=%v err=%v", key, ok, err)
	}
	return row
}

func TestKey_IsCanonical(t *testing.T) {
	if got := (Key{Name: "mine"}).String(); got != "mine" {
		t.Errorf("configured key = %q, want the bare name", got)
	}
	a := Key{Name: "mine", Args: map[string]string{"label": "bug", "base": "main"}}
	b := Key{Name: "mine", Args: map[string]string{"base": "main", "label": "bug"}}
	if a.String() != b.String() || a.String() != "mine?base=main&label=bug" {
		t.Errorf("keys %q and %q differ or are not canonical", a, b)
	}
	if !a.Adhoc() || (Key{Name: "mine"}).Adhoc() || (Key{Name: "mine", Args: map[string]string{}}).Adhoc() {
		t.Error("only a key with arguments is ad-hoc")
	}
	if a.String() == (Key{Name: "mine", Args: map[string]string{"label": "bug"}}).String() {
		t.Error("a differing argument must differ in the key")
	}
}

func TestFetch_RunsTheListingAndStoresTheOrderedIDs(t *testing.T) {
	r := newRig(t)
	r.run(t)
	row, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	want := refs(memA, memB, memC)
	if !slices.Equal(row.IDs, want) || !row.Complete || !row.Configured || row.Key != "mine" {
		t.Errorf("row = %+v", row)
	}
	if !row.FetchedAt.Equal(t0) || !row.LastSuccessAt.Equal(t0) || !row.FreshUntil.Equal(t0.Add(60*time.Second)) || row.LastError != "" {
		t.Errorf("times = fetched %v success %v fresh %v err %q", row.FetchedAt, row.LastSuccessAt, row.FreshUntil, row.LastError)
	}
	if stored := r.row(t, "mine"); !slices.Equal(stored.IDs, want) {
		t.Errorf("stored ids = %v", stored.IDs)
	}
	calls := r.lister.callList()
	if len(calls) != 1 || !slices.Equal(calls[0].exprs, []string{exprMineOpen}) || calls[0].key.String() != "mine" {
		t.Errorf("list calls = %+v", calls)
	}
	// An interactive fetch is admitted as the interactive class and purpose.
	if m := calls[0].meta; m.Class != governor.ClassInteractive || m.Purpose != governor.PurposeInteractive {
		t.Errorf("call meta = %+v, want the interactive class and purpose", m)
	}
	// The aliases make the members addressable by id.
	if got := r.aliases.byID["w-b"]; got != memB.ref {
		t.Errorf("alias of w-b = %q, want %q", got, memB.ref)
	}
}

func TestSchedule_RerunsAConfiguredQueryAtItsOwnCadenceAsClass3(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	// mine (60 s) and team (120 s) each ran once, as class 3 tasks.
	if r.lister.count() != 2 {
		t.Fatalf("first drain made %d list calls, want 2", r.lister.count())
	}
	for _, c := range r.lister.callList() {
		if c.meta.Class != governor.ClassQuery || c.meta.Purpose != governor.PurposeQuery {
			t.Errorf("list call %+v, want the background query class and purpose", c.meta)
		}
	}
	for _, b := range r.sched.State().Batches {
		if b.Kind != Kind {
			continue
		}
		for _, it := range b.Items {
			if it.Class != governor.ClassQuery {
				t.Errorf("query task %s ran as class %d, want 3", it.Entity, it.Class)
			}
		}
	}

	r.clk.Advance(30 * time.Second)
	r.sched.Drain(ctx)
	if r.lister.count() != 2 {
		t.Errorf("inside the ttl nothing re-runs, but list was called %d times", r.lister.count())
	}

	r.clk.Advance(31 * time.Second) // 61 s: mine is due, team (120 s) is not
	r.sched.Drain(ctx)
	if r.lister.count() != 3 {
		t.Errorf("after mine's ttl list calls = %d, want 3 (mine only)", r.lister.count())
	}
	if got := r.row(t, "mine").FetchedAt; !got.Equal(t0.Add(61 * time.Second)) {
		t.Errorf("mine fetched_at = %v", got)
	}
	if got := r.row(t, "team").FetchedAt; !got.Equal(t0) {
		t.Errorf("team fetched_at = %v, want unchanged", got)
	}
}

func TestSchedule_DueTimeSurvivesARestart(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.st.PutQuery(ctx, store.QueryRow{
		Key: "mine", Configured: true, IDs: refs(memA), Complete: true,
		FetchedAt: t0, FreshUntil: t0.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	// mine is stored fresh, so only team ran.
	if got := r.lister.callList(); len(got) != 1 || got[0].key.Name != "team" {
		t.Errorf("calls = %+v, want only team", got)
	}
}

func TestSchedule_DemotesAStoredQueryTheConfigNoLongerDefines(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.st.PutQuery(ctx, store.QueryRow{Key: "retired", Configured: true, IDs: refs(memA), Complete: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	if r.row(t, "retired").Configured {
		t.Error("a query the config no longer defines must stop being configured (its members stop being keep-alive)")
	}
}

func TestExecutor_TracksTheMembersOfAConfiguredRunOnly(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	// Both configured queries listed the same three members, so the tracker saw
	// them once per run, in the stored order.
	calls := r.tracker.calls()
	if len(calls) != 2 {
		t.Fatalf("tracker calls = %v, want one per configured run", calls)
	}
	for _, c := range calls {
		if !slices.Equal(c, refs(memA, memB, memC)) {
			t.Errorf("tracked %v, want the ordered references", c)
		}
	}

	// An ad-hoc run tracks nothing.
	r.run(t)
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine", Args: map[string]string{"label": "bug"}}); err != nil {
		t.Fatal(err)
	}
	if got := len(r.tracker.calls()); got != 2 {
		t.Errorf("tracker calls = %d after an ad-hoc run, want still 2", got)
	}
}

func TestFetch_AnAdhocKeyIsCachedUnderAdhocTTLAndNeverRerun(t *testing.T) {
	r := newRig(t)
	r.run(t)
	ctx := context.Background()
	key := Key{Name: "mine", Args: map[string]string{"label": "bug"}}
	row, err := r.runner.Fetch(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if row.Key != "mine?label=bug" || row.Configured || !row.FreshUntil.Equal(t0.Add(ttlAdhoc)) {
		t.Errorf("row = %+v, want an unconfigured row fresh for the ad-hoc ttl", row)
	}
	if got := r.lister.callList(); len(got) != 1 || got[0].key.String() != key.String() || !slices.Equal(got[0].exprs, []string{exprMineOpen}) {
		t.Errorf("calls = %+v, want the key handed to the lister with the configured expressions", got)
	}
	// It is not a keep-alive member...
	if rows, _ := r.st.ListQueries(ctx, true); len(rows) != 0 {
		t.Errorf("configured rows = %+v, want none (ad-hoc ids join keep-alive through access only)", rows)
	}
	// ...and the scheduler never re-runs it, however long it waits.
	r.clk.Advance(time.Hour)
	time.Sleep(100 * time.Millisecond) // let the (real-time) loop poll
	if r.lister.count() != 1 {
		t.Errorf("an ad-hoc key was re-run: %d list calls", r.lister.count())
	}
}

func TestFetch_ADifferingArgumentIsADifferentKey(t *testing.T) {
	r := newRig(t)
	r.run(t)
	ctx := context.Background()
	for _, label := range []string{"bug", "chore"} {
		if _, err := r.runner.Fetch(ctx, Key{Name: "mine", Args: map[string]string{"label": label}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := r.st.ListQueries(ctx, false)
	if len(rows) != 2 || r.lister.count() != 2 {
		t.Errorf("rows = %d, list calls = %d, want 2 and 2", len(rows), r.lister.count())
	}
}

func TestFetch_AnUnknownNameIsRefused(t *testing.T) {
	r := newRig(t)
	if _, err := r.runner.Fetch(context.Background(), Key{Name: "nope"}); err == nil || r.lister.count() != 0 {
		t.Errorf("err = %v after %d calls, want an error and no call", err, r.lister.count())
	}
}

func TestQueueRun_AClass2RerunRunsTheQueryEvenInsideItsTTLAndAClass3OneDoesNot(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.runner.Schedule(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	if r.lister.count() != 2 {
		t.Fatalf("first drain made %d list calls, want 2", r.lister.count())
	}

	r.clk.Advance(10 * time.Second) // inside the ttl
	if err := r.runner.QueueRun(Key{Name: "mine"}, governor.ClassQuery); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	if r.lister.count() != 2 {
		t.Fatalf("a class 3 task re-ran a fresh query: %d list calls, want 2", r.lister.count())
	}

	if err := r.runner.QueueRun(Key{Name: "mine"}, governor.ClassInvalidated); err != nil {
		t.Fatal(err)
	}
	if c, queued := r.sched.InspectQueued(entityOf(Key{Name: "mine"}), Group); !queued || c != governor.ClassInvalidated {
		t.Fatalf("queued = %v at class %d, want a class 2 task", queued, c)
	}
	r.sched.Drain(ctx)
	if r.lister.count() != 3 {
		t.Errorf("a class 2 re-run made %d list calls in total, want 3 (mine only)", r.lister.count())
	}
	if got := r.row(t, "mine").FetchedAt; !got.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("mine fetched_at = %v, want the re-run's time", got)
	}
	// The run booked the next cycle at class 3 from the new result.
	if c, queued := r.sched.InspectQueued(entityOf(Key{Name: "mine"}), Group); !queued || c != governor.ClassQuery {
		t.Errorf("after the run the next cycle is queued = %v at class %d, want class 3", queued, c)
	}
}

func TestQueueRun_RefusesAnUnknownNameAnAdhocKeyAndAnInvalidClass(t *testing.T) {
	r := newRig(t)
	for _, tc := range []struct {
		key   Key
		class governor.Class
	}{
		{Key{Name: "nope"}, governor.ClassInvalidated},
		{Key{Name: "mine", Args: map[string]string{"author": "someone"}}, governor.ClassInvalidated},
		{Key{Name: "mine"}, governor.Class(9)},
	} {
		if err := r.runner.QueueRun(tc.key, tc.class); err == nil {
			t.Errorf("QueueRun(%v, %d) = nil, want an error", tc.key, tc.class)
		}
	}
	if r.sched.State().Classes[1].Depth != 0 || r.lister.count() != 0 {
		t.Error("a refused QueueRun queued or ran something")
	}
}

func TestFetch_ConcurrentCallersShareOneRun(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	r.lister.setAnswer(func(listCall) (Listing, error) {
		once.Do(func() { close(entered) })
		<-release
		return listingOf(true, memA, memB, memC), nil
	})
	r.run(t)
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.runner.Fetch(context.Background(), Key{Name: "mine"}); err != nil {
				t.Error(err)
			}
		}()
	}
	<-entered
	time.Sleep(50 * time.Millisecond) // the other callers join the flight
	close(release)
	wg.Wait()
	if r.lister.count() != 1 {
		t.Errorf("3 concurrent callers made %d list calls, want 1", r.lister.count())
	}
}

func TestFetch_ARunStillGoingAtTheDeadlineCompletesAndIsStored(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	entered := make(chan struct{})
	r.lister.setAnswer(func(listCall) (Listing, error) {
		close(entered)
		<-release
		return listingOf(true, memA, memB, memC), nil
	})
	r.run(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := r.runner.Fetch(ctx, Key{Name: "mine"})
	if !errors.Is(err, ErrPending) {
		t.Fatalf("err = %v, want ErrPending", err)
	}
	<-entered
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if row, ok, _ := r.st.GetQuery(context.Background(), "mine"); ok && len(row.IDs) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run that outlived its caller was never stored")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRun_AFailureKeepsThePreviousIDsAndRecordsLastError(t *testing.T) {
	r := newRig(t)
	r.run(t)
	ctx := context.Background()
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(2 * time.Minute)
	r.lister.setAnswer(func(listCall) (Listing, error) { return Listing{}, errors.New("origin: HTTP 502: bad gateway") })
	_, err := r.runner.Fetch(ctx, Key{Name: "mine"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want the origin failure", err)
	}
	row := r.row(t, "mine")
	if !strings.Contains(row.LastError, "502") || len(row.IDs) != 3 || !row.LastSuccessAt.Equal(t0) || !row.FetchedAt.Equal(t0) {
		t.Errorf("row after the failure = %+v, want the old ids and times with last_error", row)
	}
	// The next success clears it.
	r.lister.setAnswer(func(listCall) (Listing, error) { return listingOf(true, memA, memB, memC), nil })
	r.clk.Advance(2 * time.Minute) // past the scheduler's retry hold
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	if got := r.row(t, "mine"); got.LastError != "" || !got.LastSuccessAt.Equal(r.clk.Now()) {
		t.Errorf("row after recovery = %+v", got)
	}
}

func TestRun_ABudgetRefusalIsReportedAndNotRecordedAsAnOriginFailure(t *testing.T) {
	r := newRig(t)
	r.run(t)
	ctx := context.Background()
	if _, err := r.runner.Fetch(ctx, Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(2 * time.Minute)
	r.lister.setAnswer(func(listCall) (Listing, error) {
		return Listing{}, &governor.Refusal{Kind: governor.RefusedCap}
	})
	_, err := r.runner.Fetch(ctx, Key{Name: "mine"})
	var refusal *governor.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != governor.RefusedCap {
		t.Fatalf("err = %v, want a cap refusal", err)
	}
	if got := r.row(t, "mine"); got.LastError != "" || len(got.IDs) != 3 {
		t.Errorf("row = %+v: a refusal is the budget speaking, not an origin failure", got)
	}
}

func TestRun_WhileUnauthenticatedNoListIsMade(t *testing.T) {
	r := newRig(t)
	r.run(t)
	r.auth.set(errors.New("bad credentials"))
	_, err := r.runner.Fetch(context.Background(), Key{Name: "mine"})
	if !errors.Is(err, errUnauthenticated) || !strings.Contains(err.Error(), "bad credentials") || r.lister.count() != 0 {
		t.Errorf("err = %v after %d list calls, want the injected auth error carrying the cause and no call", err, r.lister.count())
	}
	// Recovering clears it.
	r.auth.set(nil)
	r.clk.Advance(2 * time.Minute)
	if _, err := r.runner.Fetch(context.Background(), Key{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
}

func TestExpire_DeletesStaleAdhocRowsOnly(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	put := func(key string, configured bool, fetched time.Time) {
		if err := r.st.PutQuery(ctx, store.QueryRow{Key: key, Configured: configured, FetchedAt: fetched}); err != nil {
			t.Fatal(err)
		}
	}
	put("mine", true, t0.Add(-30*24*time.Hour))
	put("mine?label=old", false, t0.Add(-8*24*time.Hour))
	put("mine?label=new", false, t0.Add(-time.Hour))
	put("mine?label=failed", false, time.Time{})
	n, err := r.runner.Expire(ctx, 7*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("expired %d (%v), want 2", n, err)
	}
	rows, _ := r.st.ListQueries(ctx, false)
	var keys []string
	for _, row := range rows {
		keys = append(keys, row.Key)
	}
	if !slices.Equal(keys, []string{"mine", "mine?label=new"}) {
		t.Errorf("keys left = %v", keys)
	}
}

func TestNew_RequiresItsParts(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("New with nothing wired must fail")
	}
}

func TestNew_RecordingChangesNeedsTheMembershipRoles(t *testing.T) {
	r := newRig(t)
	cfg := Config{
		Settings: fakeSettings{}, Store: r.st, Aliases: r.aliases, Tracker: r.tracker, Lister: r.lister,
		Scheduler: r.sched, Bucket: bucketInvent, Changes: newEngine(t, r.clk),
	}
	if _, err := New(cfg); err == nil {
		t.Error("New with Changes but no roles in the spec must fail")
	}
}

func TestExprAndTTL_ComeFromTheSettings(t *testing.T) {
	r := newRig(t)
	if e, ok := r.runner.Expr("team"); !ok || !slices.Equal(e, []string{exprTeamOpen, exprTeamSecond}) {
		t.Errorf("Expr(team) = %v %v", e, ok)
	}
	if _, ok := r.runner.Expr("nope"); ok {
		t.Error("an unknown name must not be configured")
	}
	if got := r.runner.TTL(Key{Name: "team"}); got != ttlTeam {
		t.Errorf("TTL(team) = %v", got)
	}
	if got := r.runner.TTL(Key{Name: "team", Args: map[string]string{"a": "b"}}); got != ttlAdhoc {
		t.Errorf("TTL(adhoc) = %v", got)
	}
}
