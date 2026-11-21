package settle

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// These tests pin the retained accepted-event id rules of a cursor-feed lane
// (INV-EVT-3 lane retention): what persists across a restart, when an id is
// released, what abandonment releases, and where abandonment is evaluated. The
// matching decision record is DEC-ACK-5.

// pinListener accepts every offer unless decline is set, and records each offer.
type pinListener struct {
	id      string
	decline bool
	offered []string
}

func (l *pinListener) ID() string                      { return l.id }
func (l *pinListener) Matches(e eventqueue.Event) bool { return e.Type == "x" }
func (l *pinListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	l.offered = append(l.offered, o.Event.ID)
	if l.decline {
		return eventqueue.OfferResult{Decline: eventqueue.DeclineBusy}
	}
	return eventqueue.OfferResult{Accepted: true}
}

type pinClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *pinClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *pinClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// pinCore is one "process" of the core: a file-backed queue and ledger wired the
// way the daemon wires them (accept hook and lane retention).
type pinCore struct {
	q        *eventqueue.Queue
	fs       *eventqueue.FileStore
	st       *Settler
	reported []string
	mu       sync.Mutex
}

func startPinCore(t *testing.T, dir string, clock *pinClock, limit time.Duration, ledgerFile string) *pinCore {
	t.Helper()
	c := &pinCore{}
	l, err := OpenLedger(ledgerFile)
	if err != nil {
		t.Fatal(err)
	}
	c.st = NewSettler(context.Background(), l, func(_ context.Context, _, _, token string) error {
		c.mu.Lock()
		c.reported = append(c.reported, token)
		c.mu.Unlock()
		return nil
	}, nil)
	c.st.ConfigureHold(func(string) time.Duration { return limit }, clock.now, nil)
	c.fs, err = eventqueue.NewFileStore(filepath.Join(dir, "queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	c.q, err = eventqueue.New(c.fs,
		eventqueue.WithClock(clock.now),
		eventqueue.WithAcceptHook(c.st.OnEventSettled),
		eventqueue.WithLaneRetention(c.st.Ledger().Held),
		eventqueue.WithCompaction(1<<30, true))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *pinCore) stop(t *testing.T) {
	t.Helper()
	c.st.Wait()
	if err := c.fs.Close(); err != nil {
		t.Fatal(err)
	}
}

func (c *pinCore) reports() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.reported...)
}

// (a) Kill and restart: a handler accepted event A, a second handler has not.
// After the restart the offer of A is absorbed by dedup (the accepting handler
// is not re-run) until the hold limit, and the frontier is unchanged.
func TestRestart_RetainedAcceptedIDStillDedupsUntilHoldLimit(t *testing.T) {
	dir := t.TempDir()
	ledgerFile := filepath.Join(dir, "ack-ledger.json")
	clock := &pinClock{t: t0}
	const limit = 24 * time.Hour

	c1 := startPinCore(t, dir, clock, limit, ledgerFile)
	a1, b1 := &pinListener{id: "a"}, &pinListener{id: "b", decline: true}
	c1.q.Register(a1)
	c1.q.Register(b1)
	if err := c1.st.Ledger().Poll("q", "L", []Item{{ID: "A", Token: "tA"}}, clock.now()); err != nil {
		t.Fatal(err)
	}
	evt := eventqueue.Event{ID: "A", Type: "x", AckLane: "L", AckToken: "tA", At: clock.now(), ExpiresAt: clock.now().Add(time.Hour)}
	if r, err := c1.q.Enqueue(evt); err != nil || r != eventqueue.Enqueued {
		t.Fatalf("enqueue = %v, %v", r, err)
	}
	c1.q.Dispatch()
	if len(a1.offered) != 1 || c1.q.AckSettled("A") {
		t.Fatalf("setup: a offered %v, settled=%v (b has not accepted)", a1.offered, c1.q.AckSettled("A"))
	}
	c1.stop(t) // the kill

	clock.advance(2 * time.Hour) // past the event's own expiry, inside the hold limit
	c2 := startPinCore(t, dir, clock, limit, ledgerFile)
	a2, b2 := &pinListener{id: "a"}, &pinListener{id: "b", decline: true}
	c2.q.Register(a2)
	c2.q.Register(b2)
	c2.st.Restart(c2.q.AckSettled)
	c2.st.Wait()

	if !c2.st.Ledger().Held("A") {
		t.Fatal("the retained id must survive the restart")
	}
	if got := c2.reports(); len(got) != 0 {
		t.Fatalf("frontier moved after restart: reported %v", got)
	}
	if _, ok := c2.st.Ledger().Frontier("q", "L"); ok {
		t.Fatal("frontier must stay absent: A is unsettled")
	}
	if n := c2.q.Expire(); n != 0 {
		t.Fatalf("Expire dropped %d: the held id must stay retained across restart", n)
	}
	reoffer := eventqueue.Event{ID: "A", Type: "x", AckLane: "L", AckToken: "tA", At: clock.now(), ExpiresAt: clock.now().Add(time.Hour)}
	if r, err := c2.q.Enqueue(reoffer); err != nil || r != eventqueue.Deduped {
		t.Fatalf("re-offer after restart = %v, %v; want Deduped", r, err)
	}
	c2.q.Dispatch()
	if len(a2.offered) != 0 {
		t.Fatalf("the handler that accepted A was re-run after restart: %v", a2.offered)
	}

	// Past the hold limit the poll's evaluation abandons A and releases its id.
	clock.advance(limit)
	c2.st.Evaluate("q")
	c2.st.Wait()
	if c2.st.Ledger().Held("A") {
		t.Fatal("hold limit must release the id")
	}
	if got := c2.reports(); len(got) != 1 || got[0] != "tA" {
		t.Fatalf("after abandonment the frontier reports tA once, got %v", got)
	}
	c2.q.Dispatch() // b's final attempt, after expiry, settles its pair by expiry
	if n := c2.q.Expire(); n != 1 {
		t.Fatalf("Expire dropped %d after release, want 1", n)
	}
	c2.stop(t)
}

