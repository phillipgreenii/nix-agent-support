// Package orchestrator is pr-pool's mechanical drive loop: discover → per-role
// bounded drain → teardown-all. It owns no claude/tmux mechanics (ccpool does)
// and no LLM. Completion is bead-status-based; ccpool state is liveness only.
//
// Per-dispatch execution is selected by role.Type: ccpool roles run the
// ensure→send→wait path (with the budget watchdog when a finite budget is set);
// command roles run a configured executable. The pg2-c1vp single-terminal race
// between waitDone and the watchdog is unchanged — only its data source moved from
// the deleted RoleKind enum to the role's typed config.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/beads"
	"github.com/phillipgreenii/pr-pool/internal/budget"
	"github.com/phillipgreenii/pr-pool/internal/ccpool"
	"github.com/phillipgreenii/pr-pool/internal/complete"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/discover"
	"github.com/phillipgreenii/pr-pool/internal/eventlog"
	"github.com/phillipgreenii/pr-pool/internal/prompt"
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

func (o *Orchestrator) reader() usage.Reader {
	if o.usageReader != nil {
		return o.usageReader
	}
	return usage.NewTranscriptReader()
}

func (o *Orchestrator) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

func (o *Orchestrator) commander() query.Commander {
	if o.Cmd != nil {
		return o.Cmd
	}
	return query.OSCommander{}
}

