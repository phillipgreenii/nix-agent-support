package eventqueue

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Tests for the dispatch timeline (bead pg2-n7da9): the start instant recorded
// when an offer goes in-flight, the wait/run split handed to a TimingObserver,
// the timestamps on durable accept/evict records, the live oldest-pending-age and
// per-listener in-flight accessors, and the bounded timestamped history compaction
// keeps (opArchive).

// timingObserver is a recordingObserver that also implements TimingObserver.
type timingObserver struct {
	recordingObserver
	mu      sync.Mutex
	timings []DispatchTiming
	signal  chan struct{}
}

func (o *timingObserver) OnAcceptTiming(t DispatchTiming) {
	o.mu.Lock()
	o.timings = append(o.timings, t)
	o.mu.Unlock()
	if o.signal != nil {
		o.signal <- struct{}{}
	}
}

func (o *timingObserver) got() []DispatchTiming {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]DispatchTiming(nil), o.timings...)
}

// runListener advances the shared mock clock by run during Offer — a handler that
// "takes" run — and records the Offering.StartedAt it was handed.
type runListener struct {
	fakeListener
	clk       *mockClock
	run       time.Duration
	startedAt []time.Time
}

func (l *runListener) Offer(o Offering) OfferResult {
	l.startedAt = append(l.startedAt, o.StartedAt)
	l.clk.advance(l.run)
	return l.fakeListener.Offer(o)
}

func newRunListener(clk *mockClock, id string, run time.Duration, types ...string) *runListener {
	return &runListener{fakeListener: *newListener(id, types...), clk: clk, run: run}
}

func TestDispatchTimingSplitsWaitFromRun(t *testing.T) {
	clk := newClock()
	obs := &timingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := newRunListener(clk, "desk-pr", 5*time.Second, "pr.changed")
	q.Register(l)

	enqueuedAt := clk.t
	mustEnqueue(t, q, evtUntil("e1", "pr.changed", clk.in(time.Hour)))
	clk.advance(3 * time.Second) // the event waits 3s before the listener is offered it
	startedAt := clk.t
	if n := q.Dispatch(); n != 1 {
		t.Fatalf("Dispatch accepted %d, want 1", n)
	}

	got := obs.got()
	if len(got) != 1 {
		t.Fatalf("timings = %+v, want exactly one", got)
	}
	tm := got[0]
	if tm.EventID != "e1" || tm.EventType != "pr.changed" || tm.ListenerID != "desk-pr" {
		t.Fatalf("timing identity = %+v", tm)
	}
	if !tm.EnqueuedAt.Equal(enqueuedAt) || !tm.StartedAt.Equal(startedAt) || !tm.SettledAt.Equal(startedAt.Add(5*time.Second)) {
		t.Fatalf("timing instants = %+v, want enqueued %v started %v settled %v", tm, enqueuedAt, startedAt, startedAt.Add(5*time.Second))
	}
	if tm.Wait() != 3*time.Second || tm.Run() != 5*time.Second {
		t.Fatalf("wait/run = %v/%v, want 3s/5s", tm.Wait(), tm.Run())
	}
	if len(l.startedAt) != 1 || !l.startedAt[0].Equal(startedAt) {
		t.Fatalf("Offering.StartedAt = %v, want the in-flight instant %v", l.startedAt, startedAt)
	}
	// OnAccept still fires, and before the timing for the same accept.
	if len(obs.accepted) != 1 || obs.accepted[0] != "e1/desk-pr" {
		t.Fatalf("OnAccept = %v", obs.accepted)
	}
}

func TestDispatchTimingNotReportedForADecline(t *testing.T) {
	clk := newClock()
	obs := &timingObserver{}
	q := newQueue(t, clk, WithObserver(obs))
	l := newListener("h", "T")
	l.neverAccept = true
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	q.Dispatch()
	if got := obs.got(); len(got) != 0 {
		t.Fatalf("a decline reported a timing: %+v", got)
	}
}

func TestDispatchTimingFlooredAtZeroForAFutureSourceStamp(t *testing.T) {
	d := DispatchTiming{
		EnqueuedAt: time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC),
		StartedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		SettledAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if d.Wait() != 0 || d.Run() != 0 {
		t.Fatalf("wait/run = %v/%v, want 0/0 (never negative)", d.Wait(), d.Run())
	}
}

