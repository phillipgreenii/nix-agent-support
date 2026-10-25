package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/dispatchlib"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/handlerstate"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/rolefile"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/wire"
	"github.com/phillipgreenii/pg-router/conformance"
)

// runDispatch implements the `dispatch` INTF-HANDLER subcommand for a ccpool
// role: the request's role object names the role and its [role.config], which
// is decoded strictly on every call; the dispatch then runs the same sequence
// pg-router-ccpool-handler runs for a ccpool role today (origin gate, bead
// refresh, per-attempt external id, precheck, ccpool executor) and maps its
// outcome to the reply and exit code exactly as that binary does: success is
// exit 0, a pre-accept decline is exit 9 with the reason and kind in the reply
// body, any other failure an error reply and exit 1.
//
// Handler-wide settings (RepoRoot, WorktreeDir, SessionPrefix, the pool-wide
// budget thresholds, the origin probe and the event log directory) come from the
// launch config JSON: --config, or PG_ROUTER_CCPOOL_HANDLER_CONFIG. The env var
// name is shared so one wrapper and one config file serve every adapter. The role
// is never read from a file or an env var: it comes from stdin.
//
// The opportunistic reconcile and sweep passes the dispatch runs before the
// executor (closed-bead sessions, orphans, leaked worktrees, stale locks) run
// here too (runSweeps), after the per-attempt external id and before the
// precheck.
func runDispatch(args []string) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", os.Getenv(rolefile.EnvConfig), "path to this process's own launch config JSON (or "+rolefile.EnvConfig+")")
	if exit, done := parseFlags("dispatch", fs, args); done {
		return exit
	}
	// The daemon that spawned this adapter MAY exit while the dispatch is still
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
	// No TERM/INT handler: like the old binary, a signal ends the process and the
	// ccpool session it launched keeps running to be adopted by the next dispatch
	// (INV-CCH-30).
	return serveDispatch(context.Background(), os.Stdin, os.Stdout, *cfgPath, dispatchlib.BuildDeps)
}

// serveDispatch is runDispatch's testable core: ctx is the dispatch context
// (cancel it to cancel the dispatch, which never declines) and mkDeps builds the
// executor seams, so a test keeps bd and ccpool off the machine.
func serveDispatch(ctx context.Context, stdin io.Reader, stdout io.Writer, cfgPath string, mkDeps func(config.Config, roles.Role) executor.Deps) int {
	raw, ro, exit, ok := readRequest("dispatch", "handler.dispatch", stdin, stdout)
	if !ok {
		return exit
	}
	// Check already proved raw decodes and matches handler.dispatch's schema, so
	// this second Unmarshal (into the typed shape) cannot fail.
	var req wire.DispatchRequest
	_ = json.Unmarshal(raw, &req)

	role, cfg, err := loadRoleAndConfig(ro, cfgPath)
	if err != nil {
		return failDispatch(stdout, err.Error())
	}

	dctx := executor.DispatchContext{Role: role, Item: dispatchlib.ItemFromPayload(req.Event.Payload), EventID: req.Event.ID}
	// Origin availability gate (INV-CCH-10, bead pg2-4gi2c): decline BEFORE
	// building deps, refreshing the bead or any executor step, so a decline makes
	// no bd or ccpool call and mutates no bead. Every decline below writes one
	// dispatch_decline record (INV-CCH-35) to the handler's event log just before
	// its reply.
	declineLog := handlerstate.NewEventLog(cfg)
	if reason, declined := originGate(ctx, newOriginProber(cfg), role, cfg, dctx.Item.ID); declined {
		wire.DeclineAndReply(stdout, declineLog, role, dctx.Item.ID, wire.NeutralDecline(reason, ""))
		return conformance.ExitBusy
	}
	deps := mkDeps(cfg, role)
	// The event payload's metadata is a snapshot from enqueue time. Re-read the
	// bead's CURRENT metadata so the rendered prompt (e.g. a review's pinned
	// head_sha) reflects the bead as it is at dispatch time (pg2-1pt7r).
	var bead *beads.Issue
	dctx.Item, bead = executor.RefreshItemIssue(ctx, deps.BD, dctx.Item)
	// A fresh per-attempt ExternalID (pg2-nmpvs): a redelivered dispatch of the
	// same (role, bead) must not reuse a prior attempt's id. The clock is deps.Now,
	// the seam the executor's own wait logic reads. The id format is the shared
	// library's, so a session this binary starts and one the old binary starts
	// are adopted alike (ADR 0085).
	deps.ExternalID = dispatchlib.StampExternalID(role, cfg.SessionPrefix, dctx.Item.ID, deps.Now)
	// The housekeeping passes the old binary's dispatch runs before the executor
	// (closed-bead sessions, default pool, orphans, leaked worktrees, stale locks),
	// in the same place and order, so a reclaimed orphan frees its slot for this
	// very dispatch. Best effort, never a dispatch failure; postStartup runs the
	// same passes.
	runSweeps(ctx, "dispatch", role, cfg, deps)

	// Zero-model-cost precheck (INV-CCH-22): a dispatch whose bead no longer
	// warrants a session launches none. Read-only; fails open, except that a
	// connector answer of class unavailable declines retryably.
	if skip, skipped := executor.Precheck(ctx, dctx, deps, bead); skipped {
		decline := wire.NeutralDecline(skip.Reason, skip.Detail)
		if skip.Retryable {
			// A retryable skip proves nothing moot: it is routine backpressure the
			// router re-offers.
			decline.Kind = wire.DeclineKindBusy
		}
		wire.DeclineAndReply(stdout, declineLog, role, dctx.Item.ID, decline)
		return conformance.ExitBusy
	}
	result, err := executor.NewCCPool().Dispatch(ctx, dctx, deps)
	if decline, busy := wire.BusyDeclineFor(err); busy {
		// Pre-accept busy decline (INV-CONC-1, DEC-WIRE-1 exit 9): the core re-offers
		// the event with backoff. The reason and kind are a handler-to-alert
		// contract (bead pg2-j4uwg) and stay as they are.
		wire.DeclineAndReply(stdout, declineLog, role, dctx.Item.ID, decline)
		return conformance.ExitBusy
	}
	if err != nil {
		wire.WriteErrorReply(stdout, err.Error())
		return conformance.ExitError
	}
	// outcome is an opaque STRING on the wire; result.Fields() is the structured
	// shape the core stores uninterpreted, JSON-encoded into it (INV-CCH-3).
	outcomeJSON, err := json.Marshal(result.Fields())
	if err != nil {
		wire.WriteErrorReply(stdout, "encode outcome: "+err.Error())
		return conformance.ExitError
	}
	wire.WriteReply(stdout, wire.OutcomeReply(req.ID, string(outcomeJSON), result.StartedAt))
	return conformance.ExitOK
}

// failDispatch writes msg to stderr and as an error reply, and returns
// conformance.ExitError.
func failDispatch(stdout io.Writer, msg string) int {
	fmt.Fprintln(os.Stderr, "dispatch:", msg)
	wire.WriteErrorReply(stdout, msg)
	return conformance.ExitError
}
