// Package orchestrator is pg-router's mechanical drive loop: discover → per-role
// bounded drain → teardown-all. It owns no claude/tmux mechanics (ccpool does)
// and no LLM. Work-outcome completion (created/closed/handed-back) is the
// registered handler participant's own concern, not this package's (docket
// pg2-oju6w's Task 5.5, ADR 0065's "Wire contract" section, closing
// register row INV-WORKFLOW-1/R20, bead pg2-ctqo2); ccpool state is
// liveness only.
//
// As of docket pg2-oju6w's Task 5.4 (ADR 0065's "Open question resolved" and
// "Wire contract" sections), a dispatch no longer selects its execution path
// by role.Type at all: every role's own handler participant is reached over
// the wire (internal/wireclient's HandlerClient.Dispatch sends
// handler.dispatch, correlated by a dsp-<...> tracking id), and the
// ensure→send→wait ccpool path, the budget watchdog, and a bare-command
// executable are entirely that participant's own private business now
// (Task 5.2/5.3 moved the underlying mechanics to
// packages/pg-router-ccpool-handler). Task 5.4 deliberately left buildResult's
// own remaining created-bead-snapshot detection and bead-status-based
// closed/handed-back branching alone; Task 5.5 (this change) finishes that
// cleanup — snapshotIDs and createdByActor are deleted, and buildResult
// stores wireclient.Reply.Outcome verbatim instead of switching on bead
// status, so this package never again interprets created/closed/handed-back
// itself. Sibling tasks 5.6-5.8 remove the OTHER remaining seams
// (payload-path opacity, permission-mode passthrough, source-side boundary)
// from pr-pool's own dependency graph.
//
// needs_input is intentionally non-terminal: the executor keeps polling such a
// session to MaxWait and alerts the operator once on the edge (executor.waitDone),
// and teardownAll preserves needs_input sessions (does not close them) so a human
// can still `ccpool attach` after the pass. The reap side of that carve-out — left
// open by pg2-th35, which delivered the teardown half only — is now RESOLVED by
// ADR 0037 (pg2-z3aya): ccpool's reaper spares a needs_input session in BOTH its
// TTL and cap-eviction passes, so a session this pass preserves is no longer
// closed minutes later by the reap timer. Preservation is deliberately UNBOUNDED
// (no preserved-session reaper TTL); the accepted cost is a pool that may sit above
// max_sessions until an operator attends or closes the session.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/phillipgreenii/pg-router/internal/config"
	"github.com/phillipgreenii/pg-router/internal/core"
	"github.com/phillipgreenii/pg-router/internal/discover"
	"github.com/phillipgreenii/pg-router/internal/eventlog"
	"github.com/phillipgreenii/pg-router/internal/eventqueue"
	"github.com/phillipgreenii/pg-router/internal/query"
	"github.com/phillipgreenii/pg-router/internal/report"
	"github.com/phillipgreenii/pg-router/internal/roles"
	"github.com/phillipgreenii/pg-router/internal/wireclient"
)

