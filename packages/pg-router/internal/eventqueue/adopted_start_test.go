package eventqueue

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the start a handler reports for a dispatch it ADOPTED (DEC-EVENT-5,
// bead pg2-dm6de): OfferResult.StartedAt replaces the offer's own start in the
// durable accept record and in the DispatchTiming that feeds the queue-wait and
// run metrics, but only inside [the event's At, the offer's own start].

// adoptingListener is a listener whose handler adopted a dispatch: it accepts,
// "runs" for run on the shared clock and reports whatever start report derives
// from the Offering it was handed (the zero time reports nothing).
type adoptingListener struct {
	fakeListener
	clk    *mockClock
	run    time.Duration
	report func(o Offering) time.Time
}

func newAdoptingListener(clk *mockClock, run time.Duration, report func(o Offering) time.Time) *adoptingListener {
	return &adoptingListener{fakeListener: *newListener("h", "T"), clk: clk, run: run, report: report}
}

func (l *adoptingListener) Offer(o Offering) OfferResult {
	res := l.fakeListener.Offer(o)
	l.clk.advance(l.run)
	if l.report != nil {
		res.StartedAt = l.report(o)
	}
	return res
}

// adoptedRun is what one dispatch through an adoptingListener left behind.
type adoptedRun struct {
	enqueuedAt, offerStart time.Time
	timing                 DispatchTiming
	accept                 Record
}

// dispatchAdopted enqueues one event, lets the clock wait before offering it,
// dispatches it to a listener that reports report(offering) and returns the
// timing the observer saw and the accept record the store holds. report receives
// the event's enqueue instant too, so a test can place the start relative to it.
func dispatchAdopted(t *testing.T, wait, run time.Duration, report func(enqueuedAt time.Time, o Offering) time.Time) adoptedRun {
	t.Helper()
	clk := newClock()
	store := NewMemStore()
	obs := &timingObserver{}
	q, err := New(store, WithClock(clk.now), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	enqueuedAt := clk.t
	q.Register(newAdoptingListener(clk, run, func(o Offering) time.Time { return report(enqueuedAt, o) }))
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	clk.advance(wait)
	offerStart := clk.t
	if n := q.Dispatch(); n != 1 {
		t.Fatalf("Dispatch accepted %d, want 1", n)
	}

	timings := obs.got()
	if len(timings) != 1 {
		t.Fatalf("timings = %+v, want exactly one", timings)
	}
	recs, err := store.Replay()
	if err != nil {
		t.Fatal(err)
	}
	var accept *Record
	for i := range recs {
		if recs[i].Op == opAccept {
			accept = &recs[i]
		}
	}
	if accept == nil {
		t.Fatalf("no accept record in %+v", recs)
	}
	return adoptedRun{enqueuedAt: enqueuedAt, offerStart: offerStart, timing: timings[0], accept: *accept}
}

// captureWarnings routes the default logger to a buffer for the test's life.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A reported start that lies in range is the dispatch's start: the accept record
// carries it, the queue wait ends at it and the run begins at it. The wait and the
// run still sum to enqueue-to-settle, so nothing is double counted or lost.
func TestAdoptedStartDrivesTheAcceptRecordAndTheTiming(t *testing.T) {
	buf := captureWarnings(t)
	r := dispatchAdopted(t, 10*time.Minute, 5*time.Second, func(enqueuedAt time.Time, _ Offering) time.Time {
		return enqueuedAt.Add(4 * time.Minute) // the adopted session began 6 min before the re-offer
	})
	adopted := r.enqueuedAt.Add(4 * time.Minute)

	if !r.accept.StartedAt.Equal(adopted) {
		t.Fatalf("accept StartedAt = %v, want the reported start %v (the re-offer was %v)", r.accept.StartedAt, adopted, r.offerStart)
	}
	if want := r.offerStart.Add(5 * time.Second); !r.accept.At.Equal(want) {
		t.Fatalf("accept At = %v, want the settle instant %v", r.accept.At, want)
	}
	tm := r.timing
	if !tm.StartedAt.Equal(adopted) || !tm.EnqueuedAt.Equal(r.enqueuedAt) {
		t.Fatalf("timing = %+v, want StartedAt %v and EnqueuedAt %v", tm, adopted, r.enqueuedAt)
	}
	if tm.Wait() != 4*time.Minute || tm.Run() != 6*time.Minute+5*time.Second {
		t.Fatalf("wait/run = %v/%v, want 4m/6m5s: the wait ends at the reported start and the run begins there", tm.Wait(), tm.Run())
	}
	if tm.Wait()+tm.Run() != tm.SettledAt.Sub(tm.EnqueuedAt) {
		t.Fatalf("wait %v + run %v != enqueue-to-settle %v", tm.Wait(), tm.Run(), tm.SettledAt.Sub(tm.EnqueuedAt))
	}
	if buf.Len() != 0 {
		t.Fatalf("an in-range start logged a warning: %q", buf.String())
	}
}

// Both ends of the range are inclusive: a start at the event's enqueue instant (a
// dispatch that began the moment its event arrived) and one at the offer's own
// start (an "adoption" with nothing to correct) are both honoured.
func TestAdoptedStartRangeIncludesBothEnds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(enqueuedAt time.Time, o Offering) time.Time
		want  func(r adoptedRun) time.Time
	}{
		{
			"at the event's enqueue instant",
			func(enqueuedAt time.Time, _ Offering) time.Time { return enqueuedAt },
			func(r adoptedRun) time.Time { return r.enqueuedAt },
		},
		{
			"at the offer's own start",
			func(_ time.Time, o Offering) time.Time { return o.StartedAt },
			func(r adoptedRun) time.Time { return r.offerStart },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureWarnings(t)
			r := dispatchAdopted(t, time.Minute, time.Second, tc.start)
			if want := tc.want(r); !r.accept.StartedAt.Equal(want) || !r.timing.StartedAt.Equal(want) {
				t.Fatalf("accept/timing StartedAt = %v/%v, want %v", r.accept.StartedAt, r.timing.StartedAt, want)
			}
			if buf.Len() != 0 {
				t.Fatalf("a boundary start logged a warning: %q", buf.String())
			}
		})
	}
}

