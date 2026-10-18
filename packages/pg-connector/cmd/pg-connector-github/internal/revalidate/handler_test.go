package revalidate

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/entity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groups"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/groupsvc/groupsvctest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
)

const node = "PR_1"

// events records the revalidation metric events.
type events struct {
	mu  sync.Mutex
	all [][2]string
}

func (e *events) Revalidated(group, result string) {
	e.mu.Lock()
	e.all = append(e.all, [2]string{group, result})
	e.mu.Unlock()
}

func (e *events) count(group, result string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, ev := range e.all {
		if ev == [2]string{group, result} {
			n++
		}
	}
	return n
}

func (e *events) total() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.all)
}

type env struct {
	t   *testing.T
	r   *groupsvctest.Rig
	f   map[string]*groupsvctest.Fetcher
	ev  *events
	h   *Handler
	err []error
}

// newEnv wires a real store on a temp file, the real engine on a manual clock
// with a counting fake fetcher per group, and the handler over them. The
// scheduler is stepped by the test (Drain), never run in the background.
func newEnv(t *testing.T, o groupsvctest.RigOptions) *env {
	t.Helper()
	r := groupsvctest.NewRig(t, o)
	e := &env{t: t, r: r, f: map[string]*groupsvctest.Fetcher{}, ev: &events{}}
	for _, g := range []string{groups.Detail, groups.Conversation, groups.Files, groups.Commits, groups.Pending, groups.Runs} {
		e.f[g] = r.Register(groupsvctest.NewFetcher(g))
		e.f[g].Gov = o.Gov
	}
	e.h = New(Config{
		Store: r.Store, Engine: r.Svc, KeepAlive: r.Summary.InKeepAlive, Rules: r.Rules, Clock: r.Clock,
		Observer: e.ev, OnError: func(err error) { e.err = append(e.err, err) },
	})
	return e
}

func (e *env) drain() {
	e.t.Helper()
	e.r.Sched.Drain(context.Background())
}

func (e *env) calls(group string) int { return e.f[group].CallCount() }

// seedGroup fetches a group through the engine at the current time (class 2), so
// its row is the engine's own: fetched now, fresh for its ttl, and its next
// background task booked at fresh_until.
func (e *env) seedGroup(group string) {
	e.t.Helper()
	if err := e.r.Svc.QueueGroup(context.Background(), node, group, governor.ClassInvalidated, time.Time{}); err != nil {
		e.t.Fatal(err)
	}
	e.drain()
}

// refresh stores the next summary the way the summary service does (prev decoded
// from the stored row, then Persist) and calls the handler with the result. The
// mutation is applied to the previous summary; none means an unchanged summary.
func (e *env) refresh(mutate func(*summary.Summary)) summary.Stored {
	e.t.Helper()
	ctx := context.Background()
	var prev *summary.Summary
	cur := e.r.Origin.Summary(node)
	if g, ok, err := e.r.Store.GetGroup(ctx, entity.KindPR, node, summary.GroupName); err != nil {
		e.t.Fatal(err)
	} else if ok {
		var p summary.Summary
		if err := json.Unmarshal(g.Content, &p); err != nil {
			e.t.Fatal(err)
		}
		prev, cur = &p, p
	}
	if mutate != nil {
		mutate(&cur)
	}
	st, err := e.r.Persist.Persist(ctx, cur)
	if err != nil {
		e.t.Fatal(err)
	}
	e.h.OnSummaryRefreshed(prev, st)
	if len(e.err) != 0 {
		e.t.Fatalf("handler errors: %v", e.err)
	}
	return st
}

