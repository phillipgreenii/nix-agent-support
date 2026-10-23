package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dispatchlib"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/handlerstate"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/rolefile"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
	"github.com/phillipgreenii/pg-router/conformance"
)

// runDispatch implements the `dispatch` INTF-HANDLER subcommand
// (interfaces.md's "Dispatch (core -> handler)"): reads the request as JSON
// on stdin, branches internally on this process's own configured role kind
// (ccpool or command — never exposed to pg-router's core, ADR 0065's "Open
// question resolved" section), runs the moved executor logic unchanged, and
// writes handler.dispatch-reply JSON to stdout with a coarse exit code.
//
// This packet's own scope stops at making both reply forms schema-legal;
// dispatch replies INLINE for every dispatch today (the moved
// ccpoolRun/commandRun logic runs to completion synchronously) — a
// long-running ccpool session correctly holding the call open for its
// duration is a legal choice under DEC-WIRE-1 ("a reply is either an inline
// result or a deferral"), not a violation of it. Actually detaching a
// long-running ccpool session from this call (so the process can reply
// deferred and exit while the session keeps running) is a daemonization
// design this packet does not build — it depends on how docket pg2-oju6w's
// Task 5.4 wire client invokes this subcommand, which is that task's own
// call to make.
func runDispatch(args []string) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// The daemon that spawned this handler MAY exit while the dispatch is still
	// running (a restart leaves ccpool-backed dispatches running, ADR 0085), which
	// closes the read end of our stdout pipe. Everything the dispatch does is
	// finished before the one reply write, so losing that write must not be a
	// signalled death: subscribe to SIGPIPE so the write fails with EPIPE, is
	// logged by wire.WriteReply, and the process exits normally. Notify, NOT
	// signal.Ignore: an ignored disposition is inherited across exec, so every
	// child this dispatch starts (ccpool, bd, and through it the claude session)
	// would run with SIGPIPE ignored; a Notify handler resets to the default.
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	defer signal.Stop(pipeSignals)
	roleConfig := fs.String("role-config", os.Getenv(rolefile.EnvRoleConfig), "path to this process's own role config JSON (or "+rolefile.EnvRoleConfig+")")
	cfgPath := fs.String("config", os.Getenv(rolefile.EnvConfig), "path to this process's own launch config JSON (or "+rolefile.EnvConfig+")")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "dispatch: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "dispatch takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch: read request from stdin:", err)
		return conformance.ExitError
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		wire.WriteErrorReply(os.Stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("handler.dispatch", v); err != nil {
		wire.WriteErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	// Check already proved raw decodes AND matches handler.dispatch's
	// schema, so this second Unmarshal (into the typed shape) cannot fail —
	// mirrors packages/pg-router/internal/core.DiscriminateReply's own
	// "validate first, decode second" ordering.
	var req wire.DispatchRequest
	_ = json.Unmarshal(raw, &req)

	role, err := rolefile.LoadRole(*roleConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitError
	}
	cfg, err := rolefile.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitError
	}
	if role.CCPool != nil {
		if err := cfg.ValidateQuietWindowOverride(role.CCPool.WorktreeQuietWindow); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch: role %s: %v\n", role.Name, err)
			return conformance.ExitError
		}
		if err := cfg.ValidateNeedsInputStall(role.CCPool.NeedsInputStall); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch: role %s: needsInputStall: %v\n", role.Name, err)
			return conformance.ExitError
		}
	}
	rolefile.OverlayBudgetThresholds(role, cfg)

	dctx := executor.DispatchContext{Role: role, Item: dispatchlib.ItemFromPayload(req.Event.Payload), EventID: req.Event.ID}
	// Origin availability gate (INV-CCH-10, bead pg2-4gi2c): decline BEFORE
	// dispatchlib.BuildDeps, reconciliation, or any executor step, so a decline makes no bd
	// or ccpool call and mutates no bead. orchestrator.gated() is untouched —
	// the gate lives here, at the handler boundary, because only the handler
	// knows which repo a dispatch runs in.
	// Every decline below writes one dispatch_decline record (INV-CCH-35) to the
	// handler's event log just before its reply.
	declineLog := handlerstate.NewEventLog(cfg)
	if reason, declined := originGate(context.Background(), newOriginProber(cfg), role, cfg, dctx.Item.ID); declined {
		wire.DeclineAndReply(os.Stdout, declineLog, role, dctx.Item.ID, wire.NeutralDecline(reason, ""))
		return conformance.ExitBusy
	}
	deps := dispatchlib.BuildDeps(cfg, role)
	// The event payload's metadata is a snapshot from enqueue time. Re-read the
	// bead's CURRENT metadata so the rendered prompt (e.g. a review's pinned
	// head_sha) reflects the bead as it is at dispatch time, not as it was when
	// the event was queued before a head advance reopened the bead (pg2-1pt7r).
	var bead *beads.Issue
	dctx.Item, bead = executor.RefreshItemIssue(context.Background(), deps.BD, dctx.Item)
	// Stamp a fresh per-attempt ExternalID here — the call the old monolithic
	// internal/orchestrator made before invoking the ccpool executor
	// in-process, and which the Phase 5 participant extraction (docket
	// pg2-oju6w) never replaced (pg2-nmpvs): executor.Deps.ExternalID's own
	// doc comment says "resolved ONCE by the orchestrator", and this dispatch
	// subcommand IS that orchestrator now. Role.ExternalID's own doc comment
	// ("the stamp makes it unique per attempt") is why this is stamped fresh
	// per call rather than cached on cfg/role — a redelivered/retried dispatch
	// of the same (role, bead) must not reuse a prior attempt's id (the
	// "reuse existing session" path in internal/executor/ccpool.go's
	// absorbDuplicate re-derives its own id from the EXISTING session's own
	// ExternalID instead, so it never reads this fresh stamp). The clock is
	// deps.Now — the SAME seam ccpoolRun's own wait/watchdog logic reads
	// (internal/executor/executor.go) — rather than an independent time.Now()
	// call, so a caller that fakes Deps.Now for a deterministic test gets one
	// consistent clock, not two.
	deps.ExternalID = dispatchlib.StampExternalID(role, cfg.SessionPrefix, dctx.Item.ID, deps.Now)
	ctx := context.Background()
	// Opportunistic reconciliation (pg2-hrppg): dispatch is this binary's own
	// invocation that actually recurs frequently in production. query.go's
	// own `query` subcommand was the first place this landed, but the live
	// deployment never wires a [[query]] source at this handler (nothing
	// sets --query-config there), so that hook never fires — this dispatch
	// path, run once per queued item for every enabled ccpool role
	// (review/feedback/worker), is the one that does. Scoped to
	// role.CCPool != nil (mirrors rolefile.OverlayBudgetThresholds's own guard just
	// above): a "command" role has no ccpool sessions to reconcile, and
	// gating on it keeps a command-role dispatch (TestLiveDispatch_command)
	// from touching ccpool/bd at all, exactly as it does today. Reuses
	// deps.CC/deps.BD (dispatchlib.BuildDeps, above) rather than constructing a second
	// pair of runners. Best effort: logged, never turned into a dispatch
	// failure — the actual dispatch below is this subcommand's primary job.
	if role.CCPool != nil {
		if closed := reconcileClosedBeadSessions(ctx, deps.CC, worktree.GitOpener, deps.BD, cfg.SessionPrefix, cfg.RepoRoot, cfg.WorktreeDir, newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil)); closed > 0 {
			slog.Info("dispatch: reconciled sessions with closed beads", "closed", closed)
		}
		// Default-pool pass (pg2-wqi3e): a role with its own pool (or a process
		// launched against a non-default CCPOOL_POOL) never sweeps ccpool's shared
		// DEFAULT pool, where a not-live needs_input row can sit forever (ccpool's
		// reaper spares needs_input; the orphan reconcile needs a lease). GUARDED:
		// only a long-idle row whose worktree is absent or clean and pushed is
		// closed (stalesession.go). Skipped when the role's own pool already IS the
		// default pool, which the unguarded pass above has just swept. Best effort.
		if role.CCPool.PoolDir != "" || os.Getenv("CCPOOL_POOL") != "" {
			if closed := reconcileDefaultPool(ctx, cfg); closed > 0 {
				slog.Info("dispatch: reconciled stale default-pool sessions with closed beads", "closed", closed)
			}
		}
		// Orphan reconcile (pg2-g2u9m, INV-CCH-18): reclaim or budget-stop the
		// sessions of THIS role whose handler died (expired supervision lease),
		// scoped to the role's own pool runner (deps.CC) and run before the
		// capacity check inside the executor, so a reclaimed orphan frees its slot
		// for this very dispatch. Deliberately NOT called from query.go's default-pool
		// reconcile, which has no role. Best effort: never a dispatch failure.
		if reclaimed, stopped := reconcileOrphanSessions(ctx, role, deps, orphanEnv{
			open:    worktree.GitOpener,
			quiet:   newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil),
			lockDir: deps.LockDir,
		}); reclaimed+stopped > 0 {
			slog.Info("dispatch: reconciled orphaned sessions", "reclaimed", reclaimed, "hard_stopped", stopped)
		}
		// Worktree-keyed sweep (pg2-ganjb, INV-CCH-19): reclaim per-bead worktrees
		// no session row can lead to (a handler killed between worktree creation and
		// session creation, or a row closed before anyone reclaimed its worktree).
		// After the orphan reconcile, so a worktree that reconcile just released is
		// not double-handled; best effort, never a dispatch failure.
		if swept := sweepLeakedWorktrees(ctx, role, deps, orphanEnv{
			open:    worktree.GitOpener,
			cwds:    processCWDs,
			lockDir: deps.LockDir,
		}); swept > 0 {
			slog.Info("dispatch: reclaimed leaked worktrees", "reclaimed", swept)
		}
		// Lock-file GC (pg2-bjhoq): bound the handler lock directory. After the
		// worktree sweep, so the locks it just used count as freshly used; best
		// effort, never a dispatch failure.
		if n := sweepStaleLocks(deps); n > 0 {
			slog.Info("dispatch: removed stale lock files", "removed", n)
		}
	}
	// Zero-model-cost precheck (INV-CCH-22, beads pg2-5x29j and pg2-nk6th.3): a
	// review dispatch whose bead is closed, whose PR is merged, or whose pending
	// review already holds content for the head, and a "ready" role's dispatch
	// whose bead is no longer open, unclaimed, groomed, undeferred and unblocked,
	// and a "triage" role's dispatch whose bead no longer matches its discovery
	// query (open, escalated, not human, undeferred, unblocked), launch no
	// session. Read-only; fails open, except that a connector answer of class
	// unavailable declines retryably (busy, reason connector-unavailable).
	// After the reconciles above so their housekeeping still runs on a decline;
	// the decline is the same pre-accept busy reply as every other, carrying the
	// skip's own reason so pg-router core's declined metric counts it per reason.
	if skip, skipped := executor.Precheck(ctx, dctx, deps, bead); skipped {
		decline := wire.NeutralDecline(skip.Reason, skip.Detail)
		if skip.Retryable {
			// A retryable skip (the review precheck's connector-unavailable) proves
			// nothing moot: it is routine backpressure the router re-offers.
			decline.Kind = wire.DeclineKindBusy
		}
		wire.DeclineAndReply(os.Stdout, declineLog, role, dctx.Item.ID, decline)
		return conformance.ExitBusy
	}
	result, err := executor.For(role.Type).Dispatch(ctx, dctx, deps)
	if decline, busy := wire.BusyDeclineFor(err); busy {
		// Pre-accept busy decline (INV-CONC-1, DEC-WIRE-1 exit 9): the core's
		// listener re-offers the event with backoff; the activity ring
		// records it as "declined", distinct from "dispatch_failed" — for
		// EITHER reason, identically (INV-FAIL-1: every DeclineReason
		// re-offers alike; bead pg2-j4uwg does not change this).
		//
		// The reply body is OPTIONAL on this exit (DEC-WIRE-1 / interfaces.md's
		// "Coarse outcome, rich reply": "a reply body, where the participant
		// supplies one, still carries which case applies") — before bead
		// pg2-j4uwg this handler always omitted it (an empty body is equally
		// legal). It now writes one carrying reason, a short, stable
		// classification tag, so pg-router core's declined metric/status
		// breakdown can tell ErrPoolCapacityUnknown's "genuinely worth
		// investigating" apart from ErrPoolAtCapacity's "healthy, expected
		// backpressure" — confirmed live 2026-09-22 as otherwise
		// indistinguishable without manually cross-referencing ccpool
		// capacity by hand.
		wire.DeclineAndReply(os.Stdout, declineLog, role, dctx.Item.ID, decline)
		return conformance.ExitBusy
	}
	if err != nil {
		wire.WriteErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	// outcome is an opaque STRING on the wire (packages/pg-router's
	// docket pg2-oju6w Task 5.4 retypes handler.dispatch-reply.schema.json's
	// outcome property from object -> string, matching wireclient.
	// Reply.Outcome's Go type — encoding/json cannot unmarshal an object
	// into a string field). result.Fields() still carries the full
	// structured actions/refs shape (report.go's own doc: "an opaque
	// string [object] the core stores"); JSON-encoding it into a string is
	// this module's own minimal fix to stay wire-legal without losing any
	// information the core never interpreted anyway.
	//
	// This is also this module's own INV-CCH-3 obligation (docs/behavior/
	// invariants.md): a post-accept outcome — retryable/resource-limit/
	// critical, mapped by ccpool.go's waitFailureResult into the Unclaimed/
	// Escalated verbs result.Fields() carries here — crosses back to the core
	// as nothing but this one opaque completion-outcome string, never as a
	// distinguishable failure class.
	outcomeJSON, err := json.Marshal(result.Fields())
	if err != nil {
		wire.WriteErrorReply(os.Stdout, "encode outcome: "+err.Error())
		return conformance.ExitError
	}
	wire.WriteReply(os.Stdout, wire.OutcomeReply(req.ID, string(outcomeJSON), result.StartedAt))
	return conformance.ExitOK
}
