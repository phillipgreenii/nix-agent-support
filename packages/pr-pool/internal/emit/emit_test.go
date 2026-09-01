package emit

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
)

// validEvent carries an explicit far-future `expiresAt`. That is deliberate:
// `expiresAt` is the retry AND de-duplication window (INV-EVT-3 / INV-EVT-4), so
// the tests below that need a re-emit to be ABSORBED must ask for a window — under
// the born-expired default there is barely one.
const validEvent = `{"schemaVersion":"1","id":"op-1","type":"review-requested","expiresAt":"2099-01-01T00:00:00Z","payload":{"pr":42}}`

// validEventWithAt carries an optional RFC3339 `at` source-stamp.
const validEventWithAt = `{"schemaVersion":"1","id":"op-at","type":"review-requested","at":"2026-07-16T12:00:00Z","expiresAt":"2099-01-01T00:00:00Z","payload":{"pr":42}}`

// acceptListener accepts everything it is offered (for the integration path).
type acceptListener struct{ got []string }

func (a *acceptListener) ID() string                    { return "h" }
func (a *acceptListener) Matches(eventqueue.Event) bool { return true }
func (a *acceptListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	a.got = append(a.got, o.Event.ID)
	return eventqueue.OfferResult{Accepted: true, Decline: eventqueue.DeclineNone}
}

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
	// Missing the required type -> rejected by the push-inject schema.
	if _, err := Emit([]byte(`{"schemaVersion":"1","id":"x"}`), injected(), QueueEnqueuer{Q: newQueue(t)}); err == nil {
		t.Fatal("event missing type was accepted")
	}
	// And the duration-valued field the absolute-instant shape replaced is now an
	// UNDECLARED property on a closed object, so an operator still typing the old
	// shape is told rather than silently served (DEC-EVENT-1).
	legacy := `{"schemaVersion":"1","id":"x","type":"t","ttl":"15m"}`
	if _, err := Emit([]byte(legacy), injected(), QueueEnqueuer{Q: newQueue(t)}); err == nil {
		t.Fatal("event carrying the legacy duration-valued ttl was accepted")
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

// Fix 4: a present-but-unparseable instant is a malformed event, rejected
// before the core is located. Since pg2-kgydy these particular values ("not-a-
// time", a duration-shaped "15m") are no longer valid strings per the schema
// either — event.schema.json's `pattern` on `at`/`expiresAt` now rejects the
// malformed SHAPE at the schema-validation step in Emit, before DecodeEvent
// ever runs; a value that is well-formed RFC3339 syntax but calendar-invalid
// (e.g. a month of 13) would instead reach DecodeEvent's time.Parse. Either
// way the caller sees a rejection before the core is located.
func TestEmit_RejectsMalformedInstants(t *testing.T) {
	cases := map[string]string{
		"bad at":                    `{"schemaVersion":"1","id":"x","type":"t","at":"not-a-time"}`,
		"bad expiresAt":             `{"schemaVersion":"1","id":"x","type":"t","expiresAt":"not-a-time"}`,
		"duration-shaped expiresAt": `{"schemaVersion":"1","id":"x","type":"t","expiresAt":"15m"}`,
	}
	for desc, bad := range cases {
		t.Run(desc, func(t *testing.T) {
			if _, err := Emit([]byte(bad), injected(), QueueEnqueuer{Q: newQueue(t)}); err == nil {
				t.Fatalf("event with a malformed instant was accepted")
			}
		})
	}
}

// A value that is well-formed RFC3339 SYNTAX per event.schema.json's `pattern`
// but calendar-invalid (a month of 13) clears the schema layer — a regex
// cannot enforce calendar semantics — and is instead caught by DecodeEvent's
// time.Parse. Proving the schema step passes on its own is what makes this
// case distinct from TestEmit_RejectsMalformedInstants above: both layers
// reject the event, but at different steps, and Emit as a whole still refuses
// it either way.
func TestEmit_CalendarInvalidInstantPassesSchemaPatternButFailsDecode(t *testing.T) {
	bad := []byte(`{"schemaVersion":"1","id":"x","type":"t","at":"2026-13-45T12:00:00Z"}`)
	if err := conformance.CheckBytes(pushInjectSchema, bad); err != nil {
		t.Fatalf("calendar-invalid-but-shape-valid instant unexpectedly failed schema validation: %v", err)
	}
	if _, err := Emit(bad, injected(), QueueEnqueuer{Q: newQueue(t)}); err == nil {
		t.Fatal("calendar-invalid instant was accepted by Emit")
	}
}

// --- integration: emit enters the durable queue --------------------------

func TestEmit_EntersQueueAndDelivers(t *testing.T) {
	q := newQueue(t)
	l := &acceptListener{}
	q.Register(l)
	// LocalLocator, not injected(): QueueEnqueuer reaches only THIS process's queue,
	// so it refuses a ref naming a core elsewhere.
	res, err := Emit([]byte(validEvent), LocalLocator(), QueueEnqueuer{Q: q})
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

// An event with no `at` enqueues with the ZERO time. The front door deliberately
// does NOT default it: absent `at` means "the CORE's own now at ingest"
// (INV-EVT-1), and this CLI is not the core's clock — the queue resolves it.
func TestEmit_AbsentAtIsLeftUnresolved(t *testing.T) {
	cap := &captureEnqueuer{}
	if _, err := Emit([]byte(validEvent), injected(), cap); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !cap.got.At.IsZero() {
		t.Fatalf("absent at should stay the zero time for the core to resolve, got %v", cap.got.At)
	}
}

// The `expiresAt` an operator supplies reaches the core-side enqueue verbatim: it
// is the one knob that widens the retry and de-dup windows, so a front door that
// dropped or rewrote it would silently change delivery behavior.
func TestEmit_CarriesExpiresAtIntoEnqueuedEvent(t *testing.T) {
	cap := &captureEnqueuer{}
	if _, err := Emit([]byte(validEvent), injected(), cap); err != nil {
		t.Fatalf("emit: %v", err)
	}
	want, err := time.Parse(time.RFC3339, "2099-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !cap.got.ExpiresAt.Equal(want) {
		t.Fatalf("enqueued event ExpiresAt = %v, want %v", cap.got.ExpiresAt, want)
	}
}

func TestEmit_DedupesReEmitWhileRetained(t *testing.T) {
	q := newQueue(t)
	if _, err := Emit([]byte(validEvent), LocalLocator(), QueueEnqueuer{Q: q}); err != nil {
		t.Fatal(err)
	}
	res, err := Emit([]byte(validEvent), LocalLocator(), QueueEnqueuer{Q: q})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != eventqueue.Deduped {
		t.Fatalf("re-emit status = %v, want Deduped", res.Status)
	}
}

// --- the trap: QueueEnqueuer must REFUSE a core it cannot reach --------------
//
// QueueEnqueuer used to IGNORE the CoreRef ("the queue is local"), so an operator
// front door wired through it against a DISCOVERED core reported SUCCESS while
// appending to a queue that died with the CLI process. These tests pin the refusal
// that makes that impossible.

func TestQueueEnqueuer_RefusesDiscoveredCore(t *testing.T) {
	q := newQueue(t)
	ref := CoreRef{Socket: "/tmp/core.sock", Token: "tok", Discovered: true}
	if _, err := (QueueEnqueuer{Q: q}).Enqueue(ref, eventqueue.Event{ID: "e", Type: "t"}); !errors.Is(err, ErrWrongEnqueuer) {
		t.Fatalf("enqueue against a discovered core = %v, want ErrWrongEnqueuer", err)
	}
	if d := q.DepthByType()["t"]; d != 0 {
		t.Fatalf("queue depth = %d, want 0 — a refused enqueue must not append locally", d)
	}
}

func TestQueueEnqueuer_RefusesInjectedCore(t *testing.T) {
	if _, err := (QueueEnqueuer{Q: newQueue(t)}).Enqueue(
		CoreRef{Socket: "/tmp/core.sock", Token: "tok"},
		eventqueue.Event{ID: "e", Type: "t"},
	); !errors.Is(err, ErrWrongEnqueuer) {
		t.Fatalf("enqueue against an injected core = %v, want ErrWrongEnqueuer", err)
	}
}

// A ZERO-VALUE ref names no core at all (the thing a half-written locate path
// returns). It must be refused too, rather than default to a local enqueue.
func TestQueueEnqueuer_RefusesZeroValueRef(t *testing.T) {
	if _, err := (QueueEnqueuer{Q: newQueue(t)}).Enqueue(CoreRef{}, eventqueue.Event{ID: "e", Type: "t"}); !errors.Is(err, ErrWrongEnqueuer) {
		t.Fatalf("enqueue against a zero-value ref = %v, want ErrWrongEnqueuer", err)
	}
}

// Emit through a discovered core and QueueEnqueuer must FAIL, not silently report
// a successful injection.
func TestEmit_QueueEnqueuerAgainstDiscoveredCoreFails(t *testing.T) {
	q := newQueue(t)
	loc := Locator{Discover: func() (CoreRef, error) {
		return CoreRef{Socket: "/tmp/core.sock", Token: "tok", Discovered: true}, nil
	}}
	if _, err := Emit([]byte(validEvent), loc, QueueEnqueuer{Q: q}); !errors.Is(err, ErrWrongEnqueuer) {
		t.Fatalf("emit into a discovered core via QueueEnqueuer = %v, want ErrWrongEnqueuer", err)
	}
	if d := q.DepthByType()["review-requested"]; d != 0 {
		t.Fatalf("local queue depth = %d, want 0 — the event must not land in a throwaway local queue", d)
	}
}

func TestLocalLocator_LocatesTheInProcessCore(t *testing.T) {
	got, err := LocalLocator().Locate()
	if err != nil {
		t.Fatalf("LocalLocator: %v", err)
	}
	if !got.Local || got.Socket != "" || got.Discovered {
		t.Fatalf("local ref = %+v, want Local with no socket", got)
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
