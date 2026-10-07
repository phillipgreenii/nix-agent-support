package discover

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
)

// stdoutCommander is a query.Commander double returning canned stdout, so a
// real query.CommandQuery decodes it exactly as it decodes a real adapter's
// output (the path that fills event.Attributes).
type stdoutCommander struct{ out string }

func (c stdoutCommander) Run(context.Context, []string) ([]byte, error) { return []byte(c.out), nil }

// decliningListener declines every offer as a pre-accept "retry" decline (the
// shape orchestrator's roleListener uses for DEC-RETRY-2), recording each
// offered (resolved) event.
type decliningListener struct {
	binds   map[string]bool
	offered []eventqueue.Event
}

func (l *decliningListener) ID() string                        { return "desk" }
func (l *decliningListener) Matches(evt eventqueue.Event) bool { return l.binds[evt.Type] }
func (l *decliningListener) Offer(o eventqueue.Offering) eventqueue.OfferResult {
	l.offered = append(l.offered, o.Event)
	return eventqueue.OfferResult{Accepted: false, Decline: eventqueue.DeclineNone, DeclineDetail: "dispatch-retry"}
}

func commandSource(t *testing.T, stdout string) (query.SourceSet, query.Env) {
	t.Helper()
	cq := query.CommandQuery{
		Meta:   query.Meta{EmitTypes: []string{"pr.changed"}, Trig: query.PeriodTrigger{}},
		Argv:   []string{"adapter"},
		Format: query.FormatJSONL,
	}
	return query.SourceSet{{Name: "desk-pr-changes", Query: cq}}, query.Env{Cmd: stdoutCommander{out: stdout}}
}

func TestToQueueEvent_CopiesExpiresAtAndAtFromAttributes(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	exp := at.Add(30 * time.Minute)
	e := itemEvt("pr.changed", "pr-1")
	e.Attributes = map[string]any{"at": at, "expiresAt": exp}

	qe := ToQueueEvent(e)
	if !qe.At.Equal(at) || !qe.ExpiresAt.Equal(exp) {
		t.Fatalf("At/ExpiresAt = %v / %v, want %v / %v", qe.At, qe.ExpiresAt, at, exp)
	}
}

func TestToQueueEvent_NoTimingAttributesLeavesBothUnset(t *testing.T) {
	qe := ToQueueEvent(itemEvt("pr.changed", "pr-1"))
	if !qe.At.IsZero() || !qe.ExpiresAt.IsZero() {
		t.Fatalf("At/ExpiresAt = %v / %v, want both zero (the queue resolves them at ingest, INV-EVT-1)", qe.At, qe.ExpiresAt)
	}
	// A wrong-typed attribute is ignored rather than trusted.
	e := itemEvt("pr.changed", "pr-1")
	e.Attributes = map[string]any{"expiresAt": "2026-10-07T12:00:00Z"}
	if qe := ToQueueEvent(e); !qe.ExpiresAt.IsZero() {
		t.Fatalf("a non-time expiresAt attribute must not be copied, got %v", qe.ExpiresAt)
	}
}

// A command-query record with a future expiresAt reaches the queue with that
// ExpiresAt and is retained past its first (declined) settle; a record with no
// expiresAt is born expired and is settled by its single attempt (INV-EVT-1 /
// DEC-RETRY-2). Goes through the real CommandQuery decode and Produce.
func produceAndSettleOnce(t *testing.T, stdout string) (*eventqueue.Queue, *decliningListener) {
	t.Helper()
	sources, env := commandSource(t, stdout)
	q := newQueue(t)
	l := &decliningListener{binds: map[string]bool{"pr.changed": true}}
	q.Register(l)
	if _, err := Produce(context.Background(), env, sources, q, core.NewBindings("pr.changed")); err != nil {
		t.Fatal(err)
	}
	q.Dispatch()
	q.Expire()
	return q, l
}

func TestProduce_CommandQueryExpiresAtReachesQueueAndRetainsPastFirstSettle(t *testing.T) {
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	q, l := produceAndSettleOnce(t, fmt.Sprintf(`{"id":"with-window","type":"pr","expiresAt":%q}`+"\n", exp.Format(time.RFC3339)))

	if len(l.offered) != 1 {
		t.Fatalf("offers = %d, want 1", len(l.offered))
	}
	got := l.offered[0]
	if !got.ExpiresAt.Equal(exp) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, exp)
	}
	if !got.At.Before(got.ExpiresAt) {
		t.Fatalf("At = %v must precede ExpiresAt %v (At is resolved by the queue's ingest clock)", got.At, got.ExpiresAt)
	}
	if depth := q.DepthByType()["pr.changed"]; depth != 1 {
		t.Fatalf("depth after first settle = %d, want 1 (retained for a re-offer inside its window)", depth)
	}
}

func TestProduce_CommandQueryWithoutExpiresAtIsSettledByItsSingleAttempt(t *testing.T) {
	q, l := produceAndSettleOnce(t, `{"id":"no-window","type":"pr"}`+"\n")

	if len(l.offered) != 1 {
		t.Fatalf("offers = %d, want 1", len(l.offered))
	}
	if got := l.offered[0]; !got.ExpiresAt.Equal(got.At) {
		t.Fatalf("ExpiresAt = %v, want == At %v (born expired)", got.ExpiresAt, got.At)
	}
	if depth := q.DepthByType()["pr.changed"]; depth != 0 {
		t.Fatalf("depth after first settle = %d, want 0 (a born-expired event gets exactly one attempt)", depth)
	}
}