type Orchestrator struct {
	Reg roles.RoleSet
	Cfg config.Config
	Cmd query.Commander  // command-query/role exec seam (default OSCommander)
	Log *eventlog.Writer // may be nil (no-op); threaded onto Watchdog
	// Handler is the wire client dispatch now goes through (Task 5.4),
	// replacing the deleted in-process executor.For(role.Type).Dispatch(...)
	// call and its Deps seam bag entirely. Required for a real dispatch to
	// do anything; a nil Handler makes workOneWithID fail loudly rather than
	// panic (see wireclient.Client.Dispatch's own nil-Command guard for the
	// analogous seam one level down). Which command a wireclient.Client
	// actually invokes per role is that Client's own CommandFor concern
	// (wireclient's package doc), never this Orchestrator's.
	Handler wireclient.HandlerClient
	now     func() time.Time                           // clock seam (default time.Now)
	tick    func(context.Context, time.Duration) error // cancellable wait (default below)

	// SourceFailureObserver is notified of every pull-source query retry
	// ProduceTick's discover.Produce call makes (INV-FAIL-3, register gap
	// R21 / bead pg2-00jpn) — the metrics half of the log-only Warn line
	// discover.go's runAndEnqueue already writes at that same point. Left nil
	// (a safe no-op per discover.WithSourceFailureObserver's doc) by every
	// existing construction site that does not set it; cmd/pg-router's bootCore
	// is the one production site that wires a live metrics.Emitter in here.
	SourceFailureObserver discover.SourceFailureObserver
	// Bindings is the CONFIGURED role-binding set (core.NewBindings over every
	// role's Binds, INCLUDING a role disabled for this run — INV-DISP-3's
	// configuration-wide view). bootCore sets this from the SAME value it passes
	// to core.Listen's Options.Bindings, so the pull path (ProduceTick's
	// undeclared-type rejection) and the push path (core's own ingest
	// validation) can never disagree about which types are declared. A nil
	// Bindings declares nothing, matching core.Bindings.Declares' own doc
	// comment — every produced event is then rejected, so a caller that drives
	// ProduceTick outside bootCore (a test) MUST set this explicitly.
	Bindings core.Bindings
	// Registry is the core's participant registry (Task 2.3, pg2-84o3m.22):
	// when set, NewListener captures it onto each roleListener so Offer can
	// consult self-status/lifecycle availability (core.Registry.Available,
	// perf-F11's "availability checks live in Offer") BEFORE dispatching a
	// pre-accept decline never previously possible. nil (the default, and
	// every pre-Task-2.3 test) disables the check entirely: Offer accepts
	// unconditionally, matching this package's behavior before this field
	// existed. bootCore sets this from svc.Registry() BEFORE registering any
	// role's listener — a role's own promotion to `started` (Task 2.1,
	// Registry.SetLifecycle) still has to land for Available to ever return
	// true; NewListener merely captures the *reference*, read live at each
	// Offer call, not a snapshot taken at construction time.
	Registry *core.Registry
	// ResourceLimitObserver is notified when a role's dispatch ends because
	// the component hit its OWN resource ceiling — the glossary's
	// "resource-limit" outcome (this bead, pg2-fm2gw; see
	// ResourceLimitObserver's own doc in listener.go for the full story and
	// its realization-gap note). nil (the default, and every pre-this-bead
	// test) disables the notification entirely — NewListener captures this
	// field the same way it captures Registry, so it must be set BEFORE any
	// NewListener call whose Offer should notify it. cmd/pg-router's bootCore
	// is the one production site that wires a live activityObserver in here.
	ResourceLimitObserver ResourceLimitObserver
	// HandlerFailureObserver is notified when a role's dispatch (Offer, via
	// workOne) returns a genuine, non-panic error the handler itself
	// reported (this bead, pg2-97539; see HandlerFailureObserver's own doc
	// in listener.go for the full story: the gap between the two existing
	// eventqueue.Observer failure classes, FailureClassDeclined and
	// FailureClassDispatchFail, that this hook closes). nil (the default,
	// and every pre-this-bead test) disables the notification entirely —
	// NewListener captures this field the same way it captures
	// ResourceLimitObserver, so it must be set BEFORE any NewListener call
	// whose Offer should notify it. cmd/pg-router's bootCore is the one
	// production site that wires a live metrics.Emitter in here.
	HandlerFailureObserver HandlerFailureObserver
	// lastTick is the per-source next-fire substrate ProduceTick threads into
	// discover.ProduceWithCadence (Task 1.3, discover.Cadence.LastTick): an
	// Orchestrator OUTLIVES a single Produce call across `run`'s whole ticker
	// loop, so it — not discover.Produce itself, which is stateless per call —
	// is the natural place to persist each source's last-fired time between
	// ticks. Starts nil (every source due on the very first tick, matching
	// discover.Produce's own pre-Task-1.3 behavior) and only ever grows via
	// ProduceTick's own merge of each pass's returned ProduceReport.LastTick.
	//
	// Safe with NO added locking even after cmd/pg-router's tick loop moved
	// from eventqueue.Queue.Dispatch to Queue.Kick (this package's design
	// doc, "decouple pg-router-core's tick loop from per-dispatch-pass
	// completion"; INV-LIFE-3): ProduceTick/LastTick calls stay SERIAL
	// relative to each other (same ticker, same goroutine) exactly as
	// before, and Kick's per-offer phase 3 (eventqueue's settleOfferLocked)
	// lives ENTIRELY inside *eventqueue.Queue's own state — it never reads
	// or writes this field, or any other Orchestrator field, at all; the
	// ONE Orchestrator-side call Kick's phase 2 makes on its own detached
	// goroutine (roleListener.Offer -> workOne/emitResult) touches o.Log/
	// o.Registry/o.resourceLimitObs, never o.lastTick — see
	// TestOrchestrator_LastTick_hasNoRaceWithConcurrentKickInFlightOffer.
	lastTick map[string]time.Time
}

func (o *Orchestrator) commander() query.Commander {
	if o.Cmd != nil {
		return o.Cmd
	}
	return query.OSCommander{}
}

