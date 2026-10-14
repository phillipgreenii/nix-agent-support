package summary

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const (
	testTTL      = 60 * time.Second
	testTerminal = 24 * time.Hour
	testWindow   = 24 * time.Hour
	testExpiry   = 7 * 24 * time.Hour
)

// countingFetcher is the fetcher fake: it records every call, answers each id
// with a pull request, and can block, fail or spend through a governor.
type countingFetcher struct {
	clk *governor.ManualClock

	mu      sync.Mutex
	calls   []fetchCall
	numbers map[string]int
	state   map[string]string // per id; default OPEN
	gone    map[string]bool
	err     error
	// block, when set, is waited on inside Fetch (after entered is closed once).
	block   chan struct{}
	entered chan struct{}
	ctxErr  error // ctx.Err() seen by the last unblocked call
	// gov, when set, admits and settles one point per call, as the real Fetcher does.
	gov *governor.Governor
}

type fetchCall struct {
	ids   []string
	class governor.Class
}

func (f *countingFetcher) Fetch(ctx context.Context, call ratefeed.Meta, ids []string) ([]Summary, int64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fetchCall{ids: slices.Clone(ids), class: call.Class})
	block, entered, gov, ferr := f.block, f.entered, f.gov, f.err
	if entered != nil {
		f.entered = nil
		close(entered)
	}
	f.mu.Unlock()
	if gov != nil {
		tk, err := gov.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1})
		if err != nil {
			f.mu.Lock()
			f.calls = f.calls[:len(f.calls)-1] // refused: nothing was fetched
			f.mu.Unlock()
			return nil, 0, err
		}
		_ = tk.Settle(governor.Spend{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1, Outcome: "ok"})
	}
	if block != nil {
		<-block
		f.mu.Lock()
		f.ctxErr = ctx.Err()
		f.mu.Unlock()
	}
	if ferr != nil {
		return nil, 0, ferr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Summary, len(ids))
	for i, id := range ids {
		if f.gone[id] {
			out[i] = Summary{NodeID: id, Gone: true}
			continue
		}
		if f.numbers == nil {
			f.numbers = map[string]int{}
		}
		if f.numbers[id] == 0 {
			f.numbers[id] = len(f.numbers) + 1
		}
		s := sample()
		s.NodeID, s.Number = id, f.numbers[id]
		if st, ok := f.state[id]; ok {
			s.State = st
		}
		out[i] = s
	}
	return out, 1, nil
}

func (f *countingFetcher) callList() []fetchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type rig struct {
	st    *store.Store
	clk   *governor.ManualClock
	sched *scheduler.Scheduler
	svc   *Service
	fetch *countingFetcher
	pers  *Persister
}