// waitPoll blocks for d or until ctx is cancelled; returns ctx.Err() if cancelled.
func (o *Orchestrator) waitPoll(ctx context.Context, d time.Duration) error {
	if o.tick != nil {
		return o.tick(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
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
	err := o.workOneWithID(ctx, d, externalID)
	o.emitResult(ctx, d.Role, d.Item.ID, o.buildResult(ctx, d.Role, d, pre, preOK, err))
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
		err := o.workOne(ctx, d)
		if err != nil {
			slog.Warn("bead flagged", "role", role.Name, "item", d.Item.ID, "err", err)
			flagged++
		} else {
			slog.Info("bead complete", "role", role.Name, "item", d.Item.ID)
			complete++
		}
		o.emitResult(ctx, role, d.Item.ID, o.buildResult(ctx, role, d, pre, preOK, err))
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
// success the bead's final status (closed vs handed-back), on failure the role's
// configured failure action (escalated/unclaimed).
func (o *Orchestrator) buildResult(ctx context.Context, role roles.Role, d discover.DispatchContext, pre map[string]struct{}, preOK bool, dispatchErr error) report.Result {
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
		if role.CCPool != nil {
			switch role.CCPool.OnFailure {
			case roles.AddHuman:
				actions = append(actions, report.Action{Verb: report.Escalated, Refs: beadRefs([]string{d.Item.ID})})
			case roles.Unclaim:
				actions = append(actions, report.Action{Verb: report.Unclaimed, Refs: beadRefs([]string{d.Item.ID})})
			}
		}
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

// workOne dispatches a single item. The session (ccpool roles) is torn down by the
// pass-level teardownAll, not here (so strays are reaped uniformly).
func (o *Orchestrator) workOne(ctx context.Context, d discover.DispatchContext) error {
	externalID := d.Role.ExternalID(o.Cfg.SessionPrefix, d.Item.ID, o.attemptStamp())
	return o.workOneWithID(ctx, d, externalID)
}

// workOneWithID dispatches one item with the per-attempt external_id pinned by the
// caller. It selects the executor by role.Type: command roles run an executable;
// every other role takes the ccpool ensure→send→wait path.
func (o *Orchestrator) workOneWithID(ctx context.Context, d discover.DispatchContext, externalID string) error {
	if d.Role.Type == "command" {
		return o.runCommand(ctx, d)
	}
	return o.runCCPool(ctx, d, externalID)
}

// runCCPool is the ccpool dispatch: Ensure a fresh per-attempt session, Send the
// rendered nudge (async), then wait for completion — racing the budget watchdog when
// the role carries a finite budget. The session is addressed by externalID; the
// stable DisplayName is passed only as ccpool --name.
func (o *Orchestrator) runCCPool(ctx context.Context, d discover.DispatchContext, externalID string) error {
	cc := d.Role.CCPool
	display := d.Role.DisplayName(o.Cfg.SessionPrefix, d.Item.ID)
	env := map[string]string{
		"BEADS_ACTOR":    cc.Actor,
		"BEADS_DIR":      o.Cfg.RepoRoot + "/.beads",
		"WORKSPACE_ROOT": o.Cfg.RepoRoot,
	}
	if err := o.CC.Ensure(ctx, externalID, display, o.Cfg.RepoRoot, env); err != nil {
		// Could not even create the session. The bead was never dispatched, so we
		// do not flag/unclaim it on a transient hiccup. But a bead that fails to
		// launch repeatedly is escalated (ADR 0015): stamp pool-launch-fail on the
		// first failure; on a subsequent failure (label already present) add human
		// so discovery stops retrying it (worker discovery excludes human).
		o.escalateLaunchFailure(ctx, d.Item.ID)
		return fmt.Errorf("ensure %s: %w", externalID, err)
	}
	// The bead was dispatched: clear any pool-launch-fail from a prior attempt so
	// the escalation counts CONSECUTIVE launch failures, not lifetime ones (ADR
	// 0015). Best-effort.
	_ = beads.RemoveLabel(ctx, o.BD, d.Item.ID, "pool-launch-fail")
	nudge := o.renderNudge(cc, d)
	if err := o.CC.Send(ctx, externalID, nudge, ccpool.ModeNoWait); err != nil {
		// J-dispatch-fail: apply the role's configured on_dispatch_fail action.
		if cc.OnDispatchFail == roles.DispatchUnclaim {
			_ = beads.Unclaim(ctx, o.BD, d.Item.ID)
		}
		return fmt.Errorf("send %s: %w", externalID, err)
	}
	// A finite budget => run the watchdog (it races waitDone); unlimited => no
	// watchdog, so no race and waitDone always owns the outcome.
	if budgetUnlimited(cc.Budget) {
		return o.waitDone(ctx, nil, d, externalID)
	}
	return o.workerWaitWithWatchdog(ctx, d, externalID)
}

// budgetUnlimited reports whether a budget imposes no finite bound (so no watchdog
// is needed and no budget prompt-line is appended).
func budgetUnlimited(b budget.Budget) bool {
	return b.Tokens.Unlimited() && b.Cost.Unlimited() && b.Time <= 0
}

// renderNudge builds the prompt sent to a ccpool session: the (non-editable) safety
// preamble when authorship_guard is set, then the role's rendered task prompt, then
// the budget prompt-line (empty when the budget is unlimited).
func (o *Orchestrator) renderNudge(cc *roles.CCPoolConfig, d discover.DispatchContext) string {
	pctx := prompt.Context{
		Item:        d.Item,
		WorktreeDir: o.Cfg.WorktreeDir,
		SkillMD:     cc.SkillMD,
		SelfLogin:   o.Cfg.SelfLogin,
		RepoRoot:    o.Cfg.RepoRoot,
	}
	body, err := prompt.Render(cc.Prompt, pctx)
	if err != nil {
		// A prompt that references an unknown var fails here; fall back to the raw
		// template source so the dispatch still carries the task (and log it).
		slog.Warn("prompt render failed; sending raw body", "role", d.Role.Name, "err", err)
		body = cc.PromptBody
	}
	var sb strings.Builder
	if cc.AuthorshipGuard {
		sb.WriteString(prompt.AuthorshipPreamble())
	}
	sb.WriteString(body)
	sb.WriteString(cc.Budget.PromptLine()) // "" when unlimited
	return sb.String()
}

// runCommand dispatches a command role: render its argv, run it once, success iff
// exit 0. No ccpool/watchdog. (No built-in command role exists; this path is
// exercised by explicit config.)
func (o *Orchestrator) runCommand(ctx context.Context, d discover.DispatchContext) error {
	argv, err := o.renderArgv(d.Role.Command.Argv, d)
	if err != nil {
		return fmt.Errorf("command role %q: render argv: %w", d.Role.Name, err)
	}
	if _, err := o.commander().Run(ctx, argv); err != nil {
		return fmt.Errorf("command role %q item %s: %w", d.Role.Name, d.Item.ID, err)
	}
	return nil
}

// renderArgv interpolates each argv element through the prompt template engine, so a
// command role can reference {{.BeadID}} etc. An element with no template actions
// renders to itself.
func (o *Orchestrator) renderArgv(argv []string, d discover.DispatchContext) ([]string, error) {
	pctx := prompt.Context{Item: d.Item, WorktreeDir: o.Cfg.WorktreeDir, SelfLogin: o.Cfg.SelfLogin, RepoRoot: o.Cfg.RepoRoot}
	out := make([]string, 0, len(argv))
	for _, a := range argv {
		t, err := prompt.Parse("argv", a)
		if err != nil {
			return nil, err
		}
		s, err := prompt.Render(t, pctx)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// escalateLaunchFailure escalates a bead that ccpool could not launch. First
// failure: add pool-launch-fail. Repeat failure (label already present): add
// human and stop retrying (worker discovery excludes the human label). Reads are
// best-effort — a bd hiccup here just means we retry next pass rather than
// escalate, which is the safe direction. (ADR 0015)
func (o *Orchestrator) escalateLaunchFailure(ctx context.Context, beadID string) {
	already, err := beads.HasLabel(ctx, o.BD, beadID, "pool-launch-fail")
	if err != nil {
		return // can't tell ⇒ do nothing this pass; the next launch failure retries
	}
	if already {
		_ = beads.AddHuman(ctx, o.BD, beadID)
		return
	}
	_ = beads.AddLabel(ctx, o.BD, beadID, "pool-launch-fail")
}

// workerWaitWithWatchdog runs waitDone and the budget watchdog concurrently.
// First to return a terminal result wins and cancels the other. The cancelled
// loser returns ctx.Err() (and skips its terminal action by design), so only
// the winner's outcome takes effect.
//
// The single-terminal guarantee is enforced by an atomic owner claim, NOT by
// cancel() timing: cancel() only fires after the winner returns, which is too
// late to stop a loser already mid terminal bead mutation. So whichever of
// {waitDone, watchdog} reaches its terminal bead mutation first claims ownership;
// the loser then performs NO bead mutation. Without this the watchdog's unclaim
// and waitDone's add-human could both fire (bead ends open AND human), or the
// watchdog's unclaim could be misread by waitDone as a successful hand-back
// (a budget hard-stop reported as success). (pg2-c1vp)
func (o *Orchestrator) workerWaitWithWatchdog(ctx context.Context, d discover.DispatchContext, name string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var owner atomic.Bool
	claimTerminal := func() bool { return owner.CompareAndSwap(false, true) }

	wd := &watchdog.Watchdog{
		Reader:        o.reader(),
		CC:            o.CC,
		BD:            o.BD,
		Log:           o.Log,
		Budget:        d.Role.CCPool.Budget,
		RepoRoot:      o.Cfg.RepoRoot,
		WorktreeDir:   o.Cfg.WorktreeDir,
		ReminderMsg:   o.Cfg.ReminderMsg,
		WrapUpMsg:     o.Cfg.WrapUpMsg,
		Git:           watchdog.OSGit{},
		Now:           o.now,
		Poll:          o.Cfg.PollInterval,
		ClaimTerminal: claimTerminal,
	}

	type res struct{ err error }
	done := make(chan res, 2) // buffered 2: both goroutines can send without blocking
	go func() { done <- res{o.waitDone(ctx, claimTerminal, d, name)} }()
	go func() { done <- res{wd.Run(ctx, name, d.Item.ID)} }()

	first := <-done // the winner's terminal result (the loser blocks until cancel)
	cancel()        // release the loser
	<-done          // drain the loser (it returns ctx.Err(), no terminal action)
	return first.err
}

// waitDone polls the bead status until DoneSignal fires (success) or MAX_WAIT
// elapses / the session dies (failure). On detecting death it re-reads the bead
// status once more before failing (a bead that closed in the same instant the
// session ended is a success). On failure it applies the role's OnFailure.
//
// claimTerminal arbitrates the single-terminal race with the budget watchdog:
// EVERY terminal outcome (success or failure) is gated through it, so exactly
// one of {waitDone, watchdog} owns the bead's final state. The loser performs no
// bead mutation and waits for the orchestrator to cancel ctx. A nil claimTerminal
// means no watchdog is racing (feedback dispatches / direct tests) — always own.
// (pg2-c1vp)
func (o *Orchestrator) waitDone(ctx context.Context, claimTerminal func() bool, d discover.DispatchContext, name string) error {
	completion := d.Role.CCPool.Completion
	deadline := o.clock().Add(o.Cfg.MaxWait)
	seenClaimed := false
	// won reports whether this loop owns the single terminal outcome.
	won := func() bool { return claimTerminal == nil || claimTerminal() }
	// lose is the loser's exit: take NO bead action, wait for the orchestrator to
	// cancel the shared ctx, then return ctx.Err() (so the winner's result, not
	// this one, is reported by workerWaitWithWatchdog).
	lose := func() error { <-ctx.Done(); return ctx.Err() }
	for {
		// If ctx is already cancelled the watchdog won (or we're shutting down):
		// do not trust a fresh status read as completion (the watchdog's unclaim
		// would look like an "open" hand-back), and run no failure action.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// transient bd hiccup => "" => not-done, keep polling (matches bash bead_status 2>/dev/null)
		status, _ := beads.Status(ctx, o.BD, d.Item.ID)
		if complete.DoneSignal(completion, status, seenClaimed) {
			if won() {
				return nil
			}
			return lose()
		}
		if completion == roles.CloseOrHandback && status == "in_progress" {
			seenClaimed = true
		}
		if !o.active(ctx, name) {
			// re-check-after-death: the bead may have closed as the session ended.
			status, _ = beads.Status(ctx, o.BD, d.Item.ID)
			if complete.DoneSignal(completion, status, seenClaimed) {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				return o.fail(ctx, d, "session exited before completing")
			}
			return lose()
		}
		if !o.clock().Before(deadline) {
			// final status check after the deadline.
			status, _ = beads.Status(ctx, o.BD, d.Item.ID)
			if complete.DoneSignal(completion, status, seenClaimed) {
				if won() {
					return nil
				}
				return lose()
			}
			if won() {
				return o.fail(ctx, d, fmt.Sprintf("not complete within %s", o.Cfg.MaxWait))
			}
			return lose()
		}
		// cancellable wait — on cancellation return ctx.Err() and DO NOT fail
		// (the watchdog won the race and owns the terminal outcome).
		if err := o.waitPoll(ctx, o.Cfg.PollInterval); err != nil {
			return err
		}
	}
}

func (o *Orchestrator) fail(ctx context.Context, d discover.DispatchContext, reason string) error {
	_ = complete.OnFailure(ctx, o.BD, d.Role.CCPool.OnFailure, d.Item.ID)
	return fmt.Errorf("%s: %s", d.Item.ID, reason)
}

// active reports whether it is still worth waiting on the session addressed by
// externalID. A session is active while it can still make progress:
// starting/ready/working, and needs_input (paused awaiting a human who may attach
// and move it along — still bounded by MaxWait). It is NOT active once the ccpool
// session reaches idle (Claude Stop: the turn ended and nothing re-nudges it) or
// errored (Claude StopFailure), or once it is absent from ccpool list. These are
// session FACTS, not work judgments — on !active the caller re-reads the BEAD to
// decide success vs failure (ADR 0015). A list error is treated as active (can't
// tell ⇒ keep waiting; MaxWait bounds us).
func (o *Orchestrator) active(ctx context.Context, externalID string) bool {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		return true // can't tell ⇒ assume active; the deadline still bounds us
	}
	for _, s := range sessions {
		if s.ExternalID == externalID {
			return s.Live && s.State != ccpool.StateErrored && s.State != ccpool.StateIdle
		}
	}
	return false // absent ⇒ gone
}

// teardownAll closes every session whose name carries pr-pool's prefix — this
// pass's sessions AND strays left by a crashed prior run (the only self-healing
// behavior). Sessions outside the prefix are left untouched.
func (o *Orchestrator) teardownAll(ctx context.Context) (closed int) {
	sessions, err := o.CC.List(ctx)
	if err != nil {
		slog.Warn("teardown list failed", "err", err)
		return 0
	}
	for _, s := range sessions {
		if strings.HasPrefix(s.ExternalID, o.Cfg.SessionPrefix) {
			if err := o.CC.Close(ctx, s.ExternalID, true); err != nil {
				slog.Warn("teardown close failed", "session", s.ExternalID, "err", err)
				continue
			}
			closed++
		}
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