func (e *env) row(group string) store.GroupRow {
	e.t.Helper()
	row, ok := e.r.Group(node, group)
	if !ok {
		e.t.Fatalf("no %s row", group)
	}
	return row
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func TestHandlerHasTheHooksExactSignature(t *testing.T) {
	hook := (&Handler{}).OnSummaryRefreshed
	_ = summary.ServiceConfig{OnSummaryRefreshed: hook}
}

func TestAnUnchangedSummaryExtendsFreshUntilToTheMaxAgeAndTheMaximumForcesOneRepull(t *testing.T) {
	for _, group := range []string{groups.Detail, groups.Conversation} {
		t.Run(group, func(t *testing.T) {
			e := newEnv(t, groupsvctest.RigOptions{})
			e.r.SeedSummary(node, e.r.Clock.Now())
			t0 := e.r.Clock.Now()
			e.seedGroup(group)
			if got := e.calls(group); got != 1 {
				t.Fatalf("seed fetches = %d, want 1", got)
			}
			fetched := e.row(group)
			if want := t0.Add(10 * time.Minute); !fetched.FreshUntil.Equal(want) || !fetched.MaxAgeAt.Equal(t0.Add(30*time.Minute)) {
				t.Fatalf("seed row fresh_until %v max_age_at %v", fetched.FreshUntil, fetched.MaxAgeAt)
			}

			for k := 1; k <= 29; k++ {
				e.r.Clock.Advance(time.Minute)
				e.refresh(nil)
				e.drain()
				row := e.row(group)
				want := minTime(e.r.Clock.Now().Add(10*time.Minute), t0.Add(30*time.Minute))
				if !row.FreshUntil.Equal(want) {
					t.Fatalf("minute %d: fresh_until = %v, want %v", k, row.FreshUntil, want)
				}
				if !row.FetchedAt.Equal(t0) || string(row.Content) != string(fetched.Content) {
					t.Fatalf("minute %d: an extension changed the fetched content or fetched_at: %+v", k, row)
				}
				if got := e.calls(group); got != 1 {
					t.Fatalf("minute %d: %d fetches, want none before the maximum age", k, got)
				}
			}
			if got := e.ev.count(group, ResultExtended); got != 29 {
				t.Fatalf("extended events = %d, want 29", got)
			}
			if got := e.ev.count(group, ResultMaxAge); got != 0 {
				t.Fatalf("max_age events before the maximum = %d", got)
			}

			// The maximum age: the summary is unchanged again, nothing is extended,
			// and the task due at the maximum re-pulls the group exactly once.
			e.r.Clock.Advance(time.Minute)
			e.refresh(nil)
			if got := e.ev.count(group, ResultMaxAge); got != 1 {
				t.Fatalf("max_age events = %d, want 1", got)
			}
			if got := e.ev.count(group, ResultExtended); got != 29 {
				t.Fatalf("extended events = %d after the clamp, want 29", got)
			}
			if got := e.calls(group); got != 1 {
				t.Fatalf("%d fetches before the scheduler ran, want 1", got)
			}
			e.drain()
			if got := e.calls(group); got != 2 {
				t.Fatalf("%d fetches at the maximum age, want exactly one re-pull", got)
			}
			if row := e.row(group); !row.FetchedAt.Equal(t0.Add(30 * time.Minute)) {
				t.Errorf("re-pulled row fetched_at = %v, want the maximum age", row.FetchedAt)
			}
			e.drain()
			if got := e.calls(group); got != 2 {
				t.Errorf("%d fetches after a second drain, want still 2", got)
			}
			if got := e.ev.total(); got != 30 {
				t.Errorf("%d events in all, want 30 (29 extended and one max_age)", got)
			}
		})
	}
}

func TestAnUnchangedSummaryWithNothingStoredExtendsNothing(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.SeedSummary(node, e.r.Clock.Now())
	e.refresh(nil)
	e.drain()
	if e.ev.total() != 0 || e.calls(groups.Detail)+e.calls(groups.Conversation) != 0 {
		t.Errorf("events %d, fetches detail %d conversation %d; want nothing", e.ev.total(), e.calls(groups.Detail), e.calls(groups.Conversation))
	}
}

func TestAMaxAgeRowWithNoQueuedTaskGetsItsForcedRepullBooked(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.SeedSummary(node, e.r.Clock.Now())
	e.seedGroup(groups.Detail)
	e.r.Sched.Cancel(node, groups.Detail)
	e.r.Clock.Advance(31 * time.Minute)
	e.refresh(nil)
	if got := e.ev.count(groups.Detail, ResultMaxAge); got != 1 {
		t.Fatalf("max_age events = %d", got)
	}
	e.drain()
	if got := e.calls(groups.Detail); got != 2 {
		t.Errorf("%d detail fetches, want the forced re-pull", got)
	}
}

func TestChangesQueueTheirDependentGroupsAtClass2OnceEach(t *testing.T) {
	all := []string{groups.Detail, groups.Conversation, groups.Files, groups.Commits, groups.Runs}
	tests := []struct {
		name   string
		mutate func(*summary.Summary)
		want   []string
	}{
		{"head", func(s *summary.Summary) { s.HeadRefOid = "head-2" }, []string{groups.Files, groups.Commits, groups.Runs}},
		{"base oid", func(s *summary.Summary) { s.BaseRefOid = "base-2" }, []string{groups.Files, groups.Commits, groups.Detail}},
		{"base name", func(s *summary.Summary) { s.BaseRefName = "release" }, []string{groups.Files, groups.Commits, groups.Detail}},
		{
			"head and base", func(s *summary.Summary) { s.HeadRefOid, s.BaseRefOid = "head-2", "base-2" },
			[]string{groups.Files, groups.Commits, groups.Runs, groups.Detail},
		},
		{"comment count", func(s *summary.Summary) { s.CommentCount++ }, []string{groups.Conversation}},
		{"review count", func(s *summary.Summary) { s.ReviewCount++ }, []string{groups.Conversation}},
		{"review thread count", func(s *summary.Summary) { s.ReviewThreadCount++ }, []string{groups.Conversation}},
		{"updatedAt only", func(s *summary.Summary) { s.UpdatedAt = "2026-10-12T09:30:00Z" }, []string{groups.Conversation}},
		{"checks state", func(s *summary.Summary) { s.ChecksState = "FAILURE" }, []string{groups.Runs}},
		{"title only", func(s *summary.Summary) { s.Title = "Another title" }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, groupsvctest.RigOptions{})
			e.r.SeedSummary(node, e.r.Clock.Now())
			e.refresh(tc.mutate)
			e.drain()
			want := map[string]bool{}
			for _, g := range tc.want {
				want[g] = true
			}
			for _, g := range all {
				wantCalls := 0
				if want[g] {
					wantCalls = 1
				}
				if got := e.calls(g); got != wantCalls {
					t.Errorf("%s fetches = %d, want %d", g, got, wantCalls)
				}
				for _, c := range e.f[g].Calls() {
					if c.Class != governor.ClassInvalidated {
						t.Errorf("%s fetched as class %v, want class 2", g, c.Class)
					}
				}
				wantEvents := 0
				if want[g] {
					wantEvents = 1
				}
				if got := e.ev.count(g, ResultRepulled); got != wantEvents {
					t.Errorf("%s repulled events = %d, want %d", g, got, wantEvents)
				}
			}
			if got := e.calls(groups.Pending); got != 0 {
				t.Errorf("pending fetches = %d, want 0", got)
			}
			if e.ev.total() != len(tc.want) {
				t.Errorf("%d events, want %d", e.ev.total(), len(tc.want))
			}
		})
	}
}