// Gated reports whether dispatch is currently paused by an operator-managed
// gate file (PG_ROUTER_OPERATOR_PAUSED / PG_ROUTER_CICD_DOWN /
// PG_ROUTER_DISK_SPACE_LOW). A gated caller MUST NOT register listeners or
// run a producer tick — no sessions are created, so nothing needs tearing
// down either.
//
// operator_paused's and disk_space_low's own effects can each be switched
// off entirely from OUTSIDE pg-router (beads pg2-efbb0, pg2-hipf0): when
// o.Cfg.OperatorPausedDisable / o.Cfg.DiskSpaceLowDisable names a path that
// EXISTS, this method ignores that gate's own file state completely, as if
// it were never configured — see config.Config.OperatorPausedDisable's doc
// comment for the full design rationale and why this is deliberately a
// second, separate file rather than a sentinel value inside the gate's own
// file. cicd_down carries no such kill switch yet (tracked separately:
// bead pg2-8c7az).
func (o *Orchestrator) Gated() bool { return o.gated() }

// queryEnv builds the capability bag passed to each role's query.
func (o *Orchestrator) queryEnv() query.Env {
	return query.Env{RepoRoot: o.Cfg.RepoRoot, Cmd: o.commander()}
}

// ProduceTick fires the configured query set once against q — the
// discovery->enqueue producer side of the queue-as-universal-intermediary
// convergence (bead pg2-f3mcb.2): every event, pull or push, goes into the
// SAME durable queue a Listener bridge (NewListener) is registered on. This
// replaces the retired per-pass internal/eventbus producer→bus→lease drive.
//
// o.SourceFailureObserver rides along as discover.Produce's optional
// SourceFailureObserver (INV-FAIL-3, register gap R21 / bead pg2-00jpn): nil
// when unset (every construction site that does not assign it), which
// discover.WithSourceFailureObserver's own doc guarantees is a safe no-op.
//
// It returns discover.Produce's ProduceReport unchanged: source isolation
// (INV-FAIL-3, INV-EVT-1; ADR per Task 0.6) means a partial produce (one or
// more SourceErrors) is NOT itself a reason for the caller to abort — cmd/pr-
// pool's run/run-until-idle loops decide what a partial produce means for
// their own exit semantics. Only a real failure (ctx cancellation, a durable-
// queue Enqueue failure) still returns as this method's own error.
//
// It drives discover.ProduceWithCadence rather than the plain discover.Produce
// (Task 1.3): o.lastTick is this Orchestrator's own per-source next-fire
// history, carried across every tick of `run`'s ticker loop (a bare
// discover.Produce call is stateless per call and has nowhere to keep this),
// and o.Cfg.PollInterval is the pool-wide fallback period for a source whose
// own PeriodTrigger.Every is zero. Every source this pass actually fires
// (discover.ProduceReport.LastTick) is merged FORWARD into o.lastTick so the
// NEXT tick's cadence decision sees it — a source cad gated OFF this pass
// keeps its prior lastTick entry untouched.
func (o *Orchestrator) ProduceTick(ctx context.Context, q *eventqueue.Queue) (discover.ProduceReport, error) {
	rpt, err := discover.ProduceWithCadence(ctx, o.queryEnv(), o.Cfg.Queries, q, o.Bindings,
		discover.Cadence{LastTick: o.lastTick, PollInterval: o.Cfg.PollInterval},
		discover.WithSourceFailureObserver(o.SourceFailureObserver))
	for name, t := range rpt.LastTick {
		if o.lastTick == nil {
			o.lastTick = make(map[string]time.Time, len(rpt.LastTick))
		}
		o.lastTick[name] = t
	}
	return rpt, err
}

// LastTick returns a snapshot copy of this Orchestrator's own per-source
// merged-forward fire history (the lastTick field's doc above) — the real
// last time each source successfully fired across every tick this
// Orchestrator has driven, not merely the most recent ProduceTick call's own
// per-pass ProduceReport.LastTick (pg2-bzb8i). cmd/pg-router's
// sourceReportsFor reads this so a status reply's SourceReport.LastTick
// keeps showing a source's real last-fire time on a tick that cadence
// gating skipped for that source, rather than reverting to the zero value —
// a `tui` poll (every ~1s by default) lands on far more ticks than it does
// on the one tick that happens to actually fire a slower-cadenced source, so
// a per-pass-only view left the operator with no durable positive
// confirmation a source had ever run at all. A copy is returned (never the
// live map) so a caller cannot mutate this Orchestrator's own persisted
// history.
func (o *Orchestrator) LastTick() map[string]time.Time {
	out := make(map[string]time.Time, len(o.lastTick))
	for name, t := range o.lastTick {
		out[name] = t
	}
	return out
}

