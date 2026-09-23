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

	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/event"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/item"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/roles"
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

// ToQueueEvent converts a producer-emitted event.Event (item-carrying) into the
// eventqueue.Event shape the durable queue stores. The item's own fields ride
// at Payload's top level, VERBATIM — no itemPayloadKey wrapping layer, no
// core-side re-shaping (docket pg2-oju6w's Task 5.6, ADR 0065's "Wire
// contract" section, closing register row GOAL-MIN-1 / bead pg2-4t5ey): the
// item-SHAPE (which fields exist, what they mean) is the registered handler
// participant's own contract now, never interpreted here beyond this one
// pass-through construction. Field names match what the handler's own
// dispatch entrypoint decodes (the same names discover's own, now-deleted,
// ItemFromPayload used to read). Source is no longer stamped onto the wire
// payload either — nothing downstream of this package ever read
// payload["source"], so packing it was itself the same over-reach GOAL-MIN-1
// flags, just on a field a binding never named.
//
// `at` is left unset so the queue resolves it against its OWN ingest clock
// (INV-EVT-1) rather than the producer's tick time.
func ToQueueEvent(e event.Event) eventqueue.Event {
	return eventqueue.Event{
		ID:   e.ID,
		Type: e.Type,
		Payload: map[string]any{
			"id":       e.Item.ID,
			"type":     e.Item.Type,
			"title":    e.Item.Title,
			"metadata": e.Item.Metadata,
		},
	}
}

