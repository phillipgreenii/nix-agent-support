package emit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

const validEvent = `{"schemaVersion":"1","id":"op-1","type":"review-requested","ttl":"15m","payload":{"pr":42}}`

// validEventWithAt carries an optional RFC3339 `at` source-stamp.
const validEventWithAt = `{"schemaVersion":"1","id":"op-at","type":"review-requested","ttl":"15m","at":"2026-07-16T12:00:00Z","payload":{"pr":42}}`

// acceptListener accepts everything it is offered (for the integration path).
type acceptListener struct{ got []string }

func (a *acceptListener) ID() string                    { return "h" }
func (a *acceptListener) Matches(eventqueue.Event) bool { return true }
func (a *acceptListener) Offer(e eventqueue.Event) bool { a.got = append(a.got, e.ID); return true }

func newQueue(t *testing.T) *eventqueue.Queue {
	t.Helper()
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func injected() Locator { return Locator{InjectedSocket: "/tmp/core.sock", InjectedToken: "tok"} }

// --- unit: locating the core ---------------------------------------------

func TestLocate_InjectedWins(t *testing.T) {
	loc := Locator{InjectedSocket: "/s", InjectedToken: "t", Discover: func() (CoreRef, error) {
		t.Fatal("discovery must not run when a socket is injected")
		return CoreRef{}, nil
	}}
	c, err := loc.Locate()
	if err != nil || c.Socket != "/s" || c.Discovered {
		t.Fatalf("injected locate = %+v, %v", c, err)
	}
}

func TestLocate_Discovery(t *testing.T) {
	loc := Locator{Discover: func() (CoreRef, error) { return CoreRef{Socket: "/disc", Discovered: true}, nil }}
	c, err := loc.Locate()
	if err != nil || c.Socket != "/disc" || !c.Discovered {
		t.Fatalf("discovered locate = %+v, %v", c, err)
	}
}

func TestLocate_NoCore(t *testing.T) {
	if _, err := (Locator{}).Locate(); !errors.Is(err, ErrNoCore) {
		t.Fatalf("expected ErrNoCore, got %v", err)
	}
}

// --- unit: parsing / validation ------------------------------------------

func TestEmit_RejectsMalformedJSON(t *testing.T) {
	if _, err := Emit([]byte(`{not json`), injected(), QueueEnqueuer{Q: newQueue(t)}); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}

func TestEmit_RejectsNonSchemaValid(t *testing.T) {
	// Missing the required ttl -> rejected by the push-inject schema.
	if _, err := Emit([]byte(`{"schemaVersion":"1","id":"x","type":"t"}`), injected(), QueueEnqueuer{Q: newQueue(t)}); err == nil {
		t.Fatal("event missing ttl was accepted")
	}
}

// Fix 4: schema validation happens BEFORE the core is located. Malformed input
// with an EMPTY locator (which would yield ErrNoCore if locate ran) must return
// the SCHEMA rejection, not ErrNoCore — pinning the validate-then-locate order.
func TestEmit_ValidatesBeforeLocating(t *testing.T) {
	_, err := Emit([]byte(`{not json`), Locator{}, QueueEnqueuer{Q: newQueue(t)})
	if err == nil {
		t.Fatal("malformed input was accepted")
	}
	if errors.Is(err, ErrNoCore) {
		t.Fatalf("locate ran before validation (got ErrNoCore): %v", err)
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("want a schema rejection error, got: %v", err)
	}
}

// Fix 4: a present-but-unparseable `at` (a valid string per the schema) is a
// malformed event, rejected during parse — before the core is located.
func TestEmit_RejectsMalformedAt(t *testing.T) {
	bad := `{"schemaVersion":"1","id":"x","type":"t","ttl":"15m","at":"not-a-time"}`
	if _, err := Emit([]byte(bad), injected(), QueueEnqueuer{Q: newQueue(t)}); err == nil {
		t.Fatal("event with a malformed at was accepted")
	}
}

// --- integration: emit enters the durable queue --------------------------

func TestEmit_EntersQueueAndDelivers(t *testing.T) {
	q := newQueue(t)
	l := &acceptListener{}
	q.Register(l)
	res, err := Emit([]byte(validEvent), injected(), QueueEnqueuer{Q: q})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if res.Status != eventqueue.Enqueued {
		t.Fatalf("status = %v, want Enqueued", res.Status)
	}
	q.Dispatch()
	if len(l.got) != 1 || l.got[0] != "op-1" {
		t.Fatalf("emitted event not delivered: %v", l.got)
	}
}

// captureEnqueuer records the Event handed to the core-side enqueue so a test
// can assert exactly what entered the durable queue.
type captureEnqueuer struct{ got eventqueue.Event }

func (c *captureEnqueuer) Enqueue(_ CoreRef, evt eventqueue.Event) (eventqueue.EnqueueResult, error) {
	c.got = evt
	return eventqueue.Enqueued, nil
}

// Fix 4: the optional `at` source-stamp survives parsing into the enqueued
// Event (it was previously dropped by wireEvent/parseEvent).
func TestEmit_CarriesAtIntoEnqueuedEvent(t *testing.T) {
	cap := &captureEnqueuer{}
	if _, err := Emit([]byte(validEventWithAt), injected(), cap); err != nil {
		t.Fatalf("emit: %v", err)
	}
	want, err := time.Parse(time.RFC3339, "2026-07-16T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !cap.got.At.Equal(want) {
		t.Fatalf("enqueued event At = %v, want %v (at dropped)", cap.got.At, want)
	}
	if cap.got.ID != "op-at" {
		t.Fatalf("enqueued event ID = %q, want op-at", cap.got.ID)
	}
}

// An event with no `at` enqueues with the zero time (at is optional).
func TestEmit_AbsentAtIsZeroTime(t *testing.T) {
	cap := &captureEnqueuer{}
	if _, err := Emit([]byte(validEvent), injected(), cap); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !cap.got.At.IsZero() {
		t.Fatalf("absent at should be zero time, got %v", cap.got.At)
	}
}

func TestEmit_DedupesReEmitWithinTTL(t *testing.T) {
	q := newQueue(t)
	if _, err := Emit([]byte(validEvent), injected(), QueueEnqueuer{Q: q}); err != nil {
		t.Fatal(err)
	}
	res, err := Emit([]byte(validEvent), injected(), QueueEnqueuer{Q: q})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != eventqueue.Deduped {
		t.Fatalf("re-emit status = %v, want Deduped", res.Status)
	}
}

func TestEmit_NoCoreLocated(t *testing.T) {
	// Valid event but no core to enqueue against.
	if _, err := Emit([]byte(validEvent), Locator{}, QueueEnqueuer{Q: newQueue(t)}); !errors.Is(err, ErrNoCore) {
		t.Fatalf("expected ErrNoCore, got %v", err)
	}
}

// enqueueErrEnqueuer surfaces an enqueue error path.
type enqueueErrEnqueuer struct{}

func (enqueueErrEnqueuer) Enqueue(CoreRef, eventqueue.Event) (eventqueue.EnqueueResult, error) {
	return eventqueue.Enqueued, errors.New("boom")
}

func TestEmit_EnqueueErrorSurfaced(t *testing.T) {
	if _, err := Emit([]byte(validEvent), injected(), enqueueErrEnqueuer{}); err == nil {
		t.Fatal("enqueue error was swallowed")
	}
}
