package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/item"
)

func evt(typ, id string) event.Event {
	return event.NewItemEvent(typ, "src", item.Item{ID: id, Type: "task"})
}

// captureLog records emitted kinds for observability assertions.
type captureLog struct{ kinds []string }

func (c *captureLog) Emit(_, kind, _ string, _ map[string]any) error {
	c.kinds = append(c.kinds, kind)
	return nil
}

func (c *captureLog) has(kind string) bool {
	for _, k := range c.kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func TestBus_fanOut_oneQueryToNRoles(t *testing.T) {
	b := New()
	b.Subscribe("feedback", "feedback.ready")
	b.Subscribe("worker", "work.ready")
	// 1 query : N roles — a single event type fans out to every subscribed role.
	b.Subscribe("audit", "feedback.ready")

	ctx := context.Background()
	_ = b.Publish(ctx, evt("feedback.ready", "zr-1"))

	fb, _ := b.Lease(ctx, "feedback", 10)
	au, _ := b.Lease(ctx, "audit", 10)
	wk, _ := b.Lease(ctx, "worker", 10)
	if len(fb) != 1 || len(au) != 1 {
		t.Fatalf("event must fan out to both subscribers: feedback=%d audit=%d", len(fb), len(au))
	}
	if len(wk) != 0 {
		t.Fatalf("worker binds a different type; must get nothing, got %d", len(wk))
	}
}

func TestBus_competingConsumers_leaseOncePerEvent(t *testing.T) {
	b := New()
	b.Subscribe("worker", "work.ready")
	ctx := context.Background()
	_ = b.Publish(ctx, evt("work.ready", "zr-1"))

	first, _ := b.Lease(ctx, "worker", 1)
	second, _ := b.Lease(ctx, "worker", 1)
	if len(first) != 1 {
		t.Fatalf("first lease should get the event, got %d", len(first))
	}
	if len(second) != 0 {
		t.Fatalf("a leased event must not be re-leased to a competing consumer, got %d", len(second))
	}
	// After Nack it returns to the queue and is leasable again.
	_ = b.Nack(ctx, "worker", "work.ready:zr-1")
	third, _ := b.Lease(ctx, "worker", 1)
	if len(third) != 1 {
		t.Fatalf("nacked event must be re-leasable, got %d", len(third))
	}
}

func TestBus_capGate_leaseCapMinusInflight(t *testing.T) {
	b := New()
	b.Subscribe("worker", "work.ready")
	ctx := context.Background()
	for _, id := range []string{"zr-1", "zr-2", "zr-3"} {
		_ = b.Publish(ctx, evt("work.ready", id))
	}
	const cap = 1
	// n = Cap - Inflight; at pass start Inflight==0 so n==Cap==1.
	n := cap - b.Inflight("worker")
	leased, _ := b.Lease(ctx, "worker", n)
	if len(leased) != 1 {
		t.Fatalf("cap=1 must lease exactly one, got %d", len(leased))
	}
	if b.Inflight("worker") != 1 {
		t.Fatalf("inflight must be 1 after leasing, got %d", b.Inflight("worker"))
	}
	// Still leased (not acked): the gate now yields nothing more.
	n2 := cap - b.Inflight("worker")
	more, _ := b.Lease(ctx, "worker", n2)
	if len(more) != 0 {
		t.Fatalf("role at cap must lease nothing, got %d", len(more))
	}
	// Ack frees capacity; next pass leases the next event.
	_ = b.Ack(ctx, "worker", leased[0].ID)
	if b.Inflight("worker") != 0 {
		t.Fatalf("inflight must drop to 0 after ack, got %d", b.Inflight("worker"))
	}
	n3 := cap - b.Inflight("worker")
	next, _ := b.Lease(ctx, "worker", n3)
	if len(next) != 1 || next[0].Item.ID != "zr-2" {
		t.Fatalf("after ack, next lease should get zr-2, got %+v", next)
	}
}

func TestBus_dedup_stillHeldEventNotReEnqueued(t *testing.T) {
	b := New()
	b.Subscribe("worker", "work.ready")
	ctx := context.Background()
	_ = b.Publish(ctx, evt("work.ready", "zr-1"))
	// Same still-ready bead re-emitted while its event is still held: deduped.
	_ = b.Publish(ctx, evt("work.ready", "zr-1"))
	leased, _ := b.Lease(ctx, "worker", 10)
	if len(leased) != 1 {
		t.Fatalf("a still-held event must be deduped by id, got %d", len(leased))
	}
}

func TestBus_ttlSweep_evictsExpiredAndLogs(t *testing.T) {
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	clk := &clock{t: base}
	log := &captureLog{}
	b := New(WithClock(clk.now), WithTTL(30*time.Minute), WithLogger(log))
	b.Subscribe("worker", "work.ready")
	ctx := context.Background()
	_ = b.Publish(ctx, evt("work.ready", "zr-1"))

	// Advance past TTL; the next bus operation lazily sweeps it.
	clk.t = base.Add(31 * time.Minute)
	leased, _ := b.Lease(ctx, "worker", 10)
	if len(leased) != 0 {
		t.Fatalf("expired event must not be leasable, got %d", len(leased))
	}
	if !log.has("event_expired") {
		t.Fatalf("expiry must be logged; kinds=%v", log.kinds)
	}
	// Re-emission, not resurrection: re-publishing after expiry enqueues fresh.
	clk.t = base.Add(32 * time.Minute)
	_ = b.Publish(ctx, evt("work.ready", "zr-1"))
	again, _ := b.Lease(ctx, "worker", 10)
	if len(again) != 1 {
		t.Fatalf("re-emitted event after expiry must enqueue fresh, got %d", len(again))
	}
}

func TestBus_aggregator_allOf(t *testing.T) {
	b := New()
	spec := event.CorrelationSpec{Completeness: event.AllOf{Types: []string{"a.done", "b.done"}}}
	b.SubscribeAggregate("collector", []string{"a.done", "b.done"}, spec)
	ctx := context.Background()

	a := evt("a.done", "x-a")
	a.CorrelationID = "PR-7"
	_ = b.Publish(ctx, a)
	// Not complete yet — nothing on the queue.
	if got, _ := b.Lease(ctx, "collector", 10); len(got) != 0 {
		t.Fatalf("aggregate must not emit before complete, got %d", len(got))
	}
	bb := evt("b.done", "x-b")
	bb.CorrelationID = "PR-7"
	_ = b.Publish(ctx, bb)
	got, _ := b.Lease(ctx, "collector", 10)
	if len(got) != 1 {
		t.Fatalf("aggregate must emit ONE event when complete, got %d", len(got))
	}
	if got[0].CorrelationID != "PR-7" || got[0].Item.ID != "PR-7" {
		t.Fatalf("aggregated event must carry the correlation target, got %+v", got[0])
	}
}

func TestBus_aggregator_correlationIsolation(t *testing.T) {
	b := New()
	spec := event.CorrelationSpec{Completeness: event.AllOf{Types: []string{"a.done", "b.done"}}}
	b.SubscribeAggregate("collector", []string{"a.done", "b.done"}, spec)
	ctx := context.Background()
	// Two correlation ids must not cross-contaminate: a.done(PR-1) + b.done(PR-2)
	// completes NEITHER.
	a := evt("a.done", "x")
	a.CorrelationID = "PR-1"
	_ = b.Publish(ctx, a)
	bb := evt("b.done", "y")
	bb.CorrelationID = "PR-2"
	_ = b.Publish(ctx, bb)
	if got, _ := b.Lease(ctx, "collector", 10); len(got) != 0 {
		t.Fatalf("distinct correlation ids must not complete an aggregate, got %d", len(got))
	}
}

func TestBus_aggregator_incompleteExpires(t *testing.T) {
	base := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	clk := &clock{t: base}
	log := &captureLog{}
	b := New(WithClock(clk.now), WithTTL(30*time.Minute), WithLogger(log))
	spec := event.CorrelationSpec{Completeness: event.AllOf{Types: []string{"a.done", "b.done"}}}
	b.SubscribeAggregate("collector", []string{"a.done", "b.done"}, spec)
	ctx := context.Background()

	a := evt("a.done", "x")
	a.CorrelationID = "PR-9"
	_ = b.Publish(ctx, a)
	// The sibling never arrives; advance past TTL and trigger a sweep.
	clk.t = base.Add(31 * time.Minute)
	_ = b.Publish(ctx, evt("unrelated", "z")) // any bus op sweeps
	if !log.has("event_aggregate_expired") {
		t.Fatalf("incomplete aggregate must expire and log; kinds=%v", log.kinds)
	}
	// Even if the sibling arrives now, the aggregate was evicted — it does not
	// resurrect the earlier half.
	bb := evt("b.done", "y")
	bb.CorrelationID = "PR-9"
	_ = b.Publish(ctx, bb)
	if got, _ := b.Lease(ctx, "collector", 10); len(got) != 0 {
		t.Fatalf("expired aggregate must not complete from a late sibling alone, got %d", len(got))
	}
}

func TestBus_countOfCompleteness(t *testing.T) {
	b := New()
	spec := event.CorrelationSpec{Completeness: event.CountOf{N: 2}}
	b.SubscribeAggregate("collector", []string{"x.ready"}, spec)
	ctx := context.Background()
	for _, id := range []string{"i1", "i2"} {
		e := evt("x.ready", id)
		e.CorrelationID = "grp"
		_ = b.Publish(ctx, e)
	}
	got, _ := b.Lease(ctx, "collector", 10)
	if len(got) != 1 {
		t.Fatalf("count-of(2) must complete after 2 correlated events, got %d", len(got))
	}
}

// clock is a mock clock seam.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