// DeriveContextFromQueueEvent is DeriveContext's counterpart for the queue-side
// Listener bridge: it builds the ephemeral DispatchContext from the
// eventqueue.Event a Listener was offered.
//
// It resolves ONLY Item.ID here — the one field this package's own downstream
// dispatch bookkeeping still needs (Role.ExternalID's beadID argument,
// report.Result's bead refs, the dispatch-result log line). The REST of
// item.Item's shape (Type/Title/Metadata) is no longer this package's
// business to reconstruct: docket pg2-oju6w's Task 5.6 moved that
// interpretation to the registered handler participant's own dispatch
// entrypoint, which decodes the identical opaque payload object this
// function reads only one key of. A payload missing the expected "id" key
// (an externally pushed event that never went through ToQueueEvent) yields a
// zero-value Item.ID rather than an error — the same "absent path is a
// non-match, not an error" posture INV-DISP-1 states for a binding's own
// narrowing path.
func DeriveContextFromQueueEvent(role roles.Role, evt eventqueue.Event) DispatchContext {
	id, _ := evt.Payload["id"].(string)
	return DispatchContext{Role: role, Item: item.Item{ID: id}}
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

// SourceActivityObserver is notified of a pull source's own per-pass
// activity (DEC-OBS-2, bead pg2-ugcrb; INV-OBS-2's per-source recent-history
// and "processing now" signal) — a second, purely-observational seam
// alongside SourceFailureObserver, not a replacement for it:
// SourceFailureObserver fires on each RETRY within a pass's backoff loop,
// while this one brackets the pass's underlying s.Query.Run call itself
// (OnSourceFetchStart/OnSourceFetchEnd, the "currently fetching" window) and
// reports that call's own final outcome (OnSourceProduced on success,
// OnSourceGaveUp when the retry budget this pass is exhausted — mirroring
// ProduceReport.Emitted/Rejected and SourceErrors respectively). A nil
// observer (Produce's default when no option is given) is a safe no-op.
type SourceActivityObserver interface {
	// OnSourceFetchStart fires immediately before s.Query.Run is invoked for
	// this pass's attempt; OnSourceFetchEnd fires immediately after it
	// returns, whatever the outcome. The two bracket the window a source is
	// "currently fetching" — a RETRY re-enters this same bracket for each
	// attempt, so a caller tracking "in flight" as a simple counter (rather
	// than a boolean) handles a retrying source correctly without extra work.
	OnSourceFetchStart(source string)
	OnSourceFetchEnd(source string)
	// OnSourceProduced fires once, after a pass's query succeeded (on the
	// first attempt or after retrying), reporting how many events THIS pass
	// emitted and rejected for source.
	OnSourceProduced(source string, emitted, rejected int)
	// OnSourceGaveUp fires once, when this pass's retry budget is exhausted
	// and the failure is recorded into ProduceReport.SourceErrors — the
	// source-side final-failure outcome, distinct from
	// SourceFailureObserver.OnSourceFailure's per-retry signal.
	OnSourceGaveUp(source string)
}

// ProduceOption configures an optional capability on Produce (functional
// options, so every existing call site keeps compiling unchanged).
type ProduceOption func(*produceOptions)

type produceOptions struct {
	obs         SourceFailureObserver
	activityObs SourceActivityObserver
}

// WithSourceFailureObserver registers obs to be notified of every pull-source
// failure retry Produce's runAndEnqueue makes (INV-FAIL-3). The production
// call site is cmd/pg-router's bootCore (run.go), which assigns its constructed
// metrics.Emitter to orchestrator.Orchestrator.SourceFailureObserver;
// Orchestrator.ProduceTick then passes it to this option on every
// discover.Produce call (bead pg2-00jpn) — closing the gap where source
// failures were recorded to logs only in the running binary.
func WithSourceFailureObserver(obs SourceFailureObserver) ProduceOption {
	return func(o *produceOptions) { o.obs = obs }
}

// WithSourceActivityObserver registers obs to be notified of every pull
// source's own per-pass fetch window and outcome (DEC-OBS-2, bead
// pg2-ugcrb). The production call site mirrors WithSourceFailureObserver's:
// cmd/pg-router's bootCore assigns its activityObserver to
// orchestrator.Orchestrator.SourceActivityObserver, and ProduceTick passes it
// to this option on every discover.Produce call.
func WithSourceActivityObserver(obs SourceActivityObserver) ProduceOption {
	return func(o *produceOptions) { o.activityObs = obs }
}

// ProduceReport summarizes one Produce pass so a single failing pull source no
// longer withholds dispatch/expiry for every other source (INV-FAIL-3,
// INV-EVT-1; ADR per Task 0.6, resolving INV-PREC-1 as never-drop-work): a
// source's own failure or an undeclared-type event is ISOLATED into this
// report rather than aborting the whole pass.
//
//   - SourceErrors holds, per source name, the error from a pull-source query
//     whose configured failure backoff's retries were exhausted this pass. A
//     ctx cancellation observed while waiting out the backoff, or a durable-queue
//     Enqueue failure (a store failure is not a source failure), still propagate
//     as Produce's own returned error instead — see runAndEnqueue's doc comment
//     for the full classification.
//   - LastTick records, per source name, when this pass fired that source's
//     query (attempted, regardless of outcome) — the timestamp Task 1.3's
//     per-source cadence measures period against.
//   - Emitted counts, per source name, the events this pass actually enqueued.
//   - Rejected counts, per source name, events this pass discarded because
//     their type is UNDECLARED — no configured role binds to it (INV-DISP-3's
//     configuration-wide view, now held on the pull path too, not just push)
//     — never enqueued.
//   - Failure carries, per source name, THIS pass's pull-source
//     failure-backoff state (INV-FAIL-3, Task 4.1) — present only for a
//     source whose retry budget this pass exhausted (an entry in
//     SourceErrors too); absent for a source that succeeded, was not due
//     to fire (Task 1.3's cadence gating), or was never attempted. Fed
//     from the SAME give-up point runAndEnqueue already records
//     SourceErrors at — never a second, independently-tracked failure
//     state.
type ProduceReport struct {
	SourceErrors map[string]error
	LastTick     map[string]time.Time
	Emitted      map[string]int
	Rejected     map[string]int
	Failure      map[string]FailureInfo
}

// FailureInfo is one source's pull-source failure-backoff state at the end
// of a produce pass (INV-FAIL-3) — a discover-package twin of
// core.FailureInfo (Count, NextEligible): discover does not import core
// (the dependency already runs the other way), so cmd/pg-router's
// sourceReportsFor is what translates one into the other; both name the
// same two fields so that translation is a plain field copy.
type FailureInfo struct {
	// Count is the number of consecutive attempts this pass made before
	// giving up (fb.Retries+1 for a source that exhausted its retry
	// budget).
	Count int
	// NextEligible is when the NEXT attempt would become eligible: the
	// backoff wait computed for the attempt that was about to run when the
	// retry budget was exhausted, added to now. For a fail-fast query
	// (Retries == 0, the default — no [query.failure_backoff] configured),
	// no wait is ever computed before giving up on the first attempt, so
	// this is simply now — "eligible immediately" is the honest signal
	// there (the query's own Trigger/cadence, not this backoff, decides
	// when it is actually retried).
	NextEligible time.Time
}

// newProduceReport returns a ProduceReport with every map initialized (never
// nil), so a caller can index it unconditionally.
func newProduceReport() ProduceReport {
	return ProduceReport{
		SourceErrors: make(map[string]error),
		LastTick:     make(map[string]time.Time),
		Emitted:      make(map[string]int),
		Rejected:     make(map[string]int),
		Failure:      make(map[string]FailureInfo),
	}
}

// Cadence is the per-source next-fire substrate Task 1.3 threads into produce:
// a PeriodTrigger source is skipped (not fired) on a pass where it is not yet
// due.
//
//   - LastTick is fed FORWARD from a PREVIOUS pass's own ProduceReport.LastTick
//     — production wiring is orchestrator.Orchestrator.ProduceTick, which
//     persists it across ticks (an Orchestrator outlives one Produce call; a
//     bare Produce call does not). A source name ABSENT from LastTick has never
//     fired under this cadence and is due immediately — this is what preserves
//     Produce's original always-fire-every-pass behavior for a source's very
//     first pass, and for Produce itself (whose zero Cadence leaves LastTick
//     nil, so EVERY period source is due on EVERY call — no gating at all).
//   - PollInterval is the pool-wide fallback period for a PeriodTrigger whose
//     own Every is the zero value. In practice this only matters for an
//     unconfigured built-in Go query (query.Meta{}'s default PeriodTrigger{}
//     leaves Every at zero); a config-declared query's Every is already
//     resolved to cfg.PollInterval by the config registry's buildTrigger, so a
//     per-source Every > 0 always wins over this fallback when both are set.
type Cadence struct {
	LastTick     map[string]time.Time
	PollInterval time.Duration
}

// Produce fires the query set against the queue for one tick: it runs every
// PeriodTrigger query (reproducing today's once-per-pass pull — Produce itself
// applies no cadence gating; see ProduceWithCadence for that), then settles any
// ThresholdTrigger queries whose upstream now has "enough events" queued.
// ManualTrigger queries never fire here (only via the smoke harness). Each
// emitted event is stamped with its source query name (provenance), checked
// against declared (the configured role-binding set — the SAME core.Bindings
// core.Listen validates push events against, so the pull and push paths cannot
// disagree), then enqueued. A query failure retries per its configured
// pull-source failure backoff (INV-FAIL-3, pg2-0c8yz) before it is ISOLATED into
// the returned ProduceReport (pg2-qq9v: a query failure must NOT masquerade as
// "no ready work" — but nor may it withhold delivery for any other source,
// INV-PREC-1). opts configures optional capabilities (e.g.
// WithSourceFailureObserver, bead pg2-00jpn) without changing this signature.
func Produce(ctx context.Context, env query.Env, sources query.SourceSet, q *eventqueue.Queue, declared core.Bindings, opts ...ProduceOption) (ProduceReport, error) {
	var po produceOptions
	for _, opt := range opts {
		opt(&po)
	}
	return produce(ctx, env, sources, q, declared, Cadence{}, realSleep, time.Now, po.obs, po.activityObs)
}

// ProduceWithCadence is Produce, additionally honoring cad — the per-source
// next-fire substrate (Task 1.3; see Cadence's doc comment). Production wiring
// is orchestrator.Orchestrator.ProduceTick, which persists cad.LastTick across
// ticks and supplies cad.PollInterval from cfg.PollInterval. opts configures
// optional capabilities the same way Produce's do (e.g.
// WithSourceFailureObserver).
func ProduceWithCadence(ctx context.Context, env query.Env, sources query.SourceSet, q *eventqueue.Queue, declared core.Bindings, cad Cadence, opts ...ProduceOption) (ProduceReport, error) {
	var po produceOptions
	for _, opt := range opts {
		opt(&po)
	}
	return produce(ctx, env, sources, q, declared, cad, realSleep, time.Now, po.obs, po.activityObs)
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

// cadenceDue reports whether a period-triggered source named name is due to
// fire at nowT, honoring cad's per-source next-fire substrate (Task 1.3): a
// source cad.LastTick has no entry for has never fired under this cadence and
// is due immediately — so Produce's zero Cadence (an empty/nil LastTick)
// leaves every period source due on every pass, its pre-Task-1.3 behavior
// unchanged. t is the source's own Trigger(): when it resolves to a
// PeriodTrigger with a non-zero Every, that PER-SOURCE period wins over
// cad.PollInterval; a nil Trigger (query.Meta's own documented default) or a
// PeriodTrigger with Every == 0 falls back to cad.PollInterval, and a Cadence
// with no PollInterval either (period <= 0, no cadence configured at all)
// keeps firing every pass rather than gating on nothing.
func cadenceDue(nowT time.Time, name string, t query.Trigger, cad Cadence) bool {
	last, everFired := cad.LastTick[name]
	if !everFired {
		return true
	}
	period := cad.PollInterval
	if pt, ok := t.(query.PeriodTrigger); ok && pt.Every > 0 {
		period = pt.Every
	}
	if period <= 0 {
		return true
	}
	return !nowT.Before(last.Add(period))
}

// produce is Produce's/ProduceWithCadence's shared body, parameterized on the
// sleep seam (so a test can exercise the pull-source failure backoff's retry
// loop without waiting real time) and the clock seam now (so a cadence test
// can drive successive passes without waiting real time either). Produce and
// ProduceWithCadence are the production entry points (realSleep, time.Now).
func produce(ctx context.Context, env query.Env, sources query.SourceSet, q *eventqueue.Queue, declared core.Bindings, cad Cadence, sleep sleepFunc, now func() time.Time, obs SourceFailureObserver, activityObs SourceActivityObserver) (ProduceReport, error) {
	rpt := newProduceReport()
	fired := make([]bool, len(sources))
	nowT := now()
	// Period-driven (and any non-threshold, non-manual) queries fire every
	// pass EXCEPT one cad reports not yet due (Task 1.3's per-source cadence).
	for i, s := range sources {
		t := s.Query.Trigger()
		if query.IsManual(t) {
			continue
		}
		if _, isThreshold := query.Threshold(t); isThreshold {
			continue
		}
		if !cadenceDue(nowT, s.Name, t, cad) {
			continue
		}
		if err := runAndEnqueue(ctx, env, s, q, declared, sleep, obs, activityObs, &rpt, now); err != nil {
			return rpt, err
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
				if err := runAndEnqueue(ctx, env, s, q, declared, sleep, obs, activityObs, &rpt, now); err != nil {
					return rpt, err
				}
				fired[i] = true
				progressed = true
			}
		}
		if !progressed {
			break
		}
	}
	return rpt, nil
}

// runAndEnqueue runs one source's query, RETRYING on failure per its
// configured pull-source failure backoff (INV-FAIL-3, pg2-0c8yz) before giving
// up, then enqueues every emitted event onto the durable queue. Provenance
// (which source emitted an event) is no longer stamped onto the enqueued
// event at all: docket pg2-oju6w's Task 5.6 stopped ToQueueEvent from
// writing payload["source"] (nothing downstream of this package ever read
// it), so a per-event Source value has nowhere left to ride on the wire —
// s.Name is still this call's own provenance for rpt's per-source
// accounting (LastTick/Emitted/Rejected/SourceErrors/Failure) below, just
// never copied onto the event itself.
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
//
// Error classification (INV-FAIL-3, INV-EVT-1; ADR per Task 0.6 — INV-PREC-1
// resolved as never-drop-work): once Retries is exhausted the failure no
// longer propagates as an error here — it is recorded in
// rpt.SourceErrors[s.Name] and runAndEnqueue returns nil, so produce moves on
// to the next source instead of aborting the whole pass. Only a REAL failure
// still returns an error: ctx cancellation observed while waiting out the
// backoff, or a durable-queue Enqueue failure (a store failure is not a source
// failure).
//
// Every emitted event is checked against declared (the CONFIGURED role-binding
// set, core.Bindings — the same value core.Listen validates push events
// against, INV-DISP-3): an event whose type no configured role binds to is
// REJECTED — counted in rpt.Rejected[s.Name], never enqueued — rather than
// entering the durable queue only to wait, unconsumed, until it expires. A
// rejection is per-event and does not abort the source's other events.
func runAndEnqueue(ctx context.Context, env query.Env, s query.Source, q *eventqueue.Queue, declared core.Bindings, sleep sleepFunc, obs SourceFailureObserver, activityObs SourceActivityObserver, rpt *ProduceReport, now func() time.Time) error {
	rpt.LastTick[s.Name] = now()
	fb := s.Query.FailureBackoff()
	var evts []event.Event
	var err error
	var lastWait time.Duration // zero for a fail-fast query's single, wait-less attempt
	for attempt := 0; ; attempt++ {
		// OnSourceFetchStart/OnSourceFetchEnd bracket EACH attempt (DEC-OBS-2,
		// bead pg2-ugcrb): a retrying source re-enters this bracket per
		// attempt, so a caller tracking "currently fetching" observes it
		// fetching again on each retry rather than missing the window.
		if activityObs != nil {
			activityObs.OnSourceFetchStart(s.Name)
		}
		evts, err = s.Query.Run(ctx, env)
		if activityObs != nil {
			activityObs.OnSourceFetchEnd(s.Name)
		}
		if err == nil {
			break
		}
		if attempt >= fb.Retries {
			// Isolate: a query failure must NOT masquerade as "no ready work"
			// (pg2-qq9v), but it also must not withhold dispatch/expiry for any
			// OTHER source's or any pushed event's already-queued work
			// (INV-FAIL-3, INV-EVT-1, INV-PREC-1) — record it against this source
			// only and let produce continue.
			rpt.SourceErrors[s.Name] = fmt.Errorf("produce %s: %w", s.Name, err)
			// Failure (Task 4.1): the SAME give-up point, recorded alongside
			// SourceErrors above — see FailureInfo's own doc for why
			// NextEligible is simply `now` for a fail-fast (Retries==0) query.
			rpt.Failure[s.Name] = FailureInfo{Count: attempt + 1, NextEligible: now().Add(lastWait)}
			if activityObs != nil {
				activityObs.OnSourceGaveUp(s.Name)
			}
			return nil
		}
		wait := fb.Policy.Duration(attempt + 1)
		lastWait = wait
		slog.Warn("pull-source query failed; retrying after backoff (INV-FAIL-3)",
			"source", s.Name, "attempt", attempt+1, "wait", wait, "err", err)
		// The metrics half of the log line above (INV-FAIL-3, register gap R21 /
		// bead pg2-00jpn): every retry notifies the configured observer, same as
		// the log fires — not the final give-up return above, which rpt.SourceErrors
		// surfaces its own way.
		if obs != nil {
			obs.OnSourceFailure(s.Name)
		}
		if serr := sleep(ctx, wait); serr != nil {
			return fmt.Errorf("produce %s: %w", s.Name, serr)
		}
	}
	for _, e := range evts {
		if !declared.Declares(e.Type) {
			rpt.Rejected[s.Name]++
			continue
		}
		if _, err := q.Enqueue(ToQueueEvent(e)); err != nil {
			return fmt.Errorf("enqueue %s: %w", s.Name, err)
		}
		rpt.Emitted[s.Name]++
	}
	if activityObs != nil {
		activityObs.OnSourceProduced(s.Name, rpt.Emitted[s.Name], rpt.Rejected[s.Name])
	}
	return nil
}

// QueriesForRole returns the sources whose emitted event types intersect the
// role's Binds — the producers that feed this role. Since Task 1.5c, run-query
// no longer smokes a role's whole feeding set (it smokes one named source
// directly); this now backs only the deprecated `run-query <role>` form's
// mapping diagnostic, naming one REAL source that role used to be fed by.
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