// A start before the event existed is impossible, so it is ignored (not clamped to
// the enqueue instant: a value that wrong says nothing about the true one) with a
// warning, and the dispatch is timed from the offer exactly as before.
func TestAdoptedStartBeforeTheEventIsIgnored(t *testing.T) {
	buf := captureWarnings(t)
	r := dispatchAdopted(t, 10*time.Minute, 5*time.Second, func(enqueuedAt time.Time, _ Offering) time.Time {
		return enqueuedAt.Add(-time.Second)
	})
	assertTimedFromTheOffer(t, r)
	if log := buf.String(); !strings.Contains(log, "ignoring a start a listener reported") || !strings.Contains(log, "before the event's enqueue instant") || !strings.Contains(log, "eventId=e1") {
		t.Fatalf("warning = %q, want one naming the event and that the start precedes its enqueue", log)
	}
}

// An adopted dispatch began BEFORE this offer, so a start after the offer's own is a
// handler's mistake: ignored with a warning, timed from the offer.
func TestAdoptedStartAfterTheOfferIsIgnored(t *testing.T) {
	buf := captureWarnings(t)
	r := dispatchAdopted(t, 10*time.Minute, 5*time.Second, func(_ time.Time, o Offering) time.Time {
		return o.StartedAt.Add(time.Second)
	})
	assertTimedFromTheOffer(t, r)
	if log := buf.String(); !strings.Contains(log, "ignoring a start a listener reported") || !strings.Contains(log, "after this offer's own start") || !strings.Contains(log, "eventId=e1") {
		t.Fatalf("warning = %q, want one naming the event and that the start follows the offer", log)
	}
}

// No reported start (the zero OfferResult.StartedAt every listener had before this
// field existed) is today's behavior, with nothing logged.
func TestNoReportedStartTimesTheDispatchFromTheOffer(t *testing.T) {
	buf := captureWarnings(t)
	r := dispatchAdopted(t, 10*time.Minute, 5*time.Second, func(time.Time, Offering) time.Time { return time.Time{} })
	assertTimedFromTheOffer(t, r)
	if buf.Len() != 0 {
		t.Fatalf("an absent start logged a warning: %q", buf.String())
	}
}

func assertTimedFromTheOffer(t *testing.T, r adoptedRun) {
	t.Helper()
	if !r.accept.StartedAt.Equal(r.offerStart) || !r.timing.StartedAt.Equal(r.offerStart) {
		t.Fatalf("accept/timing StartedAt = %v/%v, want the offer's own start %v", r.accept.StartedAt, r.timing.StartedAt, r.offerStart)
	}
	if r.timing.Wait() != 10*time.Minute || r.timing.Run() != 5*time.Second {
		t.Fatalf("wait/run = %v/%v, want 10m/5s", r.timing.Wait(), r.timing.Run())
	}
}

