package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/eventlog"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

// Tests for a dispatch a handler ADOPTED from an earlier daemon's offer (DEC-EVENT-5,
// bead pg2-dm6de): the handler reports the dispatch's own start in its reply
// (wireclient.Reply.StartedAt), roleListener.Offer relays it as OfferResult.StartedAt
// and times the events.jsonl row from it, and the queue uses it for the accept record
// and for the queue-wait and run samples.

// timingRecorder is an eventqueue.Observer that keeps the timeline of every accepted
// dispatch, the way the metrics emitter sees it.
type timingRecorder struct {
	mu      sync.Mutex
	timings []eventqueue.DispatchTiming
}

func (*timingRecorder) OnEnqueue(eventqueue.Event)        {}
func (*timingRecorder) OnAccept(string, string)           {}
func (*timingRecorder) OnUnconsumedExpired(string)        {}
func (*timingRecorder) OnDeclined(string, string, string) {}
func (*timingRecorder) OnDeduped(string)                  {}
func (*timingRecorder) OnDispatchFailure(string, string)  {}
func (r *timingRecorder) OnAcceptTiming(t eventqueue.DispatchTiming) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timings = append(r.timings, t)
}

func (r *timingRecorder) got() []eventqueue.DispatchTiming {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]eventqueue.DispatchTiming(nil), r.timings...)
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// The whole path, through a real queue on a controlled clock: an event enqueued at
// 23:00, re-offered at 23:10 to a handler that reports the dispatch began at 23:04.
// The accept record, the wait and run samples and the events.jsonl row must all agree
// on 23:04, and the row's duration must span the six minutes before the re-offer.
func TestAdoptedDispatch_QueueAndEventLogRowShareTheReportedStart(t *testing.T) {
	buf := captureWarnings(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	enqueued := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	adopted := enqueued.Add(4 * time.Minute)
	reoffer := enqueued.Add(10 * time.Minute)

	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = sleepHandler{fakeHandler: &fakeHandler{reply: wireclient.Reply{ID: "dsp-x", Outcome: "delivered", StartedAt: adopted}}, d: 30 * time.Millisecond}
	o.Log = lw

	now := enqueued
	store := eventqueue.NewMemStore()
	obs := &timingRecorder{}
	q, err := eventqueue.New(store, eventqueue.WithClock(func() time.Time { return now }), eventqueue.WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	q.Register(o.NewListener(context.Background(), workerRole(o)))
	if _, err := q.Enqueue(eventqueue.Event{ID: "survivor-1", Type: "work.ready", ExpiresAt: enqueued.Add(time.Hour), Payload: map[string]any{"id": "zr-survivor"}}); err != nil {
		t.Fatal(err)
	}
	now = reoffer
	if n := q.Dispatch(); n != 1 {
		t.Fatalf("Dispatch accepted %d, want 1", n)
	}

	// The queue-wait and run samples.
	timings := obs.got()
	if len(timings) != 1 {
		t.Fatalf("timings = %+v, want exactly one", timings)
	}
	if tm := timings[0]; !tm.StartedAt.Equal(adopted) || tm.Wait() != 4*time.Minute || tm.Run() != 6*time.Minute {
		t.Fatalf("timing = %+v (wait %v, run %v), want StartedAt %v, wait 4m and run 6m", tm, tm.Wait(), tm.Run(), adopted)
	}
	// The durable accept record.
	recs, err := store.Replay()
	if err != nil {
		t.Fatal(err)
	}
	var accepts int
	for _, r := range recs {
		if string(r.Op) == "accept" {
			accepts++
			if !r.StartedAt.Equal(adopted) || !r.At.Equal(reoffer) {
				t.Fatalf("accept record StartedAt/At = %v/%v, want %v/%v", r.StartedAt, r.At, adopted, reoffer)
			}
		}
	}
	if accepts != 1 {
		t.Fatalf("accept records = %d in %+v, want 1", accepts, recs)
	}
	// The events.jsonl row.
	rows := dispatchRows(t, logPath)
	if len(rows) != 1 {
		t.Fatalf("dispatch rows = %v, want exactly one", rows)
	}
	r := rows[0]
	if r["started_at"] != "2026-10-12T23:04:00Z" || r["enqueued_at"] != "2026-10-12T23:00:00Z" {
		t.Fatalf("started_at/enqueued_at = %v/%v, want the reported start 23:04 and the enqueue 23:00", r["started_at"], r["enqueued_at"])
	}
	// 6 min from the reported start to the re-offer, plus the offer's own run (at least the handler's 30ms).
	sixMin := float64((6 * time.Minute).Milliseconds())
	if dur, ok := r["duration_ms"].(float64); !ok || dur < sixMin+30 || dur > sixMin+5000 {
		t.Fatalf("duration_ms = %v, want %v + the offer's own run (30ms..5s)", r["duration_ms"], sixMin)
	}
	if buf.Len() != 0 {
		t.Fatalf("an in-range start logged a warning: %q", buf.String())
	}
}

// offerOutcome is what one direct roleListener.Offer left behind.
type offerOutcome struct {
	result eventqueue.OfferResult
	row    map[string]any
	log    string
}

// offerWith runs one Offer of an event enqueued at enqueued, offered at offerStart, to
// a handler that returns (reply, err), and reads back its events.jsonl row.
func offerWith(t *testing.T, reply wireclient.Reply, handlerErr error, enqueued, offerStart time.Time) offerOutcome {
	t.Helper()
	buf := captureWarnings(t)
	logPath := filepath.Join(t.TempDir(), "events.jsonl")
	lw, err := eventlog.New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	o := newOrch(fastCfg(t), testQuerySet(nil, nil))
	o.Handler = sleepHandler{fakeHandler: &fakeHandler{reply: reply, err: handlerErr}, d: 5 * time.Millisecond}
	o.Log = lw
	l := o.NewListener(context.Background(), workerRole(o))
	evt := eventqueue.Event{ID: "e1", Type: "work.ready", At: enqueued, Payload: map[string]any{"id": "zr-1"}}
	res := l.Offer(eventqueue.Offering{ID: "dsp-1", Event: evt, StartedAt: offerStart})
	rows := dispatchRows(t, logPath)
	if len(rows) != 1 {
		t.Fatalf("dispatch rows = %v, want exactly one", rows)
	}
	return offerOutcome{result: res, row: rows[0], log: buf.String()}
}

func TestAdoptedDispatch_OfferRelaysAnInRangeStart(t *testing.T) {
	enqueued := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	offerStart := enqueued.Add(10 * time.Minute)
	adopted := enqueued.Add(4 * time.Minute)
	out := offerWith(t, wireclient.Reply{Outcome: "delivered", StartedAt: adopted}, nil, enqueued, offerStart)
	if !out.result.Accepted || !out.result.StartedAt.Equal(adopted) {
		t.Fatalf("Offer = %+v, want accepted and carrying the reported start %v", out.result, adopted)
	}
	if out.row["started_at"] != "2026-10-12T23:04:00Z" {
		t.Fatalf("row started_at = %v, want the reported start", out.row["started_at"])
	}
	if dur, ok := out.row["duration_ms"].(float64); !ok || dur < float64((6*time.Minute).Milliseconds()) {
		t.Fatalf("duration_ms = %v, want at least the 6 min before the offer", out.row["duration_ms"])
	}
	if out.log != "" {
		t.Fatalf("an in-range start logged a warning: %q", out.log)
	}
}

// A reported start outside [the event's At, the offer's own start] is ignored with a
// warning: the offer relays nothing, and the row is timed from the offer, as it is for
// a reply with no field. One case per edge, plus the no-field control and a failed
// dispatch, whose reply is never trusted for a start.
func TestAdoptedDispatch_OfferIgnoresAnUnusableStart(t *testing.T) {
	enqueued := time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC)
	offerStart := enqueued.Add(10 * time.Minute)
	for _, tc := range []struct {
		name      string
		reply     wireclient.Reply
		err       error
		wantWarn  string // "" means no warning at all
		wantErrIn string
	}{
		{
			"before the event at",
			wireclient.Reply{Outcome: "delivered", StartedAt: enqueued.Add(-time.Second)},
			nil,
			"ignoring a start the handler reported", "before the event's enqueue instant",
		},
		{
			"after the offer's own start",
			wireclient.Reply{Outcome: "delivered", StartedAt: offerStart.Add(time.Second)},
			nil,
			"ignoring a start the handler reported", "after this offer's own start",
		},
		{"no start reported", wireclient.Reply{Outcome: "delivered"}, nil, "", ""},
		{"a failed dispatch", wireclient.Reply{StartedAt: enqueued.Add(time.Minute)}, fmt.Errorf("boom"), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := offerWith(t, tc.reply, tc.err, enqueued, offerStart)
			if !out.result.Accepted || !out.result.StartedAt.IsZero() {
				t.Fatalf("Offer = %+v, want accepted with no reported start", out.result)
			}
			if out.row["started_at"] != "2026-10-12T23:10:00Z" {
				t.Fatalf("row started_at = %v, want the offer's own start 23:10", out.row["started_at"])
			}
			if dur, ok := out.row["duration_ms"].(float64); !ok || dur > 5000 {
				t.Fatalf("duration_ms = %v, want only the offer's own run (a few ms), not time before the offer", out.row["duration_ms"])
			}
			if tc.wantWarn == "" {
				// (A failed dispatch logs its own "dispatch result" warning; only a
				// start warning would be wrong here.)
				if strings.Contains(out.log, "ignoring a start") {
					t.Fatalf("logged %q, want no start warning", out.log)
				}
				return
			}
			if !strings.Contains(out.log, tc.wantWarn) || !strings.Contains(out.log, tc.wantErrIn) || !strings.Contains(out.log, "event=e1") {
				t.Fatalf("warning = %q, want %q naming the event and %q", out.log, tc.wantWarn, tc.wantErrIn)
			}
		})
	}
}
