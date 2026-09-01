// Package discover is the PRODUCER side of the event model (design 2026-06-25):
// it fires each query's Trigger strategy, runs the triggered queries, and
// ENQUEUES their typed events onto the durable event queue. The role→item
// DispatchContext is DERIVED from an event at the moment of delivery (the event
// is the self-contained transportable fact; the context is ephemeral). Query
// errors propagate (pg2-qq9v): a query failure must NOT masquerade as "no ready
// work".
//
// The queue is the UNIVERSAL INTERMEDIARY (bead pg2-f3mcb.2): every event this
// package produces goes into internal/eventqueue.Queue and nowhere else — the
// former per-pass internal/eventbus.Bus is gone. Source-side push/pull modes are
// unaffected; this is core-internal delivery only.
package discover

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DispatchContext is one (role, item) dispatch, DERIVED from an event.Event at
// delivery (design Q-meta: events cross the queue; contexts are built at
// dispatch). It is the explicit growth point for future resolved fields
// (worktree dir, self_login, template vars); keeping it a struct keeps
// run-role's call shape stable as it accretes fields.
type DispatchContext struct {
	Role roles.Role
	Item item.Item
}

// Validate reports every required field that is missing in a single error, so callers
// (run-role) get a complete diagnostic rather than dispatching a half-filled context.
func (d DispatchContext) Validate() error {
	var missing []string
	if d.Role.Name == "" {
		missing = append(missing, "role")
	}
	if d.Item.ID == "" {
		missing = append(missing, "item")
	}
	if len(missing) > 0 {
		return fmt.Errorf("dispatch context missing required field(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// DeriveContext builds the ephemeral DispatchContext for a role from a
// self-contained event (design Q-meta). Run-time-only fields (worktree dir,
// self_login, template vars) still resolve at dispatch, downstream on this
// context — exactly as before.
func DeriveContext(role roles.Role, e event.Event) DispatchContext {
	return DispatchContext{Role: role, Item: e.Item}
}

// itemPayload / metadataKey / etc. name the fields ToQueueEvent packs an
// item.Item's fields under inside eventqueue.Event.Payload, and ItemFromPayload
// unpacks. This shape is an internal wire convention between this package's
// producer and internal/orchestrator's queue->executor Listener bridge — it is
// NOT a declared, config-checkable per-type payload shape (that is the deferred
// OQ-EVT-CATALOG); a binding narrowing on a payload path would read one of these
// keys, but nothing here validates such a path.
const itemPayloadKey = "item"

// ToQueueEvent converts a producer-emitted event.Event (item-carrying) into the
// eventqueue.Event shape the durable queue stores. The item's fields ride under
// payload["item"]; `at` is left unset so the queue resolves it against its OWN
// ingest clock (INV-EVT-1) rather than the producer's tick time.
func ToQueueEvent(e event.Event) eventqueue.Event {
	return eventqueue.Event{
		ID:   e.ID,
		Type: e.Type,
		Payload: map[string]any{
			itemPayloadKey: map[string]any{
				"id":       e.Item.ID,
				"type":     e.Item.Type,
				"title":    e.Item.Title,
				"metadata": e.Item.Metadata,
			},
			"source": e.Source,
		},
	}
}

// ItemFromPayload reconstructs the item.Item a queue event carries, the inverse
// of ToQueueEvent. A payload missing the expected shape (an externally pushed
// event that never went through ToQueueEvent) yields a zero item.Item rather
// than an error — the same "absent path is a non-match, not an error" posture
// INV-DISP-1 states for a binding's own narrowing path.
func ItemFromPayload(payload map[string]any) item.Item {
	var it item.Item
	raw, ok := payload[itemPayloadKey]
	if !ok {
		return it
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return it
	}
	if v, ok := m["id"].(string); ok {
		it.ID = v
	}
	if v, ok := m["type"].(string); ok {
		it.Type = v
	}
	if v, ok := m["title"].(string); ok {
		it.Title = v
	}
	if v, ok := m["metadata"].(map[string]any); ok {
		it.Metadata = v
	}
	return it
}

// DeriveContextFromQueueEvent is DeriveContext's counterpart for the queue-side
// Listener bridge: it builds the ephemeral DispatchContext from the
// eventqueue.Event a Listener was offered.
func DeriveContextFromQueueEvent(role roles.Role, evt eventqueue.Event) DispatchContext {
	return DispatchContext{Role: role, Item: ItemFromPayload(evt.Payload)}
}

// SourceFailureObserver is notified when a pull-source query exhausts a
// retry attempt and is about to back off before trying again (INV-FAIL-3,
// register gap R21 / bead pg2-00jpn) — the metrics half of the log-only Warn
// line runAndEnqueue already writes at that same point (metrics.Emitter
// implements this via OnSourceFailure). A nil Observer (Produce's default
// when no option is given) is a safe no-op.
type SourceFailureObserver interface {
	OnSourceFailure(source string)
}

// ProduceOption configures an optional capability on Produce (functional
// options, so every existing four-argument call site keeps compiling
// unchanged).
type ProduceOption func(*produceOptions)

type produceOptions struct {
	obs SourceFailureObserver
}

// WithSourceFailureObserver registers obs to be notified of every pull-source
// failure retry Produce's runAndEnqueue makes (INV-FAIL-3). The production
// call site is cmd/pr-pool's bootCore (run.go), which assigns its constructed
// metrics.Emitter to orchestrator.Orchestrator.SourceFailureObserver;
// Orchestrator.ProduceTick then passes it to this option on every
// discover.Produce call (bead pg2-00jpn) — closing the gap where source
// failures were recorded to logs only in the running binary.
func WithSourceFailureObserver(obs SourceFailureObserver) ProduceOption {
	return func(o *produceOptions) { o.obs = obs }
}

// Produce fires the query set against the queue for one tick: it runs every
// PeriodTrigger query (reproducing today's once-per-pass pull), then settles any
// ThresholdTrigger queries whose upstream now has "enough events" queued.
// ManualTrigger queries never fire here (only via the smoke harness). Each
// emitted event is stamped with its source query name (provenance) before it is
// enqueued. A query failure retries per its configured pull-source failure
// backoff (INV-FAIL-3, pg2-0c8yz) before it propagates (pg2-qq9v: a query
// failure must NOT masquerade as "no ready work").
func Produce(ctx context.Context, env query.Env, sources query.SourceSet, q *eventqueue.Queue, opts ...ProduceOption) error {
	var po produceOptions
	for _, opt := range opts {
		opt(&po)
	}
	return produce(ctx, env, sources, q, realSleep, po.obs)
}

// sleepFunc waits for d, honoring ctx cancellation — the seam produce's
// pull-source failure backoff sleeps on between retry attempts (INV-FAIL-3),
// injected so tests never sleep real time.
type sleepFunc func(ctx context.Context, d time.Duration) error

// realSleep is sleepFunc's production implementation.
func realSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// produce is Produce's body, parameterized on the sleep seam so a test can
// exercise the pull-source failure backoff's retry loop without waiting real
// time. Produce itself is the production entry point (realSleep).
func produce(ctx context.Context, env query.Env, sources query.SourceSet, q *eventqueue.Queue, sleep sleepFunc, obs SourceFailureObserver) error {
	fired := make([]bool, len(sources))
	// Period-driven (and any non-threshold, non-manual) queries fire every pass.
	for i, s := range sources {
		t := s.Query.Trigger()
		if query.IsManual(t) {
			continue
		}
		if _, isThreshold := query.Threshold(t); isThreshold {
			continue
		}
		if err := runAndEnqueue(ctx, env, s, q, sleep, obs); err != nil {
			return err
		}
		fired[i] = true
	}
	// Threshold ("enough-events") settling: a threshold query fires once its bound
	// upstream has produced >= Count events queued. Bounded fixpoint so a chain of
	// threshold queries can cascade within the pass without looping forever. Depth
	// is read FRESH on every check (via q.DepthByType()) so an event a threshold
	// query itself just enqueued this same pass can unblock a later one.
	for iter := 0; iter <= len(sources); iter++ {
		progressed := false
		for i, s := range sources {
			if fired[i] {
				continue
			}
			tt, ok := query.Threshold(s.Query.Trigger())
			if !ok {
				continue
			}
			depthByType := q.DepthByType()
			depth := 0
			for _, b := range tt.Binds {
				depth += depthByType[b]
			}
			if depth >= tt.Count {
				if err := runAndEnqueue(ctx, env, s, q, sleep, obs); err != nil {
					return err
				}
				fired[i] = true
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return nil
}

// runAndEnqueue runs one source's query, RETRYING on failure per its
// configured pull-source failure backoff (INV-FAIL-3, pg2-0c8yz) before giving
// up, then enqueues every emitted event onto the durable queue, stamping the
// source query name as provenance when the query left it blank.
//
// The failure backoff is DISTINCT from Trigger's success-path polling
// interval: Trigger says how often to ask when things are fine; this says how
// long to wait before asking again after the source itself reported a failure
// (unavailable / out of resources). It is bounded by its OWN Retries count —
// unlike the handler retry cadence (INV-FAIL-2), which an event's expiresAt
// bounds externally (INV-EVT-4), a pull source has no such external bound, so
// this loop caps its own attempts or a source that stays down would retry
// forever inside one drain pass.
//
// Retries defaults to 0 (fail fast) for any query that has not opted in via
// [query.failure_backoff] or the pool-level default — exactly pg2-qq9v's
// original behavior ("a query failure must NOT masquerade as no ready work"),
// unchanged for every existing deployment that has not configured this.
func runAndEnqueue(ctx context.Context, env query.Env, s query.Source, q *eventqueue.Queue, sleep sleepFunc, obs SourceFailureObserver) error {
	fb := s.Query.FailureBackoff()
	var evts []event.Event
	var err error
	for attempt := 0; ; attempt++ {
		evts, err = s.Query.Run(ctx, env)
		if err == nil {
			break
		}
		if attempt >= fb.Retries {
			// Propagate: a query failure must NOT masquerade as "no ready work", or
			// the pool silently idles on infra failure (pg2-qq9v) — unchanged once
			// any configured retries are exhausted.
			return fmt.Errorf("produce %s: %w", s.Name, err)
		}
		wait := fb.Policy.Duration(attempt + 1)
		slog.Warn("pull-source query failed; retrying after backoff (INV-FAIL-3)",
			"source", s.Name, "attempt", attempt+1, "wait", wait, "err", err)
		// The metrics half of the log line above (INV-FAIL-3, register gap R21 /
		// bead pg2-00jpn): every retry notifies the configured observer, same as
		// the log fires — not the final give-up return above, which the caller
		// surfaces its own way.
		if obs != nil {
			obs.OnSourceFailure(s.Name)
		}
		if serr := sleep(ctx, wait); serr != nil {
			return fmt.Errorf("produce %s: %w", s.Name, serr)
		}
	}
	for _, e := range evts {
		if e.Source == "" {
			e.Source = s.Name
		}
		if _, err := q.Enqueue(ToQueueEvent(e)); err != nil {
			return fmt.Errorf("enqueue %s: %w", s.Name, err)
		}
	}
	return nil
}

// QueriesForRole returns the sources whose emitted event types intersect the
// role's Binds — the producers that feed this role. Used by the run-query smoke
// harness (which resolves a role name, then runs the queries wired to it).
func QueriesForRole(sources query.SourceSet, role roles.Role) query.SourceSet {
	bindSet := make(map[string]bool, len(role.Binds))
	for _, b := range role.Binds {
		bindSet[b] = true
	}
	var out query.SourceSet
	for _, s := range sources {
		for _, e := range s.Query.Emits() {
			if bindSet[e] {
				out = append(out, s)
				break
			}
		}
	}
	return out
}