// Kick settles per offer on its own clock reading; the timing must still carry the
// in-flight instant from phase 1 and a settle instant at or after it.
func TestKickTimingCarriesStartAndSettle(t *testing.T) {
	clk := newClock()
	obs := &timingObserver{signal: make(chan struct{}, 4)}
	q := newQueue(t, clk, WithObserver(obs))
	l := &blockingListener{id: "h", binds: map[string]bool{"T": true}, proceed: make(chan struct{}), entered: make(chan struct{})}
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	clk.advance(2 * time.Second)
	startedAt := clk.t
	if n := q.Kick(); n != 1 {
		t.Fatalf("Kick launched %d", n)
	}
	<-l.entered
	// While the offer is outstanding the per-listener gauge reads 1.
	if got := q.InFlightByListener(); !reflect.DeepEqual(got, map[string]int{"h": 1}) {
		t.Fatalf("InFlightByListener mid-offer = %v, want h=1", got)
	}
	close(l.proceed)
	<-obs.signal
	got := obs.got()
	if len(got) != 1 || !got[0].StartedAt.Equal(startedAt) || got[0].SettledAt.Before(got[0].StartedAt) {
		t.Fatalf("kick timing = %+v, want StartedAt %v and Settled >= Started", got, startedAt)
	}
	if got[0].Wait() != 2*time.Second {
		t.Fatalf("kick wait = %v, want 2s", got[0].Wait())
	}
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })
	if got := q.InFlightByListener(); !reflect.DeepEqual(got, map[string]int{"h": 0}) {
		t.Fatalf("InFlightByListener after settle = %v, want h=0", got)
	}
}

func TestInFlightByListenerListsEveryRegisteredListener(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	q.Register(newListener("a", "A"))
	q.Register(newListener("b", "B"))
	if got := q.InFlightByListener(); !reflect.DeepEqual(got, map[string]int{"a": 0, "b": 0}) {
		t.Fatalf("idle InFlightByListener = %v, want a=0 b=0", got)
	}
}

func TestOldestPendingAgeByType(t *testing.T) {
	clk := newClock()
	q := newQueue(t, clk)
	l := newListener("h", "A")
	q.Register(l)
	if got := q.OldestPendingAgeByType(); len(got) != 0 {
		t.Fatalf("empty queue ages = %v", got)
	}
	mustEnqueue(t, q, evtUntil("a1", "A", clk.in(time.Hour)))
	clk.advance(10 * time.Second)
	mustEnqueue(t, q, evtUntil("a2", "A", clk.in(time.Hour)))
	clk.advance(5 * time.Second)
	// Unbound type: retained but owed to nobody, so it is NOT a pending wait.
	mustEnqueue(t, q, evtUntil("u1", "U", clk.in(time.Hour)))
	got := q.OldestPendingAgeByType()
	if want := map[string]time.Duration{"A": 15 * time.Second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ages = %v, want %v (oldest a1 = 15s; unbound U absent)", got, want)
	}
	q.Dispatch() // accepts a1
	clk.advance(time.Second)
	if got, want := q.OldestPendingAgeByType(), (map[string]time.Duration{"A": 6 * time.Second}); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a1 settled, ages = %v, want %v (oldest owed is now a2)", got, want)
	}
	q.Dispatch() // accepts a2
	if got := q.OldestPendingAgeByType(); len(got) != 0 {
		t.Fatalf("everything settled, ages = %v, want none", got)
	}
}

func TestAcceptAndEvictRecordsCarryTimestamps(t *testing.T) {
	clk := newClock()
	store := NewMemStore()
	q, err := New(store, WithClock(clk.now), WithEarlyEviction())
	if err != nil {
		t.Fatal(err)
	}
	l := newRunListener(clk, "h", 4*time.Second, "T")
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	clk.advance(time.Second)
	started := clk.t
	q.Dispatch() // accept + early evict

	recs, _ := store.Replay()
	var acc, ev *Record
	for i := range recs {
		switch recs[i].Op {
		case opAccept:
			acc = &recs[i]
		case opEvict:
			ev = &recs[i]
		}
	}
	if acc == nil || ev == nil {
		t.Fatalf("records = %+v, want an accept and an evict", recs)
	}
	if !acc.StartedAt.Equal(started) || !acc.At.Equal(started.Add(4*time.Second)) {
		t.Fatalf("accept record At/StartedAt = %v/%v, want %v/%v", acc.At, acc.StartedAt, started.Add(4*time.Second), started)
	}
	if ev.Reason != EvictReasonAllAccepted || ev.At.IsZero() {
		t.Fatalf("evict record = %+v, want reason %q and an At", ev, EvictReasonAllAccepted)
	}
}