type rigOpts struct {
	gov       *governor.Governor
	workers   int
	batchSize int
	onRefresh func(prev *Summary, next Stored)
	onBatch   func(ids []string, role scheduler.Role)
	hook      StoreHook
	onGone    GoneHook
	// reuse an existing store and clock (a simulated restart).
	st  *store.Store
	clk *governor.ManualClock
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	st, clk := o.st, o.clk
	if st == nil {
		var err error
		st, _, err = store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
	}
	if clk == nil {
		clk = governor.NewManualClock(t0)
	}
	if o.workers == 0 {
		o.workers = 1
	}
	if o.batchSize == 0 {
		o.batchSize = MaxBatch
	}
	f := &countingFetcher{clk: clk, gov: o.gov}
	pers := &Persister{Store: st, Identity: entity.NewIdentity(st, clk), TTL: testTTL, TerminalTTL: testTerminal, Clock: clk, Hook: o.hook}
	opt := scheduler.Options{Clock: clk, Workers: o.workers, FillHorizon: testTTL}
	if o.gov != nil {
		opt.Gate = o.gov
	}
	sched := scheduler.New(opt)
	svc, err := NewService(ServiceConfig{
		Store: st, Persister: pers, Fetcher: f, Identity: pers.Identity, Scheduler: sched, Clock: clk,
		KeepaliveWindow: testWindow, Expiry: testExpiry, BatchSize: o.batchSize,
		OnSummaryRefreshed: o.onRefresh, OnBatchSent: o.onBatch, OnGone: o.onGone,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{st: st, clk: clk, sched: sched, svc: svc, fetch: f, pers: pers}
}

func (r *rig) putEntity(t *testing.T, id string, lastAccess time.Time) {
	t.Helper()
	if err := r.st.PutEntity(context.Background(), store.EntityRow{Kind: entity.KindPR, ID: id, LastAccess: lastAccess}); err != nil {
		t.Fatal(err)
	}
}

// putGroup seeds a stored summary group fetched at fetched, fresh until fresh.
func (r *rig) putGroup(t *testing.T, id, group string, fetched, fresh time.Time) {
	t.Helper()
	err := r.st.PutGroup(context.Background(), store.GroupRow{
		Kind: entity.KindPR, ID: id, Group: group, Content: []byte(`{}`), FetchedAt: fetched, FreshUntil: fresh,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *rig) putQuery(t *testing.T, key string, configured bool, ids ...string) {
	t.Helper()
	if err := r.st.PutQuery(context.Background(), store.QueryRow{Key: key, Configured: configured, IDs: ids, Complete: true}); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) group(t *testing.T, id string) (store.GroupRow, bool) {
	t.Helper()
	g, ok, err := r.st.GetGroup(context.Background(), entity.KindPR, id, GroupName)
	if err != nil {
		t.Fatal(err)
	}
	return g, ok
}

func ids(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return out
}

func TestKeepAliveMembershipAndWindowBoundaries(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	r.putQuery(t, "mine", true, "member-1", "old-and-member")
	r.putQuery(t, "adhoc", false, "adhoc-only") // a query that is not configured does not keep anything alive
	r.putEntity(t, "member-1", time.Time{})
	r.putEntity(t, "adhoc-only", time.Time{})
	r.putEntity(t, "inside", t0.Add(-testWindow+time.Second))
	r.putEntity(t, "at-edge", t0.Add(-testWindow))
	r.putEntity(t, "outside", t0.Add(-testWindow-time.Second))
	r.putEntity(t, "never", time.Time{})
	r.putEntity(t, "old-and-member", t0.Add(-30*24*time.Hour))
	for id, want := range map[string]bool{
		"member-1": true, "adhoc-only": false, "inside": true, "at-edge": true, "outside": false,
		"never": false, "old-and-member": true, // a member stays in regardless of access
	} {
		got, err := r.svc.InKeepAlive(ctx, id)
		if err != nil || got != want {
			t.Errorf("InKeepAlive(%s) = %v %v, want %v", id, got, err, want)
		}
	}
}

func TestMembershipAcceptsWireReferencesInQueryRows(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	ref, _ := entity.ParseRef("Acme-Org/Widget#7")
	if _, err := r.pers.Identity.Put(ctx, "PR_7", ref); err != nil {
		t.Fatal(err)
	}
	r.putQuery(t, "mine", true, "acme-org/widget#7", "other-org/thing#1") // the second is not known: left out
	r.putEntity(t, "PR_7", time.Time{})
	if ok, err := r.svc.InKeepAlive(ctx, "PR_7"); err != nil || !ok {
		t.Fatalf("a member listed by reference must be in the set: %v %v", ok, err)
	}
}

func TestInteractiveStaleSummaryGoesOutAtOnceWithSpareSlotsFilled(t *testing.T) {
	var mu sync.Mutex
	var roles []string
	r := newRig(t, rigOpts{onBatch: func(ids []string, role scheduler.Role) {
		mu.Lock()
		defer mu.Unlock()
		roles = append(roles, fmt.Sprintf("%s:%v", role, ids))
	}})
	ctx := context.Background()
	// Three entities in the set that go stale within the next summary_ttl.
	for i, id := range []string{"near-a", "near-b", "near-c"} {
		r.putEntity(t, id, t0.Add(-time.Hour))
		r.putGroup(t, id, GroupName, t0, t0.Add(time.Duration(10*(i+1))*time.Second))
	}
	r.putEntity(t, "far", t0.Add(-time.Hour))
	r.putGroup(t, "far", GroupName, t0, t0.Add(time.Hour)) // outside the horizon
	if _, err := r.svc.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	r.putEntity(t, "wanted", time.Time{}) // stale: no summary at all

	res := make(chan []InteractiveResult, 1)
	go func() { res <- r.svc.Interactive(ctx, []string{"wanted"}) }()
	deadline := time.After(10 * time.Second)
	var got []InteractiveResult
poll:
	for {
		r.sched.Step(ctx)
		select {
		case got = <-res:
			break poll
		case <-deadline:
			t.Fatal("interactive refresh never completed")
		case <-time.After(time.Millisecond):
		}
	}
	r.sched.Wait()
	if len(got) != 1 || !got[0].Fresh || got[0].Err != nil {
		t.Fatalf("result = %+v", got)
	}
	calls := r.fetch.callList()
	if len(calls) != 1 {
		t.Fatalf("fetcher called %d times, want 1", len(calls))
	}
	if calls[0].class != governor.ClassInteractive {
		t.Errorf("admitted as %v, want interactive", calls[0].class)
	}
	if want := []string{"wanted", "near-a", "near-b", "near-c"}; !slices.Equal(calls[0].ids, want) {
		t.Errorf("batch ids = %v, want %v (the interactive id first, then the nearest to going stale; far is outside the horizon)", calls[0].ids, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"due:[wanted]", "fill:[near-a near-b near-c]"}; !slices.Equal(roles, want) {
		t.Errorf("OnBatchSent = %v, want %v", roles, want)
	}
	if g, ok := r.group(t, "wanted"); !ok || !g.FreshUntil.Equal(t0.Add(testTTL)) {
		t.Errorf("stored group = %+v %v", g, ok)
	}
}

func TestInteractiveOfAFreshSummaryCostsNothing(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.putEntity(t, "e", time.Time{})
	r.putGroup(t, "e", GroupName, t0, t0.Add(time.Minute))
	got := r.svc.Interactive(context.Background(), []string{"e"})
	if !got[0].Fresh || len(r.fetch.callList()) != 0 {
		t.Fatalf("result = %+v, calls = %d", got[0], len(r.fetch.callList()))
	}
	// The read still counts as an access.
	row, _, _ := r.st.GetEntity(context.Background(), entity.KindPR, "e")
	if !row.LastAccess.Equal(t0) || !row.KeepAlive {
		t.Errorf("entity row = %+v, want accessed now and kept alive", row)
	}
}

func TestInteractiveFreshFetchesEvenWhenTheSummaryIsInsideFreshUntil(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.sched.Run(ctx)
	r.putEntity(t, "e", time.Time{})
	r.putGroup(t, "e", GroupName, t0, t0.Add(time.Minute))

	if got := r.svc.Interactive(ctx, []string{"e"}); !got[0].Fresh || len(r.fetch.callList()) != 0 {
		t.Fatalf("control: Interactive of a fresh summary fetched (%+v, %d calls)", got[0], len(r.fetch.callList()))
	}
	got := r.svc.InteractiveFresh(ctx, []string{"e"})
	if got[0].Err != nil || !got[0].Fresh {
		t.Fatalf("result = %+v", got[0])
	}
	calls := r.fetch.callList()
	if len(calls) != 1 || len(calls[0].ids) != 1 || calls[0].ids[0] != "e" || calls[0].class != governor.ClassInteractive {
		t.Errorf("calls = %+v, want one interactive fetch of e", calls)
	}
}

func TestInteractiveFailureAndRefusalAreReported(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	r.fetch.err = errors.New("origin unreachable")
	done := make(chan []InteractiveResult, 1)
	go func() { done <- r.svc.Interactive(ctx, []string{"e", "owner/unknown#1"}) }()
	var got []InteractiveResult
	for got == nil {
		r.sched.Step(ctx)
		select {
		case got = <-done:
		case <-time.After(time.Millisecond):
		}
	}
	if got[0].Fresh || got[0].Err == nil {
		t.Errorf("a failed refresh must not be fresh: %+v", got[0])
	}
	if !errors.Is(got[1].Err, ErrUnknownEntity) {
		t.Errorf("unknown reference: %+v", got[1])
	}
}

func TestConcurrentRequestsForOneEntityShareOneFetch(t *testing.T) {
	r := newRig(t, rigOpts{workers: 2})
	r.fetch.block = make(chan struct{})
	r.fetch.entered = make(chan struct{})
	entered := r.fetch.entered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.sched.Run(ctx)

	var wg sync.WaitGroup
	results := make([][]InteractiveResult, 3)
	start := func(i int) {
		wg.Add(1)
		go func() { defer wg.Done(); results[i] = r.svc.Interactive(context.Background(), []string{"e"}) }()
	}
	start(0)
	<-entered // the first fetch is running
	start(1)
	start(2)
	time.Sleep(50 * time.Millisecond) // let the others reach the scheduler
	close(r.fetch.block)
	wg.Wait()
	if n := len(r.fetch.callList()); n != 1 {
		t.Fatalf("the counting fetcher saw %d calls, want 1 shared fetch", n)
	}
	for i, res := range results {
		if !res[0].Fresh || res[0].Err != nil {
			t.Errorf("caller %d: %+v", i, res[0])
		}
	}
}

func TestFetchStillRunningAtTheCallersDeadlineCompletesAndIsStored(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.fetch.block = make(chan struct{})
	r.fetch.entered = make(chan struct{})
	entered := r.fetch.entered
	bg, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.sched.Run(bg)

	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	got := r.svc.Interactive(ctx, []string{"e"})
	<-entered
	if !got[0].Pending || got[0].Fresh {
		t.Fatalf("at the deadline the result must be pending: %+v", got[0])
	}
	if _, ok := r.group(t, "e"); ok {
		t.Fatal("nothing is stored before the fetch returns")
	}
	close(r.fetch.block)
	deadline := time.After(10 * time.Second)
	for {
		if g, ok := r.group(t, "e"); ok {
			if !g.FreshUntil.Equal(t0.Add(testTTL)) {
				t.Fatalf("stored group = %+v", g)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("the fetch did not complete in the background")
		case <-time.After(5 * time.Millisecond):
		}
	}
	r.fetch.mu.Lock()
	defer r.fetch.mu.Unlock()
	if r.fetch.ctxErr != nil {
		t.Errorf("the fetch's context was canceled with the caller's: %v", r.fetch.ctxErr)
	}
}

func TestTerminalTTLAppliesToMergedAndClosedPRs(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	r.fetch.state = map[string]string{"merged": "MERGED", "closed": "CLOSED", "open": "OPEN"}
	for _, id := range []string{"merged", "closed", "open"} {
		r.putEntity(t, id, t0)
	}
	if _, err := r.svc.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	for id, want := range map[string]time.Duration{"merged": testTerminal, "closed": testTerminal, "open": testTTL} {
		g, ok := r.group(t, id)
		if !ok || g.FreshUntil.Sub(g.FetchedAt) != want {
			t.Errorf("%s: fresh for %v, want %v (%+v)", id, g.FreshUntil.Sub(g.FetchedAt), want, g)
		}
	}
	// The next refresh of a terminal PR is a terminal_ttl away, not a summary_ttl.
	r.clk.Advance(2 * testTTL)
	before := len(r.fetch.callList())
	r.sched.Drain(ctx)
	calls := r.fetch.callList()[before:]
	if len(calls) != 1 || !slices.Equal(calls[0].ids, []string{"open"}) {
		t.Fatalf("after two summary_ttls only the open PR refreshes, got %v", calls)
	}
}

func TestSummaryTTLRefreshOfEveryMemberAndTheRefreshedCallback(t *testing.T) {
	type seen struct {
		prev bool
		id   string
	}
	var mu sync.Mutex
	var cb []seen
	r := newRig(t, rigOpts{onRefresh: func(prev *Summary, next Stored) {
		mu.Lock()
		defer mu.Unlock()
		cb = append(cb, seen{prev: prev != nil, id: next.Summary.NodeID})
	}})
	ctx := context.Background()
	members := ids("pr-", 3)
	r.putQuery(t, "mine", true, members...)
	for _, id := range members {
		r.putEntity(t, id, time.Time{})
	}
	if n, err := r.svc.Rebuild(ctx); err != nil || n != 3 {
		t.Fatalf("Rebuild = %d %v", n, err)
	}
	r.sched.Drain(ctx)
	if got := len(r.fetch.callList()); got != 1 {
		t.Fatalf("first pass: %d calls, want 1 batch of the three members", got)
	}
	r.clk.Advance(testTTL - time.Second)
	r.sched.Drain(ctx)
	if got := len(r.fetch.callList()); got != 1 {
		t.Fatalf("a member is refreshed on summary_ttl, not sooner (%d calls)", got)
	}
	r.clk.Advance(2 * time.Second)
	r.sched.Drain(ctx)
	calls := r.fetch.callList()
	if len(calls) != 2 || len(calls[1].ids) != 3 {
		t.Fatalf("second pass: %v", calls)
	}
	if calls[1].class != governor.ClassQuery {
		t.Errorf("members of a configured query refresh as class 3, got %v", calls[1].class)
	}
	mu.Lock()
	defer mu.Unlock()
	var first, again int
	for _, s := range cb {
		if s.prev {
			again++
		} else {
			first++
		}
	}
	if first != 3 || again != 3 {
		t.Errorf("OnSummaryRefreshed: %d first sightings, %d with a previous summary, want 3 and 3", first, again)
	}
}

func TestAnEntityOutsideTheSetIsNotRefreshed(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	r.putEntity(t, "in", t0.Add(-time.Hour))
	r.putGroup(t, "in", GroupName, t0.Add(-time.Hour), t0.Add(-time.Minute))
	r.putEntity(t, "out", t0.Add(-testWindow-time.Hour))
	r.putGroup(t, "out", GroupName, t0.Add(-time.Hour), t0.Add(-time.Minute))
	// Both have a queued task (as after a restart that queued them, or after the
	// entity fell out of the set while its task waited).
	for _, id := range []string{"in", "out"} {
		if err := r.sched.Submit(scheduler.Task{
			Kind: GroupName, Entity: id, Group: GroupName,
			Class: governor.ClassKeepAlive, Due: t0.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	r.sched.Drain(ctx)
	calls := r.fetch.callList()
	if len(calls) != 1 || !slices.Equal(calls[0].ids, []string{"in"}) {
		t.Fatalf("calls = %v, want one batch holding only the entity in the set", calls)
	}
	if r.sched.Queued("out", GroupName) {
		t.Error("the task of an entity outside the set must be dropped")
	}
	if g, _ := r.group(t, "out"); !g.FreshUntil.Equal(t0.Add(-time.Minute)) {
		t.Error("an entity outside the set must not be refreshed")
	}
}

func TestExpiryEvictsOnlyOnceEveryGroupIsOlderThanExpiry(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	old := t0.Add(-testExpiry - time.Second)
	justInside := t0.Add(-testExpiry + time.Second)
	out := t0.Add(-testWindow - time.Hour) // outside keepalive_window

	r.putEntity(t, "one-group-inside", out)
	r.putGroup(t, "one-group-inside", GroupName, old, old)
	r.putGroup(t, "one-group-inside", "detail", justInside, justInside)

	r.putEntity(t, "all-older", out)
	r.putGroup(t, "all-older", GroupName, old, old)
	r.putGroup(t, "all-older", "detail", old.Add(-time.Hour), old)

	r.putEntity(t, "in-the-set", t0.Add(-time.Hour)) // accessed recently: never evicted
	r.putGroup(t, "in-the-set", GroupName, old, old)

	r.putEntity(t, "member", out)
	r.putGroup(t, "member", GroupName, old, old)
	r.putQuery(t, "mine", true, "member")

	n, err := r.svc.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("Sweep evicted %d (%v), want 1", n, err)
	}
	if _, ok, _ := r.st.GetEntity(ctx, entity.KindPR, "all-older"); ok {
		t.Error("an entity whose every group is older than expiry must be evicted")
	}
	if gs, _ := r.st.ListGroups(ctx, entity.KindPR, "all-older"); len(gs) != 0 {
		t.Errorf("its groups must go with it: %v", gs)
	}
	for _, id := range []string{"one-group-inside", "in-the-set", "member"} {
		if _, ok, _ := r.st.GetEntity(ctx, entity.KindPR, id); !ok {
			t.Errorf("%s must be kept", id)
		}
	}
	// One more second and the last group of "one-group-inside" is past expiry too.
	r.clk.Advance(2 * time.Second)
	if n, err := r.svc.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("second Sweep evicted %d (%v), want 1", n, err)
	}
	// The sweep also keeps the keep-alive flag current.
	row, _, _ := r.st.GetEntity(ctx, entity.KindPR, "member")
	if !row.KeepAlive {
		t.Error("Sweep must record the keep-alive flag of a member")
	}
}

func TestTrackQueuesClass3SummariesAndReportsUnresolvedRefs(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	ref, _ := entity.ParseRef("Acme-Org/Widget#7")
	if _, err := r.pers.Identity.Put(ctx, "PR_7", ref); err != nil {
		t.Fatal(err)
	}
	unresolved, err := r.svc.Track(ctx, []string{"acme-org/widget#7", "acme-org/widget#8"})
	if err != nil || !slices.Equal(unresolved, []string{"acme-org/widget#8"}) {
		t.Fatalf("Track = %v %v", unresolved, err)
	}
	if _, ok, _ := r.st.GetEntity(ctx, entity.KindPR, "PR_7"); !ok {
		t.Error("Track must create the missing entity row")
	}
	if n, ok := r.sched.Position("PR_7", GroupName); !ok || n != 1 {
		t.Errorf("Position = %d %v", n, ok)
	}
	if n, ok, err := r.svc.Position(ctx, "acme-org/widget#7"); err != nil || !ok || n != 1 {
		t.Errorf("service Position by reference = %d %v %v", n, ok, err)
	}
	if st := r.sched.State(); st.Classes[2].Depth != 1 {
		t.Errorf("a tracked entity is a class 3 task: %+v", st.Classes)
	}
	// Not in the keep-alive set (never accessed, in no configured query), yet the
	// first fetch happens: class 3 is the query's own work.
	r.sched.Drain(ctx)
	if g, ok := r.group(t, "PR_7"); !ok || g.Fingerprint == "" {
		t.Fatal("a tracked entity must get its first summary")
	}
	if r.sched.Queued("PR_7", GroupName) {
		t.Error("outside the set, no next cycle is booked")
	}
}

func TestAccessRaisesPriorityAndKeepsTheEntityAlive(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		r.putEntity(t, id, time.Time{})
		r.putGroup(t, id, GroupName, t0.Add(-time.Hour), t0.Add(-time.Minute))
	}
	if err := r.svc.Access(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	r.clk.Advance(time.Second)
	if err := r.svc.Access(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.svc.InKeepAlive(ctx, "a"); !ok {
		t.Fatal("an accessed entity is in the set")
	}
	r.sched.Drain(ctx)
	calls := r.fetch.callList()
	if len(calls) != 1 || !slices.Equal(calls[0].ids, []string{"b", "a"}) {
		t.Fatalf("calls = %v, want one batch, most recently accessed first", calls)
	}
}

func TestAGoneNodeIsTombstonedAndNoLongerRefreshed(t *testing.T) {
	r := newRig(t, rigOpts{})
	ctx := context.Background()
	r.fetch.gone = map[string]bool{"deleted": true}
	r.putEntity(t, "deleted", t0)
	if _, err := r.svc.Rebuild(ctx); err != nil {
		t.Fatal(err)
	}
	r.sched.Drain(ctx)
	row, _, _ := r.st.GetEntity(ctx, entity.KindPR, "deleted")
	if row.TombstonedAt.IsZero() {
		t.Fatal("a node GitHub no longer returns must be tombstoned")
	}
	r.clk.Advance(time.Hour)
	r.sched.Drain(ctx)
	if got := len(r.fetch.callList()); got != 1 {
		t.Errorf("a tombstoned entity is not refreshed again (%d calls)", got)
	}
}

func TestAfterARestartTheQueueIsRebuiltAndSpendStaysInsideTheCap(t *testing.T) {
	ctx := context.Background()
	st, _, err := store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	clk := governor.NewManualClock(t0)

	// A daemon that has been down for days: 200 entities in the set, every
	// summary long stale.
	const cap = 5
	all := ids("pr-", 200)
	for _, id := range all {
		if err := st.PutEntity(ctx, store.EntityRow{Kind: entity.KindPR, ID: id, LastAccess: t0.Add(-time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if err := st.PutGroup(ctx, store.GroupRow{
			Kind: entity.KindPR, ID: id, Group: GroupName, Content: []byte(`{}`),
			FetchedAt: t0.Add(-72 * time.Hour), FreshUntil: t0.Add(-72*time.Hour + testTTL),
		}); err != nil {
			t.Fatal(err)
		}
	}
	gov, err := governor.New(governor.Config{
		GraphQL: governor.Limits{Cap: cap}, RESTCore: governor.Limits{Cap: 100}, RESTSearch: governor.Limits{Cap: 100},
		BackgroundShare: 1,
	}, governor.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}

	// The restart: a NEW scheduler and service over the same store.
	r := newRig(t, rigOpts{st: st, clk: clk, gov: gov, workers: 4, batchSize: 10})
	if n, err := r.svc.Rebuild(ctx); err != nil || n != 200 {
		t.Fatalf("Rebuild queued %d (%v), want 200", n, err)
	}
	spent := func() int64 {
		bs, _ := gov.State().Bucket(governor.BucketGraphQL)
		return bs.Spent
	}
	r.sched.Drain(ctx)
	if got := len(r.fetch.callList()); got != cap || spent() != cap {
		t.Fatalf("first window: %d calls, spent %d, want exactly the cap %d", got, spent(), cap)
	}
	if n := r.sched.Step(ctx); n != 0 {
		t.Fatalf("Step started %d batches against a spent cap", n)
	}
	// As the window moves the queue drains, a cap's worth at a time.
	for window := 2; window <= 4; window++ {
		clk.Advance(time.Hour + time.Second)
		r.sched.Drain(ctx)
		if spent() > cap {
			t.Fatalf("window %d: spent %d past the cap %d", window, spent(), cap)
		}
		if got := len(r.fetch.callList()); got != cap*window {
			t.Fatalf("window %d: %d calls in all, want %d", window, got, cap*window)
		}
	}
	for _, c := range r.fetch.callList() {
		if len(c.ids) > 10 {
			t.Fatalf("batch of %d ids exceeds batch_size", len(c.ids))
		}
	}
}

func TestServiceRejectsAnIncompleteConfig(t *testing.T) {
	if _, err := NewService(ServiceConfig{}); err == nil {
		t.Fatal("NewService accepted an empty config")
	}
}
