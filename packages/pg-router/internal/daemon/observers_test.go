package daemon

import (
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/activity"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
)

// recordingDispatchFailureObserver is a minimal eventqueue.Observer that only
// records OnDispatchFailure calls — enough to prove fanOutObserver.
// OnDispatchFailure (bead pg2-icm3u) reaches both fanned-out observers.
type recordingDispatchFailureObserver struct{ dispatchFailed []string }

func (*recordingDispatchFailureObserver) OnEnqueue(eventqueue.Event)        {}
func (*recordingDispatchFailureObserver) OnAccept(string, string)           {}
func (*recordingDispatchFailureObserver) OnUnconsumedExpired(string)        {}
func (*recordingDispatchFailureObserver) OnDeclined(string, string, string) {}
func (*recordingDispatchFailureObserver) OnDeduped(string)                  {}
func (r *recordingDispatchFailureObserver) OnDispatchFailure(t, lid string) {
	r.dispatchFailed = append(r.dispatchFailed, t+"/"+lid)
}

// fanOutObserver.OnDispatchFailure must call BOTH fanned-out observers, in
// order — exactly like its siblings OnEnqueue/OnAccept/OnUnconsumedExpired/
// OnDeclined already do (bootCore's one construction site relies on this to
// feed the metrics.Emitter and the activity.Ring from the same queue hook).
func TestFanOutObserver_OnDispatchFailureCallsBoth(t *testing.T) {
	a := &recordingDispatchFailureObserver{}
	b := &recordingDispatchFailureObserver{}
	f := fanOutObserver{a, b}

	f.OnDispatchFailure("review-requested", "worker-a")

	if !reflect.DeepEqual(a.dispatchFailed, []string{"review-requested/worker-a"}) {
		t.Fatalf("a.dispatchFailed = %v, want [review-requested/worker-a]", a.dispatchFailed)
	}
	if !reflect.DeepEqual(b.dispatchFailed, []string{"review-requested/worker-a"}) {
		t.Fatalf("b.dispatchFailed = %v, want [review-requested/worker-a]", b.dispatchFailed)
	}
}

// TestListenerCountObserver_OnAccept_RecordsLastDeliveredAt proves
// listenerCountObserver.OnAccept (Task 2) records the delivery timestamp
// via its own injectable clock seam (now), rather than time.Now directly,
// so the assertion below is deterministic.
func TestListenerCountObserver_OnAccept_RecordsLastDeliveredAt(t *testing.T) {
	counts := map[string]*core.ListenerCounts{"df-feedback": {}}
	fixedNow := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	obs := &listenerCountObserver{counts: counts, now: func() time.Time { return fixedNow }}

	obs.OnAccept("evt-1", "df-feedback")

	got := counts["df-feedback"].LastDeliveredAtNanos.Load()
	if want := fixedNow.UnixNano(); got != want {
		t.Fatalf("LastDeliveredAtNanos = %d, want %d", got, want)
	}
}

// recordingHandlerFailureObserver is a minimal
// orchestrator.HandlerFailureObserver test double recording every
// OnHandlerFailure call -- cmd/pg-router's own package-local copy (it
// cannot import package orchestrator's identically-named test-only type
// declared in an internal/orchestrator _test.go file across packages).
type recordingHandlerFailureObserver struct {
	eventID, evtType, listenerID string
	calls                        int
}

func (r *recordingHandlerFailureObserver) OnHandlerFailure(eventID, evtType, listenerID string, err error) {
	r.eventID, r.evtType, r.listenerID = eventID, evtType, listenerID
	r.calls++
}

// TestHandlerFailureCountObserver_OnHandlerFailure_BumpsNamedListener is
// this task's required RED test: handlerFailureCountObserver must bump the
// named listener's HandlerFailures tally, the same lookup-by-listenerID
// pattern listenerCountObserver.OnAccept/OnDeclined already use.
func TestHandlerFailureCountObserver_OnHandlerFailure_BumpsNamedListener(t *testing.T) {
	counts := map[string]*core.ListenerCounts{"df-feedback": {}}
	obs := &handlerFailureCountObserver{counts: counts}

	obs.OnHandlerFailure("evt-1", "x", "df-feedback", nil)

	if got := counts["df-feedback"].HandlerFailures.Load(); got != 1 {
		t.Fatalf("HandlerFailures = %d, want 1", got)
	}
}