// RunOne dispatches a single self-contained event through one role. It is
// the single-event entry behind `pg-router run-role`: smoke-test one role
// against one event without running discovery.
//
// pg2-oju6w.15: RunOne no longer closes the session it launched itself —
// ccpool session lifecycle lives entirely in the registered handler
// participant's own process now, and this method has no way to observe or
// close one directly any more (the CC field is gone). Its caller
// (cmd/pg-router's run-role) instead brackets the call with its own
// Handler.PreShutdown, exactly mirroring run.go's own per-role preShutdown
// sweep at daemon/run-until-idle shutdown — the SAME mechanism, at run-role's
// own single-dispatch scope, superseding the inline
// sessionStateByID/closeUnlessNeedsInput pair this method used to run in its
// own defer.
//
// evt is the eventqueue.Event shape wireclient.Dispatch's Contract signature
// requires (Task 5.4's Interfaces block) — discover.DeriveContextFromQueueEvent
// derives the ephemeral DispatchContext from it directly, the same
// queue-event-in path the queue-driven roleListener.Offer already uses
// (internal/orchestrator/listener.go), rather than the old
// DeriveContext+ToQueueEvent pair built for a producer-emitted event.Event.
func (o *Orchestrator) RunOne(ctx context.Context, role roles.Role, evt eventqueue.Event) error {
	d := discover.DeriveContextFromQueueEvent(role, evt)
	reply, err := o.workOneWithID(ctx, d, evt)
	o.emitResult(ctx, d.Role, d.Item.ID, o.buildResult(d, reply, err), err)
	return err
}

// buildResult stores the handler-reported outcome as pg-router's own opaque
// dispatch record: reply.Outcome, verbatim, with no switch on its value.
// Before docket pg2-oju6w's Task 5.5 this method instead re-derived a
// created/closed/handed-back verdict itself — a created-bead snapshot diff
// (the retired snapshotIDs/createdByActor pair) plus a post-dispatch
// `beads.Status` read interpreted into closed/handed-back — duplicating
// internal/complete's own policy, which had already moved out of pr-pool
// with Task 5.2. Task 5.5 deletes that duplication for real: this package
// never again interprets created/closed/handed-back itself (ADR 0065's
// "Wire contract" section, closing register row INV-WORKFLOW-1/R20, bead
// pg2-ctqo2).
//
// A failed dispatch (dispatchErr != nil) never carries a meaningful reply
// (wireclient.Client.Dispatch's own contract: Reply is the zero value on
// error), and an accepted-but-outcome-less reply (reply.Outcome == "") has
// nothing to record either — in both cases this returns an empty
// report.Result. The dispatch error itself is already surfaced separately,
// by emitResult's own dispatchErr-logging path.
func (o *Orchestrator) buildResult(d discover.DispatchContext, reply wireclient.Reply, dispatchErr error) report.Result {
	if dispatchErr != nil || reply.Outcome == "" {
		return report.Result{}
	}
	return report.Result{Actions: []report.Action{
		{Verb: report.Verb(reply.Outcome), Refs: beadRefs([]string{d.Item.ID})},
	}}
}

// emitResult writes the dispatch report to the event log (when configured) and the
// human-readable drain summary; on the run-role smoke path (Log == nil) it prints to
// stdout so the operator still sees what happened.
//
// dispatchErr is the error workOne/workOneWithID actually returned (nil on
// success). Before pg2-an65v this was accepted by every caller only to decide
// branching (errors.Is(err, wireclient.ErrBusy) etc.) and then discarded — a
// launch failure (e.g. isolation.Ensure()/ccpool.Ensure() failing) surfaced
// here as nothing but the bare verb the executor applied (escalated/
// unclaimed/...), with the actual underlying error message never logged
// anywhere. Investigating pg2-an65v required reproducing the failure by hand
// because neither the CLI log nor events.jsonl recorded it. Now a non-nil
// dispatchErr is logged at "warn" (not "info") with its message, both on the
// slog line and (when configured) as the event log's "error" field, so a
// future launch-failure spree is diagnosable from the log alone.
func (o *Orchestrator) emitResult(_ context.Context, role roles.Role, beadID string, res report.Result, dispatchErr error) {
	if dispatchErr != nil {
		slog.Warn("dispatch result", "role", role.Name, "bead", beadID, "actions", res.Actions, "err", dispatchErr)
	} else {
		slog.Info("dispatch result", "role", role.Name, "bead", beadID, "actions", res.Actions)
	}
	if o.Log != nil {
		fields := res.Fields()
		fields["role"] = role.Name
		fields["bead"] = beadID
		level := "info"
		if dispatchErr != nil {
			level = "warn"
			fields["error"] = dispatchErr.Error()
		}
		if err := o.Log.Emit(level, "dispatch", "dispatch result", fields); err != nil {
			slog.Warn("event log emit failed", "err", err)
		}
		return
	}
	fmt.Printf("# dispatch %s %s: %v\n", role.Name, beadID, res.Actions)
}