func TestAMergeableToConflictingMoveThroughUnknownQueuesDetailAndUnknownAloneQueuesNothing(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.SeedSummary(node, e.r.Clock.Now())

	// UNKNOWN alone: the persister carries MERGEABLE, so nothing changed.
	st := e.refresh(func(s *summary.Summary) { s.Mergeable = "UNKNOWN" })
	if st.MergeabilityChanged || st.Summary.Mergeable != "MERGEABLE" {
		t.Fatalf("persister result = %+v, want the carried MERGEABLE and no change", st)
	}
	e.drain()
	if e.ev.total() != 0 || e.calls(groups.Detail) != 0 {
		t.Fatalf("UNKNOWN alone: %d events, %d detail fetches; want none", e.ev.total(), e.calls(groups.Detail))
	}

	// The known value moves: detail is queued, and only detail.
	st = e.refresh(func(s *summary.Summary) { s.Mergeable = "CONFLICTING" })
	if !st.MergeabilityChanged {
		t.Fatalf("persister result = %+v, want a mergeability change", st)
	}
	e.drain()
	for _, g := range []string{groups.Conversation, groups.Files, groups.Commits, groups.Runs, groups.Pending} {
		if e.calls(g) != 0 {
			t.Errorf("%s fetched for a mergeability change", g)
		}
	}
	calls := e.f[groups.Detail].Calls()
	if len(calls) != 1 || calls[0].Class != governor.ClassInvalidated {
		t.Fatalf("detail calls = %+v, want one at class 2", calls)
	}
	if e.ev.count(groups.Detail, ResultRepulled) != 1 || e.ev.total() != 1 {
		t.Errorf("events = %v", e.ev.all)
	}
}

