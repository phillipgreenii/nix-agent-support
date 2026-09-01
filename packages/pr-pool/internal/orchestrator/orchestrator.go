// Package orchestrator is pr-pool's mechanical drive loop: discover → per-role
// bounded drain → teardown-all. It owns no claude/tmux mechanics (ccpool does)
// and no LLM. Completion is bead-status-based; ccpool state is liveness only.
//
// Per-dispatch execution is selected by role.Type: ccpool roles run the
// ensure→send→wait path (with the budget watchdog when a finite budget is set);
// command roles run a configured executable. The pg2-c1vp single-terminal race
// between waitDone and the watchdog is unchanged — only its data source moved from
// the deleted RoleKind enum to the role's typed config.
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
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/executor"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/report"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
	"github.com/phillipgreenii/pr-pool/internal/watchdog"
	"github.com/phillipgreenii/pr-pool/internal/worktree"
)

type Orchestrator struct {
	CC          ccpool.Runner
	BD          beads.Runner
	Reg         roles.RoleSet
	Cfg         config.Config
	Cmd         query.Commander                            // command-query/role exec seam (default OSCommander)
	Log         *eventlog.Writer                           // may be nil (no-op); threaded onto Watchdog
	now         func() time.Time                           // clock seam (default time.Now)
	tick        func(context.Context, time.Duration) error // cancellable wait (default below)
	stamp       func() string                              // per-attempt id stamp seam (default below)
	usageReader usage.Reader                               // default usage.NewTranscriptReader()
	git         watchdog.GitRunner                         // watchdog's hard-stop reset/clean seam (nil ⇒ executor's OSGit{}); tests inject a fake so they never touch the real repo
	gitOpener   worktree.Opener                            // per-bead worktree creation seam (nil ⇒ executor's gitclient.New{} default); tests inject a fake so they never touch the real repo

	// SourceFailureObserver is notified of every pull-source query retry
	// ProduceTick's discover.Produce call makes (INV-FAIL-3, register gap
	// R21 / bead pg2-00jpn) — the metrics half of the log-only Warn line
	// discover.go's runAndEnqueue already writes at that same point. Left nil
	// (a safe no-op per discover.WithSourceFailureObserver's doc) by every
	// existing construction site that does not set it; cmd/pr-pool's bootCore
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
	// lastTick is the per-source next-fire substrate ProduceTick threads into
	// discover.ProduceWithCadence (Task 1.3, discover.Cadence.LastTick): an
	// Orchestrator OUTLIVES a single Produce call across `run`'s whole ticker
	// loop, so it — not discover.Produce itself, which is stateless per call —
	// is the natural place to persist each source's last-fired time between
	// ticks. Starts nil (every source due on the very first tick, matching
	// discover.Produce's own pre-Task-1.3 behavior) and only ever grows via
	// ProduceTick's own merge of each pass's returned ProduceReport.LastTick.
	lastTick map[string]time.Time
}

// attemptStamp returns a fresh per-attempt timestamp token. A unique stamp per
// dispatch yields a unique external_id, so ccpool always launches a brand-new
// session and never resumes a prior attempt (ADR 0015).
func (o *Orchestrator) attemptStamp() string {
	if o.stamp != nil {
		return o.stamp()
	}
	return time.Now().UTC().Format("20060102T150405")
}

func (o *Orchestrator) commander() query.Commander {
	if o.Cmd != nil {
		return o.Cmd
	}
	return query.OSCommander{}
}

// Gated reports whether dispatch is currently paused by an operator-managed
// gate file (PR_POOL_QUOTA_PAUSED / PR_POOL_CICD_DOWN). A gated caller MUST NOT
// register listeners or run a producer tick — no sessions are created, so
// nothing needs tearing down either.
func (o *Orchestrator) Gated() bool { return o.gated() }

// TeardownAll is teardownAll's exported form for cmd/pr-pool's run /
// run-until-idle entry points (a different package).
func (o *Orchestrator) TeardownAll(ctx context.Context) int { return o.teardownAll(ctx) }