func TestEvictReasonsRetiredAndReemit(t *testing.T) {
	clk := newClock()
	store := NewMemStore()
	q, err := New(store, WithClock(clk.now))
	if err != nil {
		t.Fatal(err)
	}
	// Retired by the sweep: no listener binds U, so it is dropped at expiry.
	mustEnqueue(t, q, evtUntil("gone", "U", clk.in(time.Minute)))
	clk.advance(2 * time.Minute)
	q.Expire()
	// Re-emit of a stale (retention over, sweep not yet run) id.
	mustEnqueue(t, q, evtUntil("again", "U", clk.in(time.Minute)))
	clk.advance(2 * time.Minute)
	mustEnqueue(t, q, evtUntil("again", "U", clk.in(time.Minute)))

	recs, _ := store.Replay()
	reasons := map[string]string{}
	for _, r := range recs {
		if r.Op == opEvict {
			reasons[r.EventID] = r.Reason
			if r.At.IsZero() {
				t.Fatalf("evict of %s has no At", r.EventID)
			}
		}
	}
	if reasons["gone"] != EvictReasonRetired {
		t.Fatalf("sweep evict reason = %q, want %q", reasons["gone"], EvictReasonRetired)
	}
	if reasons["again"] != EvictReasonReemit {
		t.Fatalf("re-emit evict reason = %q, want %q", reasons["again"], EvictReasonReemit)
	}
}

// --- compaction history ---------------------------------------------------

// historyLog builds n events e0..e(n-1), each enqueued, accepted by "h" (with
// stamps) and evicted, one minute apart.
func historyLog(n int) []Record {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	var out []Record
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		id := fmt.Sprintf("e%d", i)
		out = append(
			out,
			Record{Op: opEnqueue, EventID: id, Type: "pr.changed", At: at, ExpiresAt: at, EnqueuedAt: at},
			Record{Op: opAccept, EventID: id, ListenerID: "h", StartedAt: at.Add(10 * time.Second), At: at.Add(40 * time.Second)},
			Record{Op: opEvict, EventID: id, At: at.Add(41 * time.Second), Reason: EvictReasonRetired},
		)
	}
	return out
}

func archivesOf(recs []Record) []Record {
	var out []Record
	for _, r := range recs {
		if r.Op == opArchive {
			out = append(out, r)
		}
	}
	return out
}

func TestCompactionKeepsTimestampedHistoryOfEvictedEvents(t *testing.T) {
	log := historyLog(3)
	c := compactRecords(log)
	arch := archivesOf(c)
	if len(arch) != 3 {
		t.Fatalf("archive records = %d, want 3 (one per departed event): %+v", len(arch), c)
	}
	a := arch[1]
	base := time.Date(2026, 10, 1, 0, 1, 0, 0, time.UTC)
	if a.EventID != "e1" || a.Type != "pr.changed" || !a.At.Equal(base) || !a.EvictedAt.Equal(base.Add(41*time.Second)) || a.Reason != EvictReasonRetired {
		t.Fatalf("archive e1 = %+v", a)
	}
	want := []AcceptStamp{{ListenerID: "h", StartedAt: base.Add(10 * time.Second), At: base.Add(40 * time.Second)}}
	if !reflect.DeepEqual(a.Accepts, want) {
		t.Fatalf("archive e1 accepts = %+v, want %+v", a.Accepts, want)
	}
	// A replay ignores the history: same state as the uncompacted log, and as an empty queue.
	if stateOfRecords(t, c) != stateOfRecords(t, log) {
		t.Fatal("history changed replay state")
	}
}

func TestCompactionKeepsTimestampsOnLiveAccepts(t *testing.T) {
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	log := []Record{
		{Op: opEnqueue, EventID: "live", Type: "T", At: base, ExpiresAt: base.Add(time.Hour), EnqueuedAt: base},
		{Op: opAccept, EventID: "live", ListenerID: "h", StartedAt: base.Add(time.Second), At: base.Add(3 * time.Second)},
	}
	c := compactRecords(log)
	var acc *Record
	for i := range c {
		if c[i].Op == opAccept {
			acc = &c[i]
		}
	}
	if acc == nil || !acc.StartedAt.Equal(base.Add(time.Second)) || !acc.At.Equal(base.Add(3*time.Second)) {
		t.Fatalf("compacted live accept lost its timestamps: %+v", c)
	}
}