func TestAFirstSightingOfAKeptAlivePRBooksDetailConversationAndRunsOnly(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.refresh(nil) // no stored summary: a first sighting; the entity is not in the set yet
	if e.ev.total() != 0 {
		t.Fatalf("a PR outside the keep-alive set got %v", e.ev.all)
	}
	e.drain()
	for _, g := range groups.All()[1:] {
		if e.calls(g) != 0 {
			t.Errorf("%s fetched for an entity outside the keep-alive set", g)
		}
	}
	if _, queued, running, _ := e.r.Svc.Inspect(context.Background(), node, groups.Detail); queued || running {
		t.Error("a task is booked for an entity outside the keep-alive set")
	}

	// A new kept-alive PR: accessed now.
	const kept = "PR_2"
	if err := e.r.Store.TouchEntity(context.Background(), entity.KindPR, kept, e.r.Clock.Now()); err != nil {
		t.Fatal(err)
	}
	st, err := e.r.Persist.Persist(context.Background(), e.r.Origin.Summary(kept))
	if err != nil || !st.First {
		t.Fatalf("persist = %+v, %v; want a first sighting", st, err)
	}
	e.h.OnSummaryRefreshed(nil, st)
	for _, g := range PopulateGroups() {
		if e.ev.count(g, ResultRepulled) != 1 {
			t.Errorf("no repulled event for %s", g)
		}
	}
	if e.ev.total() != 3 {
		t.Errorf("events = %v, want repulled for detail, conversation and runs", e.ev.all)
	}
	e.drain()
	for _, g := range []string{groups.Detail, groups.Conversation, groups.Runs} {
		calls := e.f[g].Calls()
		if len(calls) != 1 || !reflect.DeepEqual(calls[0].NodeIDs(), []string{kept}) {
			t.Errorf("%s calls = %+v, want one for %s", g, calls, kept)
		}
		if len(calls) == 1 && calls[0].Class != governor.ClassKeepAlive {
			t.Errorf("%s fetched as class %v, want class 4 (not a configured-query member)", g, calls[0].Class)
		}
	}
	for _, g := range []string{groups.Files, groups.Commits, groups.Pending} {
		if e.calls(g) != 0 {
			t.Errorf("%s fetched on a first sighting", g)
		}
	}
}

func TestAFirstSightingOfAConfiguredQueryMemberIsBookedAtClass3(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.PutQuery("mine", node)
	e.refresh(nil)
	e.drain()
	for _, g := range PopulateGroups() {
		calls := e.f[g].Calls()
		if len(calls) != 1 || calls[0].Class != governor.ClassQuery {
			t.Errorf("%s calls = %+v, want one at class 3", g, calls)
		}
	}
}

func TestADependentQueuedAtClass2AndStillWaitingIsNotReplacedPostponedOrExtended(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.SeedSummary(node, e.r.Clock.Now())
	e.seedGroup(groups.Detail)
	before := e.row(groups.Detail)

	// A mergeability change queues detail at class 2; the scheduler is not
	// stepped, so the dependent refresh is still waiting.
	e.refresh(func(s *summary.Summary) { s.Mergeable = "CONFLICTING" })
	if c, queued, _, _ := e.r.Svc.Inspect(context.Background(), node, groups.Detail); !queued || c != governor.ClassInvalidated {
		t.Fatalf("detail task: class %v queued %v, want class 2 queued", c, queued)
	}

	// The next summary is unchanged: detail is not extended and its task is
	// neither replaced nor postponed.
	e.r.Clock.Advance(time.Minute)
	extendedBefore := e.ev.count(groups.Detail, ResultExtended)
	e.refresh(nil)
	if got := e.ev.count(groups.Detail, ResultExtended); got != extendedBefore {
		t.Errorf("detail extended while a dependent refresh waits")
	}
	if row := e.row(groups.Detail); !row.FreshUntil.Equal(before.FreshUntil) || row.Generation != before.Generation {
		t.Errorf("detail row changed while a dependent refresh waits: %+v, was %+v", row, before)
	}
	if c, queued, _, _ := e.r.Svc.Inspect(context.Background(), node, groups.Detail); !queued || c != governor.ClassInvalidated {
		t.Fatalf("detail task after the unchanged summary: class %v queued %v, want class 2 queued", c, queued)
	}
	if got := e.calls(groups.Detail); got != 1 {
		t.Fatalf("%d detail fetches before the dependent ran, want the seed fetch only", got)
	}

	// Once the dependent has run, extension resumes.
	e.drain()
	if got := e.calls(groups.Detail); got != 2 {
		t.Fatalf("%d detail fetches, want the dependent's refresh", got)
	}
	if c := e.f[groups.Detail].Calls()[1].Class; c != governor.ClassInvalidated {
		t.Errorf("the dependent fetched as class %v, want class 2", c)
	}
	e.r.Clock.Advance(time.Minute)
	e.refresh(nil)
	if got := e.ev.count(groups.Detail, ResultExtended); got != extendedBefore+1 {
		t.Errorf("extension did not resume: %d extended events, want %d", got, extendedBefore+1)
	}
}

