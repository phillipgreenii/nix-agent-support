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
// can still `ccpool attach` after the pass. A reaper TTL for preserved sessions
// was considered and deferred (pg2-th35).
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
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/executor"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/report"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"github.com/phillipgreenii/pr-pool/internal/usage"
	"github.com/phillipgreenii/pr-pool/internal/watchdog"
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
	git         watchdog.GitRunner                         // per-bead worktree git seam (nil ⇒ executor's OSGit{}); tests inject a fake so they never touch the real repo
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

// DrainOnce runs one pass: gate check, discover, drain each role up to its cap,
// teardown all pr-pool sessions. Returns nil even when individual beads fail
// (failures are recorded on the beads via OnFailure), matching the bash.
func (o *Orchestrator) DrainOnce(ctx context.Context) error {
	if o.gated() {
		slog.Info("gated; pausing without dispatch")
		return nil // NOTE: gated exit does NOT teardown (no sessions were created)
	}
	defer o.teardownAll(ctx) // always run teardown after the gated check, even on error
	dispatches, err := discover.Discover(ctx, o.queryEnv(), o.Reg)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	slog.Info("discover", "found", len(dispatches))
	var complete, flagged int
	for _, role := range o.Reg {
		c, f := o.drain(ctx, role, dispatches)
		complete += c
		flagged += f
	}
	slog.Info("done", "complete", complete, "flagged", flagged)
	return nil
}

// queryEnv builds the capability bag passed to each role's query.
func (o *Orchestrator) queryEnv() query.Env {
	return query.Env{BD: o.BD, RepoRoot: o.Cfg.RepoRoot, Cmd: o.commander()}
}

// RunOne dispatches a single DispatchContext through the full workOne path and then
// closes that one session (the drain's pass-level teardownAll is not involved). It is
// the single-bead entry behind `pr-pool run-role`: smoke-test one role against one
// bead without running discovery. Unlike DrainOnce it does NOT consult the quota/CICD
// gates and does NOT reap stray pr-pool-* sessions — it is a manual, intentional
// single dispatch where the operator is in control.
func (o *Orchestrator) RunOne(ctx context.Context, d discover.DispatchContext) error {
	externalID := d.Role.ExternalID(o.Cfg.SessionPrefix, d.Item.ID, o.attemptStamp())
	defer func() {
		if err := o.CC.Close(ctx, externalID, true); err != nil {
			slog.Warn("run-one teardown close failed", "session", externalID, "err", err)
		}
	}()
	pre, preOK := o.snapshotIDs(ctx)
	res, err := o.workOneWithID(ctx, d, externalID)
	o.emitResult(ctx, d.Role, d.Item.ID, o.buildResult(ctx, d.Role, d, pre, preOK, res, err))
	return err
}

func (o *Orchestrator) drain(ctx context.Context, role roles.Role, all []discover.DispatchContext) (complete, flagged int) {
	worked := 0
	for _, d := range all {
		if d.Role.Name != role.Name {
			continue
		}
		if worked >= role.Cap {
			break
		}
		slog.Info("dispatching", "role", role.Name, "item", d.Item.ID)
		pre, preOK := o.snapshotIDs(ctx) // bracket workOne so creations on BOTH success and failure paths are seen
		res, err := o.workOne(ctx, d)
		if err != nil {
			slog.Warn("bead flagged", "role", role.Name, "item", d.Item.ID, "err", err)
			flagged++
		} else {
			slog.Info("bead complete", "role", role.Name, "item", d.Item.ID)
			complete++
		}
		o.emitResult(ctx, role, d.Item.ID, o.buildResult(ctx, role, d, pre, preOK, res, err))
		worked++
	}
	return complete, flagged
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
		Git: o.git, // nil ⇒ executor falls back to OSGit{} in production
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
		// Preserve a needs_input session: it is paused awaiting a human who must
		// still be able to `ccpool attach <external_id>` after the pass ends.
		// Closing it here would kill the session before the operator can attach.
		// (pg2-th35; the alert that points the operator here fires in waitDone.)
		if s.State == ccpool.StateNeedsInput {
			slog.Info("teardown preserving needs_input session for operator attach",
				"session", s.ExternalID, "attach", "ccpool attach "+s.ExternalID)
			continue
		}
		if err := o.CC.Close(ctx, s.ExternalID, true); err != nil {
			slog.Warn("teardown close failed", "session", s.ExternalID, "err", err)
			continue
		}
		closed++
	}
	slog.Info("teardown", "closed", closed)
	return closed
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