func beadRefs(ids []string) []report.Ref {
	refs := make([]report.Ref, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, report.Ref{Type: "bead", ID: id})
	}
	return refs
}

// workOne dispatches a single item over the wire (Task 5.4). The session
// (ccpool roles, now the registered handler participant's own concern) is
// torn down by the pass-level teardownAll, not here (so strays are reaped
// uniformly).
func (o *Orchestrator) workOne(ctx context.Context, d discover.DispatchContext, qevt eventqueue.Event) (wireclient.Reply, error) {
	return o.workOneWithID(ctx, d, qevt)
}

// workOneWithID dispatches one item by sending it, as qevt, to whichever
// handler participant is registered for d.Role (internal/wireclient's
// HandlerClient.Dispatch) — replacing the retired
// executor.For(d.Role.Type).Dispatch(ctx, d, o.buildDeps(externalID)) call
// and its Deps seam bag entirely (Task 5.4's Binding decisions).
//
// The externalID parameter this method used to thread through to Deps for
// the OLD in-process ccpool session launch no longer has anything to feed —
// session naming is now the registered handler participant's own concern —
// so it is gone from this method's signature; RunOne, which still needs its
// OWN copy of that value for its deferred teardown Close, already computes
// it independently before calling this method (see RunOne).
//
// The returned wireclient.Reply (ID/Deferred/Outcome) is passed straight
// through to the caller (RunOne / roleListener.Offer), which hands it to
// buildResult unmodified — this method itself never inspects Outcome
// (docket pg2-oju6w's Task 5.5, ADR 0065's "Wire contract" section: the
// opaque string is buildResult's own concern, not this dispatch call's).
func (o *Orchestrator) workOneWithID(ctx context.Context, d discover.DispatchContext, qevt eventqueue.Event) (wireclient.Reply, error) {
	return o.wireClient().Dispatch(ctx, d.Role, qevt)
}

// wireClient returns o.Handler, or a client that always fails loudly (rather
// than panicking on a nil interface call) when no Handler was configured —
// a production caller (cmd/pg-router's bootCore/runRunRole, wired by bead
// pg2-g068j) always sets Handler now; every existing test that does not is
// exercising a path that no longer dispatches anything for real (see this
// task's own test rewrites).
func (o *Orchestrator) wireClient() wireclient.HandlerClient {
	if o.Handler != nil {
		return o.Handler
	}
	return unconfiguredHandler{}
}

type unconfiguredHandler struct{}

func (unconfiguredHandler) Dispatch(context.Context, roles.Role, eventqueue.Event) (wireclient.Reply, error) {
	return wireclient.Reply{}, fmt.Errorf("orchestrator: no Handler configured (internal/wireclient.HandlerClient)")
}

func (unconfiguredHandler) PostStartup(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, fmt.Errorf("orchestrator: no Handler configured (internal/wireclient.HandlerClient)")
}

func (unconfiguredHandler) PreShutdown(context.Context, roles.Role) (wireclient.Reply, error) {
	return wireclient.Reply{}, fmt.Errorf("orchestrator: no Handler configured (internal/wireclient.HandlerClient)")
}

func (o *Orchestrator) gated() bool {
	// The bead pg2-efbb0 kill switch: OperatorPausedDisable present ⇒
	// operator_paused's own file state (however it reads) is ignored for
	// this check — see Gated()'s doc comment above.
	if o.Cfg.OperatorPaused != "" && fileExists(o.Cfg.OperatorPaused) && !fileExists(o.Cfg.OperatorPausedDisable) {
		return true
	}
	if o.Cfg.CICDDown != "" && fileExists(o.Cfg.CICDDown) {
		return true
	}
	// The bead pg2-hipf0 kill switch: DiskSpaceLowDisable present ⇒
	// disk_space_low's own file state (however it reads) is ignored for
	// this check — see Gated()'s doc comment above, mirroring
	// OperatorPausedDisable's identical treatment above.
	if o.Cfg.DiskSpaceLow != "" && fileExists(o.Cfg.DiskSpaceLow) && !fileExists(o.Cfg.DiskSpaceLowDisable) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