// Kick settles each offer on its own, after its own clock reading; the reported start
// must reach the timing and the accept record there too.
func TestAdoptedStartThroughKick(t *testing.T) {
	clk := newClock()
	store := NewMemStore()
	obs := &timingObserver{signal: make(chan struct{}, 4)}
	q, err := New(store, WithClock(clk.now), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	enqueuedAt := clk.t
	adopted := enqueuedAt.Add(time.Minute)
	l := newAdoptingListener(clk, 3*time.Second, func(Offering) time.Time { return adopted })
	q.Register(l)
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	clk.advance(5 * time.Minute)
	if n := q.Kick(); n != 1 {
		t.Fatalf("Kick launched %d, want 1", n)
	}
	<-obs.signal
	got := obs.got()
	if len(got) != 1 || !got[0].StartedAt.Equal(adopted) || got[0].Wait() != time.Minute {
		t.Fatalf("kick timing = %+v, want StartedAt %v and a 1m wait", got, adopted)
	}
	waitForCond(t, 5*time.Second, func() bool { return q.SessionsInFlight() == 0 })
	recs, err := store.Replay()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Op == opAccept && !r.StartedAt.Equal(adopted) {
			t.Fatalf("kick accept StartedAt = %v, want %v", r.StartedAt, adopted)
		}
	}
}

// The accept record is durable, so the adopted start must survive a restart's
// compaction: it is what the next process replays, and what the history keeps.
func TestCompactionKeepsAnAdoptedAcceptStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.jsonl")
	clk := newClock()
	q, fs := newFileQueue(t, path, WithClock(clk.now))
	enqueuedAt := clk.t
	adopted := enqueuedAt.Add(time.Minute)
	q.Register(newAdoptingListener(clk, 4*time.Second, func(Offering) time.Time { return adopted }))
	mustEnqueue(t, q, evtUntil("e1", "T", clk.in(time.Hour)))
	clk.advance(10 * time.Minute)
	if n := q.Dispatch(); n != 1 {
		t.Fatalf("Dispatch accepted %d, want 1", n)
	}
	_ = fs.Close()

	// The restart compacts the log at startup (WithCompaction(0, true)).
	_, fs2 := newFileQueue(t, path, WithClock(clk.now), WithCompaction(0, true))
	defer func() { _ = fs2.Close() }()
	recs, err := fs2.Replay()
	if err != nil {
		t.Fatal(err)
	}
	var accepts []Record
	for _, r := range recs {
		if r.Op == opAccept {
			accepts = append(accepts, r)
		}
	}
	if len(accepts) != 1 || accepts[0].ListenerID != "h" || !accepts[0].StartedAt.Equal(adopted) {
		t.Fatalf("after the compacting restart the accept records = %+v, want one for h starting at the adopted %v", accepts, adopted)
	}
	// And the compaction of an archived accept keeps it as well.
	for _, a := range compactRecords([]Record{
		{Op: opEnqueue, EventID: "gone", Type: "T", At: enqueuedAt, ExpiresAt: enqueuedAt, EnqueuedAt: enqueuedAt},
		{Op: opAccept, EventID: "gone", ListenerID: "h", StartedAt: adopted, At: adopted.Add(time.Hour)},
		{Op: opEvict, EventID: "gone", At: adopted.Add(time.Hour), Reason: EvictReasonRetired},
	}) {
		if a.Op == opArchive && (len(a.Accepts) != 1 || !a.Accepts[0].StartedAt.Equal(adopted)) {
			t.Fatalf("archived accept stamps = %+v, want StartedAt %v", a.Accepts, adopted)
		}
	}
}

func TestCheckAdoptedStart(t *testing.T) {
	at := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	offer := at.Add(time.Hour)
	for _, tc := range []struct {
		name                     string
		start, enqueued, offerAt time.Time
		wantErrContains          string
	}{
		{"inside", at.Add(time.Minute), at, offer, ""},
		{"at the enqueue instant", at, at, offer, ""},
		{"at the offer's start", offer, at, offer, ""},
		{"before the enqueue instant", at.Add(-time.Nanosecond), at, offer, "before the event's enqueue instant"},
		{"after the offer's start", offer.Add(time.Nanosecond), at, offer, "after this offer's own start"},
		{"no enqueue instant leaves the low side open", at.Add(-24 * time.Hour), time.Time{}, offer, ""},
		{"no offer start leaves the high side open", offer.Add(24 * time.Hour), at, time.Time{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckAdoptedStart(tc.start, tc.enqueued, tc.offerAt)
			switch {
			case tc.wantErrContains == "" && err != nil:
				t.Fatalf("CheckAdoptedStart = %v, want nil", err)
			case tc.wantErrContains != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErrContains)):
				t.Fatalf("CheckAdoptedStart = %v, want an error containing %q", err, tc.wantErrContains)
			}
		})
	}
}