// TestFanOutHandlerFailureObserver_CallsEveryObserver is this task's
// required RED test: fanOutHandlerFailureObserver must call every fanned-out
// orchestrator.HandlerFailureObserver, in order -- bootCore's own
// construction site relies on this to feed both the metrics.Emitter and the
// new per-role handlerFailureCountObserver from the same
// o.HandlerFailureObserver hook.
func TestFanOutHandlerFailureObserver_CallsEveryObserver(t *testing.T) {
	a, b := &recordingHandlerFailureObserver{}, &recordingHandlerFailureObserver{}
	f := fanOutHandlerFailureObserver{a, b}

	f.OnHandlerFailure("evt-1", "x", "df-feedback", nil)

	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls = (%d,%d), want (1,1)", a.calls, b.calls)
	}
}

// restartAcceptListener accepts every event of one type; minimal
// eventqueue.Listener for the restart test below.
type restartAcceptListener struct{ typ string }

func (restartAcceptListener) ID() string { return "restart-accept" }
func (l restartAcceptListener) Matches(e eventqueue.Event) bool {
	return e.Type == l.typ
}

func (restartAcceptListener) Offer(eventqueue.Offering) eventqueue.OfferResult {
	return eventqueue.OfferResult{Accepted: true}
}

// TestActivityObserver_RestoredEventsSeededWithoutActivityRows (bead
// pg2-0efop): replaying the durable queue after a restart, through the
// production fanOutObserver, seeds activityObserver's eventID->Type
// correlation (so a later accept renders the real type) but appends NO ring
// rows for the restore itself and does not re-fire OnEnqueue.
func TestActivityObserver_RestoredEventsSeededWithoutActivityRows(t *testing.T) {
	mem := eventqueue.NewMemStore()
	q1, err := eventqueue.New(mem)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q1.Enqueue(eventqueue.Event{ID: "evt-r1", Type: "pr.changed", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	ring := activity.New(8)
	activityObs := NewActivityObserver(ring)
	q2, err := eventqueue.New(mem, eventqueue.WithObserver(fanOutObserver{activityObs, &recordingDispatchFailureObserver{}}))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]activity.Entry, 8)
	if n, _ := ring.Read(0, buf); n != 0 {
		t.Fatalf("ring rows after replay = %d (%+v), want 0 (restore must not emit activity)", n, buf[:n])
	}

	q2.Register(restartAcceptListener{typ: "pr.changed"})
	q2.Dispatch()
	n, _ := ring.Read(0, buf)
	if n != 1 || buf[0].Type != "pr.changed" || buf[0].Outcome != "delivered" {
		t.Fatalf("ring after accept = %+v, want exactly one {Type: pr.changed, Outcome: delivered}", buf[:n])
	}
}

// timingRecorder is an eventqueue.Observer that also implements TimingObserver.
type timingRecorder struct {
	recordingDispatchFailureObserver
	timings []eventqueue.DispatchTiming
}

func (r *timingRecorder) OnAcceptTiming(t eventqueue.DispatchTiming) {
	r.timings = append(r.timings, t)
}

func TestFanOutObserver_OnAcceptTimingForwardsToImplementingArmsOnly(t *testing.T) {
	a := &timingRecorder{}
	b := &recordingDispatchFailureObserver{} // no TimingObserver: must be skipped, not panic
	f := fanOutObserver{a, b}
	tm := eventqueue.DispatchTiming{EventID: "e1", EventType: "pr.changed", ListenerID: "desk-pr"}
	f.OnAcceptTiming(tm)
	if len(a.timings) != 1 || a.timings[0] != tm {
		t.Fatalf("a.timings = %+v, want the one timing", a.timings)
	}
	// And through a nested fan-out, as bootCore builds it.
	c := &timingRecorder{}
	nested := fanOutObserver{b, fanOutObserver{a, c}}
	nested.OnAcceptTiming(tm)
	if len(a.timings) != 2 || len(c.timings) != 1 {
		t.Fatalf("nested forwarding: a=%d c=%d, want 2 and 1", len(a.timings), len(c.timings))
	}
}
