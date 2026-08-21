// Package discover is the PRODUCER side of the event model (design 2026-06-25):
// it fires each query's Trigger strategy, runs the triggered queries, and
// publishes their typed events onto the bus. The role→item DispatchContext is
// DERIVED from an event at the moment of delivery (the event is the
// self-contained transportable fact; the context is ephemeral). Query errors
// propagate (pg2-qq9v): a query failure must NOT masquerade as "no ready work".
package discover

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventbus"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// DispatchContext is one (role, item) dispatch, DERIVED from an event.Event at
// delivery (design Q-meta: events cross the bus; contexts are built at dispatch).
// It is the explicit growth point for future resolved fields (worktree dir,
// self_login, template vars); keeping it a struct keeps run-role's call shape
// stable as it accretes fields.
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

// Produce fires the query set against the bus for one drain tick: it publishes
// the internal clock.tick event (Q1: the period tick is itself an event), runs
// every PeriodTrigger query (reproducing today's once-per-pass pull), then
// settles any ThresholdTrigger queries whose upstream now has "enough events".
// ManualTrigger queries never fire here (only via the smoke harness). Each
// emitted event is stamped with its source query name (provenance) before
// publish. A query failure retries per its configured pull-source failure
// backoff (INV-FAIL-3, pg2-0c8yz) before it propagates (pg2-qq9v: a query
// failure must NOT masquerade as "no ready work").
func Produce(ctx context.Context, env query.Env, sources query.SourceSet, bus *eventbus.Bus, now time.Time) error {
	return produce(ctx, env, sources, bus, now, realSleep)
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
func produce(ctx context.Context, env query.Env, sources query.SourceSet, bus *eventbus.Bus, now time.Time, sleep sleepFunc) error {
	// The tick is itself an event — uniform with the rest of the model and the
	// observable "a pass happened" signal. No role binds it; it fans out to no
	// queue.
	_ = bus.Publish(ctx, event.Event{ID: "clock.tick:" + now.Format(time.RFC3339Nano), Type: event.ClockTick, EmittedAt: now})

	fired := make([]bool, len(sources))
	// Period-driven (and any non-threshold, non-manual) queries react to the tick.
	for i, s := range sources {
		t := s.Query.Trigger()
		if query.IsManual(t) {
			continue
		}
		if _, isThreshold := query.Threshold(t); isThreshold {
			continue
		}
		if err := runAndPublish(ctx, env, s, bus, sleep); err != nil {
			return err
		}
		fired[i] = true
	}
	// Threshold ("enough-events") settling: a threshold query fires once its bound
	// upstream has produced >= Count events. Bounded fixpoint so a chain of
	// threshold queries can cascade within the pass without looping forever.
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
			depth := 0
			for _, b := range tt.Binds {
				depth += bus.Depth(b)
			}
			if depth >= tt.Count {
				if err := runAndPublish(ctx, env, s, bus, sleep); err != nil {
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

// runAndPublish runs one source's query, RETRYING on failure per its
// configured pull-source failure backoff (INV-FAIL-3, pg2-0c8yz) before giving
// up, then publishes every emitted event, stamping the source query name as
// provenance when the query left it blank.
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
func runAndPublish(ctx context.Context, env query.Env, s query.Source, bus *eventbus.Bus, sleep sleepFunc) error {
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
		if serr := sleep(ctx, wait); serr != nil {
			return fmt.Errorf("produce %s: %w", s.Name, serr)
		}
	}
	for _, e := range evts {
		if e.Source == "" {
			e.Source = s.Name
		}
		if err := bus.Publish(ctx, e); err != nil {
			return fmt.Errorf("publish %s: %w", s.Name, err)
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