// queryEnv builds the capability bag passed to each role's query.
func (o *Orchestrator) queryEnv() query.Env {
	return query.Env{BD: o.BD, RepoRoot: o.Cfg.RepoRoot, Cmd: o.commander()}
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

// RunOne dispatches a single self-contained EVENT through one role and then
// closes that one session (the drain's pass-level teardownAll is not involved).
// It is the single-bead entry behind `pr-pool run-role`: smoke-test one role
// against one bead without running discovery. Per the design's context-vs-event
// resolution (Q-meta), run-role accepts an EVENT (self-contained, replayable)
// and DERIVES the ephemeral DispatchContext here at dispatch. Unlike DrainOnce it
// does NOT consult the quota/CICD gates and does NOT reap stray pr-pool-*
// sessions — it is a manual, intentional single dispatch where the operator is
// in control.
func (o *Orchestrator) RunOne(ctx context.Context, role roles.Role, ev event.Event) error {
	d := discover.DeriveContext(role, ev)
	externalID := d.Role.ExternalID(o.Cfg.SessionPrefix, d.Item.ID, o.attemptStamp())
	defer func() {
		// Tear down the one session we launched — but PRESERVE it if it ended in
		// needs_input, exactly as drain's teardownAll does, so the operator can
		// `ccpool attach` (the needs_input alert in waitDone advertises that). Without
		// this, run-role would print "attach to continue" and then purge the session
		// out from under the operator (pg2-2yn2). State unknown (absent / list error)
		// falls through to a purge — closing a gone session is a harmless no-op.
		state, _ := o.sessionStateByID(ctx, externalID)
		o.closeUnlessNeedsInput(ctx, externalID, state)
	}()
	pre, preOK := o.snapshotIDs(ctx)
	res, err := o.workOneWithID(ctx, d, externalID)
	o.emitResult(ctx, d.Role, d.Item.ID, o.buildResult(ctx, d.Role, d, pre, preOK, res, err))
	return err
}

// snapshotIDs returns the set of all bead IDs (any status, incl. closed) and
// whether the read succeeded. A failed read returns (nil, false) so buildResult
// reports an indeterminate "created" rather than a false "none".
func (o *Orchestrator) snapshotIDs(ctx context.Context) (map[string]struct{}, bool) {
	issues, err := beads.List(ctx, o.BD, "--all")
	if err != nil {
		return nil, false
	}
	ids := make(map[string]struct{}, len(issues))
	for _, iss := range issues {
		ids[iss.ID] = struct{}{}
	}
	return ids, true
}

// buildResult assembles the structured dispatch report from observable signals,
// WITHOUT touching the single-terminal race code: the created marker from the
// snapshot diff (or indeterminate on a failed read), plus the outcome verb — on
// success the bead's final status (closed vs handed-back), on failure the verb
// the executor actually applied to the bead (execRes — pg2-kj7j).
func (o *Orchestrator) buildResult(ctx context.Context, role roles.Role, d discover.DispatchContext, pre map[string]struct{}, preOK bool, execRes report.Result, dispatchErr error) report.Result {
	var actions []report.Action
	post, lerr := beads.List(ctx, o.BD, "--all")
	switch {
	case !preOK || lerr != nil:
		actions = append(actions, report.Action{Verb: report.Indeterminate, Refs: beadRefs([]string{d.Item.ID})})
	default:
		if created := createdByActor(pre, post, actorOf(role)); len(created) > 0 {
			actions = append(actions, report.Action{Verb: report.Created, Refs: beadRefs(created)})
		}
	}
	if dispatchErr != nil {
		actions = append(actions, execRes.Actions...) // verb the executor actually applied (pg2-kj7j)
	} else {
		switch status, _ := beads.Status(ctx, o.BD, d.Item.ID); status {
		case "closed":
			actions = append(actions, report.Action{Verb: report.Closed, Refs: beadRefs([]string{d.Item.ID})})
		case "open":
			actions = append(actions, report.Action{Verb: report.HandedBack, Refs: beadRefs([]string{d.Item.ID})})
		}
	}
	return report.Result{Actions: actions}
}

// emitResult writes the dispatch report to the event log (when configured) and the
// human-readable drain summary; on the run-role smoke path (Log == nil) it prints to
// stdout so the operator still sees what happened.
func (o *Orchestrator) emitResult(_ context.Context, role roles.Role, beadID string, res report.Result) {
	slog.Info("dispatch result", "role", role.Name, "bead", beadID, "actions", res.Actions)
	if o.Log != nil {
		fields := res.Fields()
		fields["role"] = role.Name
		fields["bead"] = beadID
		if err := o.Log.Emit("info", "dispatch", "dispatch result", fields); err != nil {
			slog.Warn("event log emit failed", "err", err)
		}
		return
	}
	fmt.Printf("# dispatch %s %s: %v\n", role.Name, beadID, res.Actions)
}

// actorOf returns the BEADS_ACTOR a ccpool role's dispatch creates beads under, or
// "" for a command role (no actor ⇒ the created-marker diff finds nothing).
func actorOf(role roles.Role) string {
	if role.CCPool != nil {
		return role.CCPool.Actor
	}
	return ""
}

func beadRefs(ids []string) []report.Ref {
	refs := make([]report.Ref, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, report.Ref{Type: "bead", ID: id})
	}
	return refs
}

