package eventqueue

import (
	"testing"
	"time"
)

// restoreRecordingObserver is a recordingObserver that ALSO implements the
// optional RestoreObserver extension (bead pg2-0efop).
type restoreRecordingObserver struct {
	recordingObserver
	restored []string
}

func (o *restoreRecordingObserver) OnRestore(e Event) { o.restored = append(o.restored, e.ID) }

// replay reports each event that survived the durable log to a
// RestoreObserver exactly once, in FIFO order, and never reports an evicted
// one. It must NOT route through OnEnqueue (a restore is not a fresh enqueue).
func TestReplayReportsRetainedEventsToRestoreObserverOnly(t *testing.T) {
	mem := NewMemStore()
	far := time.Now().Add(time.Hour)

	q1, err := New(mem, WithEarlyEviction())
	if err != nil {
		t.Fatal(err)
	}
	l1 := newListener("h", "T")
	q1.Register(l1)
	mustEnqueue(t, q1, evtUntil("gone", "T", far))
	q1.Dispatch() // accepts "gone"; early eviction durably evicts it
	mustEnqueue(t, q1, evtUntil("e1", "T", far))
	mustEnqueue(t, q1, evtUntil("e2", "U", far))

	obs := &restoreRecordingObserver{}
	q2, err := New(mem, WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"e1", "e2"}; !equal(obs.restored, want) {
		t.Fatalf("restored = %v, want %v (FIFO, evicted event excluded)", obs.restored, want)
	}
	if len(obs.enqueued) != 0 {
		t.Fatalf("OnEnqueue fired %v during replay, want none (a restore is not an enqueue)", obs.enqueued)
	}
	if got := q2.DepthByType(); got["T"] != 1 || got["U"] != 1 {
		t.Fatalf("depth = %v, want T:1 U:1", got)
	}
}

// An Observer that does not implement RestoreObserver is unaffected: replay
// must not call OnEnqueue (or anything) on it.
func TestReplayLeavesPlainObserverUntouched(t *testing.T) {
	mem := NewMemStore()
	q1, err := New(mem)
	if err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, q1, evtUntil("e1", "T", time.Now().Add(time.Hour)))

	obs := &recordingObserver{}
	if _, err := New(mem, WithObserver(obs)); err != nil {
		t.Fatal(err)
	}
	if len(obs.enqueued)+len(obs.accepted)+len(obs.duped) != 0 {
		t.Fatalf("plain observer saw replay signals: %+v", obs)
	}
}