// Control for the test above: with a ledger that did not persist (a restart that
// drops the retained ids), the id is no longer held and Expire drops it, which
// is exactly the failure the restart test exists to catch.
func TestRestart_DroppedLedgerReleasesTheID(t *testing.T) {
	dir := t.TempDir()
	clock := &pinClock{t: t0}
	c1 := startPinCore(t, dir, clock, 24*time.Hour, "") // in-memory ledger: nothing persisted
	c1.q.Register(&pinListener{id: "a"})
	c1.q.Register(&pinListener{id: "b", decline: true})
	_ = c1.st.Ledger().Poll("q", "L", []Item{{ID: "A", Token: "tA"}}, clock.now())
	_, _ = c1.q.Enqueue(eventqueue.Event{ID: "A", Type: "x", AckLane: "L", AckToken: "tA", At: clock.now(), ExpiresAt: clock.now().Add(time.Hour)})
	c1.q.Dispatch()
	c1.stop(t)
	clock.advance(2 * time.Hour)
	c2 := startPinCore(t, dir, clock, 24*time.Hour, "")
	if c2.st.Ledger().Held("A") {
		t.Fatal("an in-memory ledger cannot hold the id after restart")
	}
	c2.stop(t)
}

// (b) An id is released when its lane frontier passes it (it and every earlier
// event settled), or when the hold limit abandons it. Settling a later event
// alone releases nothing.
func TestRelease_FrontierPassOrHoldLimit(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "L", items("e1", "t1", "e2", "t2", "e3", "t3"), t0)
	_, _ = l.MarkSettled("e2")
	if !l.Held("e2") {
		t.Fatal("e2 is settled but e1 before it is not: still held")
	}
	_, _ = l.MarkSettled("e1")
	if l.Held("e1") || l.Held("e2") || !l.Held("e3") {
		t.Fatalf("frontier passed e1 and e2 only: held e1=%v e2=%v e3=%v", l.Held("e1"), l.Held("e2"), l.Held("e3"))
	}
	if _, err := l.Abandon("q", "L", time.Hour, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if l.Held("e3") {
		t.Fatal("hold limit must release the abandoned e3")
	}
}

// (c) Abandonment is per entry by age, not a prefix of the list: every unsettled
// entry older than the limit is settled, whatever its position; entries younger
// than the limit stay unsettled, and ids stay held while an unsettled entry sits
// at or before them.
func TestAbandon_ReleasesByAgeNotByPosition(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "L", items("old", "t0"), t0)
	// "young" is listed first by the newer poll; "old" keeps its first-seen of t0.
	_ = l.Poll("q", "L", items("young", "t1", "old", "t0"), t0.Add(50*time.Minute))
	gone, err := l.Abandon("q", "L", time.Hour, t0.Add(70*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0].ID != "old" {
		t.Fatalf("abandoned %+v, want only old", gone)
	}
	es := l.Entries("q", "L")
	if es[0].Settled || !es[1].Settled {
		t.Fatalf("young must stay unsettled, old settled: %+v", es)
	}
	if !l.Held("young") || !l.Held("old") {
		t.Fatal("old sits behind the unsettled young: both stay held")
	}
}

// (d) A newer poll that no longer lists an event drops it from the ledger, so
// its id is released even though it was never settled.
func TestNewerPollWithoutTheEventReleasesIt(t *testing.T) {
	l, _ := OpenLedger("")
	_ = l.Poll("q", "L", items("e1", "t1", "e2", "t2"), t0)
	if !l.Held("e1") {
		t.Fatal("setup: e1 is unsettled and held")
	}
	_ = l.Poll("q", "L", items("e2", "t2"), t0)
	if l.Held("e1") {
		t.Fatal("an id the newest poll no longer lists is released")
	}
	if es := l.Entries("q", "L"); len(es) != 1 || es[0].ID != "e2" {
		t.Fatalf("entries = %+v", es)
	}
}

// (e) Abandonment runs only inside frontier evaluation (Evaluate, called after a
// poll, on an accept and at restart); there is no timer, so a clock that moves
// without an evaluation abandons nothing.
func TestAbandonOnlyInsideEvaluation(t *testing.T) {
	f, st := newHold(t, time.Hour)
	_ = st.Ledger().Poll("q", "L", items("e1", "t1"), t0)
	f.advance(3 * time.Hour)
	time.Sleep(20 * time.Millisecond)
	st.Wait()
	if len(f.abandoned) != 0 || len(f.reported) != 0 || !st.Ledger().Held("e1") {
		t.Fatalf("abandoned without an evaluation: abandoned=%v reported=%v", f.abandoned, f.reported)
	}
	st.Evaluate("q")
	st.Wait()
	if len(f.abandoned) != 1 || st.Ledger().Held("e1") {
		t.Fatalf("evaluation must abandon: abandoned=%v held=%v", f.abandoned, st.Ledger().Held("e1"))
	}
}