// createdByActor returns, sorted, the IDs of beads present in post but absent
// from the pre snapshot whose CreatedBy is actor. The snapshot diff drops every
// pre-existing bead; the actor filter drops beads created concurrently by anyone
// else (notably the pg-pr daemon's cycle/PR beads), so the result is exactly the
// beads this dispatch's actor created. Feeds the per-dispatch "created" action.
func createdByActor(pre map[string]struct{}, post []beads.Issue, actor string) []string {
	if actor == "" {
		return nil
	}
	var out []string
	for _, iss := range post {
		if _, existed := pre[iss.ID]; existed {
			continue
		}
		if iss.CreatedBy == actor {
			out = append(out, iss.ID)
		}
	}
	sort.Strings(out)
	return out
}

// buildDeps assembles the executor seam bag from the orchestrator's state. The
// per-attempt externalID is resolved ONCE by the caller and threaded in so the
// same id is reused for the deferred teardown Close (RunOne).
func (o *Orchestrator) buildDeps(externalID string) executor.Deps {
	return executor.Deps{
		CC: o.CC, BD: o.BD, Cmd: o.commander(), Log: o.Log, Cfg: o.Cfg,
		Now: o.now, Tick: o.tick, UsageReader: o.usageReader, ExternalID: externalID,
		Git:       o.git,       // nil ⇒ executor falls back to OSGit{} in production
		GitOpener: o.gitOpener, // nil ⇒ executor falls back to gitclient.New in production
	}
}

// workOne dispatches a single item. The session (ccpool roles) is torn down by the
// pass-level teardownAll, not here (so strays are reaped uniformly).
func (o *Orchestrator) workOne(ctx context.Context, d discover.DispatchContext) (report.Result, error) {
	externalID := d.Role.ExternalID(o.Cfg.SessionPrefix, d.Item.ID, o.attemptStamp())
	return o.workOneWithID(ctx, d, externalID)
}

// workOneWithID dispatches one item with the per-attempt external_id pinned by the
// caller, selecting the executor by role.Type.
func (o *Orchestrator) workOneWithID(ctx context.Context, d discover.DispatchContext, externalID string) (report.Result, error) {
	return executor.For(d.Role.Type).Dispatch(ctx, d, o.buildDeps(externalID))
}

// teardownAll closes every session whose name carries pr-pool's prefix — this
// pass's sessions AND strays left by a crashed prior run (the only self-healing
// behavior) — EXCEPT sessions in needs_input, which are preserved (left alive)
// so the operator can still `ccpool attach` after the pass (pg2-th35). Sessions
// outside the prefix are left untouched. Returns the number actually closed.
func (o *Orchestrator) teardownAll(ctx context.Context) (closed int) {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		slog.Warn("teardown list failed", "err", err)
		return 0
	}
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, o.Cfg.SessionPrefix) {
			continue
		}
		if o.closeUnlessNeedsInput(ctx, s.ExternalID, s.State) {
			closed++
		}
	}
	slog.Info("teardown", "closed", closed)
	return closed
}

// closeUnlessNeedsInput tears down one session UNLESS it is in needs_input, which is
// PRESERVED (left alive) so the operator can still `ccpool attach <external_id>` — the
// session is paused awaiting a human and the needs_input alert (waitDone) points them
// here (pg2-th35, pg2-2yn2). Shared by teardownAll's per-session loop and run-role's
// single-session teardown so the two paths can't drift. Returns true iff the session
// was actually closed (purged).
//
// ccpool's reaper carries the peer predicate (session.preservedForHuman), spanning its
// TTL and cap-eviction passes. Both realize ONE decision — ADR 0037 — which is itself
// this repo's realization of the deployment set's INV-CCPOOL-6; change one only by
// changing that ADR.
func (o *Orchestrator) closeUnlessNeedsInput(ctx context.Context, externalID string, state ccpool.SessionState) bool {
	if state == ccpool.StateNeedsInput {
		slog.Info("teardown preserving needs_input session for operator attach",
			"session", externalID, "attach", "ccpool attach "+externalID)
		return false
	}
	if err := o.CC.Close(ctx, externalID, true); err != nil {
		slog.Warn("teardown close failed", "session", externalID, "err", err)
		return false
	}
	return true
}

// sessionStateByID returns the current ccpool state of externalID, or ("", false) if
// it is absent from the list or the list errors. The caller treats an unknown state
// as "not needs_input" (so a gone/unknowable session is still purge-closed).
func (o *Orchestrator) sessionStateByID(ctx context.Context, externalID string) (ccpool.SessionState, bool) {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		return "", false
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.State, true
		}
	}
	return "", false
}

func (o *Orchestrator) gated() bool {
	if o.Cfg.QuotaPaused != "" && fileExists(o.Cfg.QuotaPaused) {
		return true
	}
	if o.Cfg.CICDDown != "" && fileExists(o.Cfg.CICDDown) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