func TestCompactionHistoryIsBoundedNewestWinAndIdempotent(t *testing.T) {
	n := maxArchiveRecords + 500
	c := compactRecords(historyLog(n))
	arch := archivesOf(c)
	if len(arch) != maxArchiveRecords {
		t.Fatalf("archive = %d records, want the bound %d", len(arch), maxArchiveRecords)
	}
	if first, last := arch[0].EventID, arch[len(arch)-1].EventID; first != "e500" || last != fmt.Sprintf("e%d", n-1) {
		t.Fatalf("kept %s..%s, want the newest e500..e%d", first, last, n-1)
	}
	// Compacting a compacted log changes nothing, even though its archive is already full.
	if cc := compactRecords(c); !reflect.DeepEqual(cc, c) {
		t.Fatal("compaction with history is not idempotent")
	}
	// History carried in from an earlier compaction merges with new departures.
	more := append(append([]Record(nil), c...), historyLog(1)[0:3]...)
	for i := range more[len(more)-3:] {
		more[len(more)-3+i].EventID = "fresh"
	}
	merged := archivesOf(compactRecords(more))
	if len(merged) != maxArchiveRecords || merged[len(merged)-1].EventID != "fresh" {
		t.Fatalf("merged history = %d records ending %q, want %d ending fresh", len(merged), merged[len(merged)-1].EventID, maxArchiveRecords)
	}
}

func TestCompactionHistoryHonoursByteBudget(t *testing.T) {
	f := newLogFold()
	for _, r := range historyLog(100) {
		f.apply(r)
	}
	if all := len(archivesOf(f.records())); all != 100 {
		t.Fatalf("premise: %d archive records", all)
	}
	one := encodedLen(f.archive[0])
	f.archiveBudget = 10 * one
	kept := archivesOf(f.records())
	if len(kept) < 8 || len(kept) > 10 || kept[len(kept)-1].EventID != "e99" {
		t.Fatalf("kept %d records (last %q) under a ~10-record budget, want <=10 newest", len(kept), kept[len(kept)-1].EventID)
	}
}

// Through a real file: dispatch, evict, restart (startup compaction). The history
// must survive the restart's compaction, and the restarted queue must behave as if
// the history were absent.
func TestHistorySurvivesRestartCompaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	clk := newClock()
	q, fs := newFileQueue(t, path, WithClock(clk.now), WithEarlyEviction())
	l := newRunListener(clk, "h", 7*time.Second, "pr.changed")
	q.Register(l)
	for i := 0; i < 5; i++ {
		mustEnqueue(t, q, evtUntil(fmt.Sprintf("e%d", i), "pr.changed", clk.in(time.Hour)))
		clk.advance(2 * time.Second)
		q.Dispatch()
	}
	_ = fs.Close()

	q2, fs2 := newFileQueue(t, path, WithClock(clk.now), WithCompaction(0, true))
	defer func() { _ = fs2.Close() }()
	recs, err := fs2.Replay()
	if err != nil {
		t.Fatal(err)
	}
	arch := archivesOf(recs)
	if len(arch) != 5 {
		t.Fatalf("after the restart compaction: %d archive records, want 5 (history lost): %+v", len(arch), recs)
	}
	for _, a := range arch {
		if len(a.Accepts) != 1 || a.Accepts[0].ListenerID != "h" || a.Accepts[0].At.Sub(a.Accepts[0].StartedAt) != 7*time.Second || a.Reason != EvictReasonAllAccepted {
			t.Fatalf("archived timeline wrong: %+v", a)
		}
	}
	if got := q2.DepthByType(); len(got) != 0 {
		t.Fatalf("history leaked into queue state: depth %v", got)
	}
}

// Under tight log limits the history is budgeted down so it cannot hold the log
// above its soft threshold.
func TestHistoryBudgetFollowsTheSoftLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fs.Close() }()
	if _, err := New(fs, WithLogLimits(5000, 10000)); err != nil {
		t.Fatal(err)
	}
	if got := fs.archiveBudget.Load(); got != 500 {
		t.Fatalf("archive budget = %d, want soft/%d = 500", got, archiveBudgetDivisor)
	}
}