func TestADependentRefreshInFlightIsNotExtendedOver(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.SeedSummary(node, e.r.Clock.Now())
	e.seedGroup(groups.Detail)
	before := e.row(groups.Detail)

	release := e.f[groups.Detail].Block()
	t.Cleanup(release)
	e.refresh(func(s *summary.Summary) { s.Mergeable = "CONFLICTING" })
	e.r.Sched.Step(context.Background())
	if !e.f[groups.Detail].WaitStarted(2, 10*time.Second) {
		t.Fatal("the dependent refresh never started")
	}
	e.r.Clock.Advance(time.Minute)
	e.refresh(nil)
	if row := e.row(groups.Detail); !row.FreshUntil.Equal(before.FreshUntil) || row.Generation != before.Generation {
		t.Errorf("detail row changed while its dependent refresh is running: %+v, was %+v", row, before)
	}
	if got := e.ev.count(groups.Detail, ResultExtended); got != 0 {
		t.Errorf("extended %d times while a dependent refresh is running", got)
	}
	release()
	e.r.Sched.Wait()
}

func TestAnInvalidationBetweenReadingTheRowAndWritingTheExtensionMakesItANoOp(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.SeedSummary(node, e.r.Clock.Now())
	e.seedGroup(groups.Detail)
	e.r.Clock.Advance(time.Minute)

	e.h.beforeCommit = func(n, g string) {
		if err := groups.Invalidate(context.Background(), e.r.Store, entity.KindPR, n, g, e.r.Clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	e.refresh(nil)

	row := e.row(groups.Detail)
	if !row.FreshUntil.Equal(e.r.Clock.Now()) || row.Generation != 2 {
		t.Errorf("row = fresh_until %v generation %d, want the invalidation's (now, generation 2): nothing stored over it",
			row.FreshUntil, row.Generation)
	}
	if got := e.ev.count(groups.Detail, ResultExtended); got != 0 {
		t.Errorf("an extension that lost was reported %d times", got)
	}
}

func TestTheHandlerWritesNoChangeRowAndCreatesNoOtherTable(t *testing.T) {
	// The handler's only side effects are group rows and scheduler tasks: after a
	// full set of triggers the store holds the groups the engine fetched and the
	// summary, nothing else of an entity.
	e := newEnv(t, groupsvctest.RigOptions{})
	e.r.SeedSummary(node, e.r.Clock.Now())
	e.refresh(func(s *summary.Summary) {
		s.HeadRefOid, s.BaseRefOid, s.CommentCount, s.ChecksState = "head-2", "base-2", 9, "FAILURE"
	})
	e.drain()
	rows, err := e.r.Store.ListGroups(context.Background(), entity.KindPR, node)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Group] = true
	}
	want := map[string]bool{
		groups.Summary: true, groups.Detail: true, groups.Conversation: true, groups.Files: true, groups.Commits: true, groups.Runs: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored groups = %v, want %v", got, want)
	}
}

func TestWithoutAnObserverTheEventsGoToTheEnginesObserver(t *testing.T) {
	e := newEnv(t, groupsvctest.RigOptions{})
	ev := &events{}
	e.r.Svc.SetObserver(observerAdapter{ev})
	h := New(Config{Store: e.r.Store, Engine: e.r.Svc, KeepAlive: e.r.Summary.InKeepAlive, Rules: e.r.Rules, Clock: e.r.Clock})
	e.r.SeedSummary(node, e.r.Clock.Now())
	cur := e.r.Origin.Summary(node)
	next := cur
	next.CommentCount++
	st, err := e.r.Persist.Persist(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	h.OnSummaryRefreshed(&cur, st)
	if got := ev.count(groups.Conversation, ResultRepulled); got != 1 {
		t.Errorf("engine observer saw %d repulled events, want 1", got)
	}
}

type observerAdapter struct{ *events }

func (observerAdapter) Served(string, time.Duration) {}
