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
	"path/filepath"
	"syscall"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/eventlog"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/executor"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/item"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/originprobe"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/sessionlock"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
)

// itemFromPayload reconstructs an item.Item from a dispatch event's opaque
// payload object — this module's own local replacement for
// packages/pg-router/internal/discover.ItemFromPayload, which is
// unreachable from here (Go's internal-package visibility rule) and, as of
// docket pg2-oju6w's Task 5.6, deleted entirely: the core-side
// discover.ToQueueEvent now writes the item's fields directly at Payload's
// top level (no wrapping key), and this function reads that same flat
// shape — the SAME field names ItemFromPayload used to read, just no
// longer nested under an "item" key (Task 5.6's "same shape, different
// side" framing; docs/adr/0065's Addendum). A payload missing the expected
// fields yields a zero item.Item rather than an error, matching
// ItemFromPayload's own "absent path is a non-match, not an error" posture.
func itemFromPayload(payload map[string]any) item.Item {
	var it item.Item
	if v, ok := payload["id"].(string); ok {
		it.ID = v
	}
	if v, ok := payload["type"].(string); ok {
		it.Type = v
	}
	if v, ok := payload["title"].(string); ok {
		it.Title = v
	}
	if v, ok := payload["metadata"].(map[string]any); ok {
		it.Metadata = v
	}
	return it
}

// dispatchRequest/dispatchEvent decode the handler.dispatch wire message
// (packages/pg-router/schemas/handler.dispatch.schema.json /
// .../event.schema.json).
type dispatchRequest struct {
	SchemaVersion string        `json:"schemaVersion"`
	ID            string        `json:"id"`
	Event         dispatchEvent `json:"event"`
}

type dispatchEvent struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

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
	// logged by writeReply, and the process exits normally. Notify, NOT
	// signal.Ignore: an ignored disposition is inherited across exec, so every
	// child this dispatch starts (ccpool, bd, and through it the claude session)
	// would run with SIGPIPE ignored; a Notify handler resets to the default.
	pipeSignals := make(chan os.Signal, 1)
	signal.Notify(pipeSignals, syscall.SIGPIPE)
	defer signal.Stop(pipeSignals)
	roleConfig := fs.String("role-config", os.Getenv(envRoleConfig), "path to this process's own role config JSON (or "+envRoleConfig+")")
	cfgPath := fs.String("config", os.Getenv(envConfig), "path to this process's own launch config JSON (or "+envConfig+")")
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
		writeErrorReply(os.Stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("handler.dispatch", v); err != nil {
		writeErrorReply(os.Stdout, err.Error())
		return conformance.ExitError
	}
	// Check already proved raw decodes AND matches handler.dispatch's
	// schema, so this second Unmarshal (into the typed shape) cannot fail —
	// mirrors packages/pg-router/internal/core.DiscriminateReply's own
	// "validate first, decode second" ordering.
	var req dispatchRequest
	_ = json.Unmarshal(raw, &req)

	role, err := loadRole(*roleConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitError
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return conformance.ExitError
	}
	overlayBudgetThresholds(role, cfg)

	dctx := executor.DispatchContext{Role: role, Item: itemFromPayload(req.Event.Payload), EventID: req.Event.ID}
	// Origin availability gate (INV-CCH-10, bead pg2-4gi2c): decline BEFORE
	// buildDeps, reconciliation, or any executor step, so a decline makes no bd
	// or ccpool call and mutates no bead. orchestrator.gated() is untouched —
	// the gate lives here, at the handler boundary, because only the handler
	// knows which repo a dispatch runs in.
	if reason, declined := originGate(context.Background(), newOriginProber(cfg), role, cfg, dctx.Item.ID); declined {
		writeBusyReply(os.Stdout, reason)
		return conformance.ExitBusy
	}
	deps := buildDeps(cfg, role)
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
	deps.ExternalID = stampExternalID(role, cfg.SessionPrefix, dctx.Item.ID, deps.Now)
	ctx := context.Background()
	// Opportunistic reconciliation (pg2-hrppg): dispatch is this binary's own
	// invocation that actually recurs frequently in production. query.go's
	// own `query` subcommand was the first place this landed, but the live
	// deployment never wires a [[query]] source at this handler (nothing
	// sets --query-config there), so that hook never fires — this dispatch
	// path, run once per queued item for every enabled ccpool role
	// (review/feedback/worker), is the one that does. Scoped to
	// role.CCPool != nil (mirrors overlayBudgetThresholds's own guard just
	// above): a "command" role has no ccpool sessions to reconcile, and
	// gating on it keeps a command-role dispatch (TestLiveDispatch_command)
	// from touching ccpool/bd at all, exactly as it does today. Reuses
	// deps.CC/deps.BD (buildDeps, above) rather than constructing a second
	// pair of runners. Best effort: logged, never turned into a dispatch
	// failure — the actual dispatch below is this subcommand's primary job.
	if role.CCPool != nil {
		if closed := reconcileClosedBeadSessions(ctx, deps.CC, gitWorktreeOpener, deps.BD, cfg.SessionPrefix, cfg.RepoRoot, cfg.WorktreeDir, newTranscriptQuietCheck(cfg.WorktreeQuietWindow, nil, nil)); closed > 0 {
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
			open:    gitWorktreeOpener,
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
			open:    gitWorktreeOpener,
			cwds:    processCWDs,
			lockDir: deps.LockDir,
		}); swept > 0 {
			slog.Info("dispatch: reclaimed leaked worktrees", "reclaimed", swept)
		}
	}
	// Zero-model-cost precheck (INV-CCH-22, bead pg2-5x29j): a review dispatch
	// whose bead is closed, whose PR is merged, or whose pending review already
	// holds content for the head launches no session. Read-only; fails open.
	// After the reconciles above so their housekeeping still runs on a decline;
	// the decline is the same pre-accept busy reply as every other, carrying the
	// skip's own reason so pg-router core's declined metric counts it per reason.
	if skip, skipped := executor.PrecheckReview(ctx, dctx, deps, bead); skipped {
		writeBusyReply(os.Stdout, skip.Reason)
		return conformance.ExitBusy
	}
	result, err := executor.For(role.Type).Dispatch(ctx, dctx, deps)
	if reason, busy := busyDeclineReason(err); busy {
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
		writeBusyReply(os.Stdout, reason)
		return conformance.ExitBusy
	}
	if err != nil {
		writeErrorReply(os.Stdout, err.Error())
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
		writeErrorReply(os.Stdout, "encode outcome: "+err.Error())
		return conformance.ExitError
	}
	writeReply(os.Stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       string(outcomeJSON),
	})
	return conformance.ExitOK
}

// externalIDStampLayout is the per-attempt ccpool ExternalID's timestamp
// layout. This module's own internal/dtest.TestStamp fixture
// ("20260616T010203") already reserves the second-precision "20060102T150405"
// layout as this codebase's stamp-generation convention (matching
// internal/ccpool/cli_test.go's "pg-router-worker-zr-1-20260616T010203"
// fixture) — reused here rather than inventing an unrelated format — with
// nanosecond precision appended so two attempts stamped within the same
// wall-clock second still mint distinct ids; a bare second-resolution stamp
// cannot promise that on its own.
const externalIDStampLayout = "20060102T150405.000000000"

// stampExternalID builds role's per-attempt ccpool ExternalID
// (roles.Role.ExternalID's own documented <prefix><name>-<beadid>-<stamp>
// shape), stamping it fresh from now on every call (pg2-nmpvs). now nil
// (buildDeps never sets Deps.Now in production) defaults to time.Now,
// mirroring executor.Deps.Now's own "clock seam; nil ⇒ time.Now" doc
// comment — Deps' own clock() method implementing that default is
// unexported, so this package (main) cannot call it directly and
// re-implements the same nil-check here instead of inventing a second,
// independent clock seam.
func stampExternalID(role roles.Role, prefix, beadID string, now func() time.Time) string {
	if now == nil {
		now = time.Now
	}
	return role.ExternalID(prefix, beadID, now().UTC().Format(externalIDStampLayout))
}

// buildDeps wires the executor.Deps seam bag for production use from cfg and
// role: a real ccpool.CLIRunner and beads.CLIRunner, everything else left at
// its own nil-safe default (Deps.git/gitOpener/clock/reader/commander/
// waitPoll — internal/executor/executor.go).
//
// role.CCPool.PoolDir (bead pg2-mr0sl), when this role sets one, scopes the
// ccpool.CLIRunner to that role's own dedicated pool via
// ccpool.NewCLIRunnerForPool — every ccpool call this dispatch call makes
// (the admission-gate Capacity check, Ensure/Send, and every List/Close call
// the wait-loop and worktree cleanup make) then targets that pool, not
// whatever CCPOOL_POOL this process inherited. This is scoped to buildDeps'
// one production call site (runDispatch) deliberately: preshutdown.go's
// once-per-process-lifetime sweep and query.go's own reconciliation call
// still build a plain ccpool.NewCLIRunner(cfg) — both act once across every
// enabled role sharing this process, with no single role to scope to (see
// each of those call sites' own doc comments on that pre-existing,
// unchanged-by-this-bead posture).
func buildDeps(cfg config.Config, role roles.Role) executor.Deps {
	cc := ccpool.NewCLIRunner(cfg)
	if role.CCPool != nil && role.CCPool.PoolDir != "" {
		cc = ccpool.NewCLIRunnerForPool(cfg, role.CCPool.PoolDir)
	}
	// Every bd call for this role's items resolves against the tracker the
	// items live in: the role's own BeadsDir when set (pg2-2grpj, e.g. the pg2
	// tracker for the escalation triager), else the monorepo at cfg.RepoRoot.
	bdDir := cfg.RepoRoot
	if role.CCPool != nil && role.CCPool.BeadsDir != "" {
		bdDir = role.CCPool.BeadsDir
	}
	return executor.Deps{
		CC:  cc,
		BD:  beads.NewCLIRunnerForRepo(bdDir, roleActor(role)),
		Cfg: cfg,
		Log: newEventLog(cfg),
		// Per-session flocks under the handler state directory (the same one as
		// events.jsonl) serialize the orphan reconcile against an absorbing
		// dispatch (pg2-g2u9m, INV-CCH-18).
		LockDir: sessionlock.Dir(handlerStateDir(cfg)),
	}
}

// roleActor is the bd identity the handler's own bd writes for role are
// attributed to (bead pg2-lhi3b): the role's ccpool actor, i.e. the same
// identity its dispatched worker sessions claim under via BEADS_ACTOR
// (internal/executor/ccpool.go). "" for a role with no ccpool block.
func roleActor(role roles.Role) string {
	if role.CCPool != nil {
		return role.CCPool.Actor
	}
	return ""
}

// handlerEventLogName is the JSONL file, inside the handler state directory,
// that the executor's and watchdog's structured events (hard_stop,
// needs_input, reminder, ...) are appended to (bead pg2-ui2gk).
const handlerEventLogName = "events.jsonl"

// newEventLog opens the handler's event log at
// <stateDir>/events.jsonl (stateDir resolved exactly as the origin prober
// resolves it: cfg.OriginProbe.StateDir, else the XDG/home default). It
// never returns nil: executor.Deps.Log and watchdog.Watchdog.Log treat nil as
// a silent no-op, which is the defect this fixes. If the file cannot be
// opened the handler still must dispatch, so it falls back to a writer on
// os.DevNull (events dropped, warning logged via slog) rather than nil.
func newEventLog(cfg config.Config) *eventlog.Writer {
	w, err := eventlog.New(filepath.Join(handlerStateDir(cfg), handlerEventLogName))
	if err != nil {
		slog.Warn("dispatch: event log unavailable; events dropped", "err", err)
		if w, err = eventlog.New(os.DevNull); err != nil {
			slog.Warn("dispatch: null event log unavailable", "err", err)
		}
	}
	return w
}

func writeReply(w io.Writer, v any) {
	b, _ := json.Marshal(v)
	if _, err := w.Write(b); err != nil {
		// Not fatal: the reader (the daemon) may be gone already, e.g. a dispatch
		// that outlived a daemon restart (ADR 0085). The dispatch's work is done.
		slog.Warn("reply undeliverable; the process that read this handler's stdout is gone", "err", err)
	}
}

func writeErrorReply(w io.Writer, msg string) {
	writeReply(w, map[string]any{"schemaVersion": schemas.SchemaVersion, "error": msg})
}

// busyReasonCapacityUnknown / busyReasonAtCapacity are the short, stable
// classification tags writeBusyReply carries for executor's two admission-
// gate sentinels (bead pg2-j4uwg). Kept as constants, never a free-text
// diagnostic sentence, so a wireclient caller on the other side of the wire
// can classify on the exact string rather than pattern-match prose (the
// slog.Warn/Info calls inside internal/executor/ccpool.go's run() already
// carry the human-readable detail for the operator's own logs).
const (
	busyReasonCapacityUnknown = "capacity-unknown"
	busyReasonAtCapacity      = "at-capacity"
	// busyReasonLowDisk (bead pg2-8vn8t): the isolation step's `git worktree
	// add` failed because the filesystem ran out of room (executor.ErrLowDisk)
	// — a transient, system-wide condition, not a per-bead defect, so it is
	// mapped to the SAME busy-decline treatment as the two capacity reasons
	// above rather than an escalation (see ccpool.go's own doc comment at its
	// isolation-Ensure error branch).
	busyReasonLowDisk = "low-disk"
	// busyReasonCommandBusy (bead pg2-358u3): a "command" role's argv exited 9
	// (conformance.ExitBusy's own code — the command's "not right now" signal),
	// which internal/executor/command.go turns into executor.ErrBusy. It gets
	// its OWN tag, not at-capacity's, because the cause is opaque to this
	// module (the command, not ccpool, decided it was busy) and the Grafana
	// failure-rate rule excludes at-capacity as routine backpressure — a
	// command that stays busy MUST remain visible, not hide inside that
	// exclusion.
	busyReasonCommandBusy = "command-busy"
)

// busyDeclineReason maps err to the wire's busy-decline reason tag when err
// is one of executor's four "decline and retry later" sentinels (bead
// pg2-j4uwg's capacity pair, widened by pg2-8vn8t's low-disk sentinel and
// pg2-358u3's command-busy sentinel): ok is false for any other err
// (including nil), matching none of them. All four still map to the SAME wire-level conformance.ExitBusy — the core's
// retry/backoff cadence never depends on which reason applied (INV-FAIL-1)
// — reason exists purely so pg-router core's declined metric/status
// breakdown can tell them apart (see this file's runDispatch call site and
// ErrPoolCapacityUnknown/ErrPoolAtCapacity/ErrLowDisk/ErrBusy's own doc
// comments).
func busyDeclineReason(err error) (reason string, ok bool) {
	switch {
	case errors.Is(err, executor.ErrPoolCapacityUnknown):
		return busyReasonCapacityUnknown, true
	case errors.Is(err, executor.ErrPoolAtCapacity):
		return busyReasonAtCapacity, true
	case errors.Is(err, executor.ErrLowDisk):
		return busyReasonLowDisk, true
	case errors.Is(err, executor.ErrBusy):
		return busyReasonCommandBusy, true
	default:
		return "", false
	}
}

// writeBusyReply writes the OPTIONAL reply body DEC-WIRE-1 / interfaces.md's
// "Coarse outcome, rich reply" allows a participant to accompany an exit-9
// busy decline with ("a reply body, where the participant supplies one,
// still carries which case applies") — reusing writeReply, the SAME
// lowest-level body-writing helper writeErrorReply and the success path
// both already use, rather than inventing a second one. No handler.dispatch-
// reply schema branch covers this shape yet (that schema's oneOf enumerates
// only the sync-outcome/deferred-ack success shapes); wireclient.Dispatch on
// the core side reads this OPPORTUNISTICALLY on exit 9 and tolerates an
// absent/empty body exactly as before this bead (DEC-WIRE-1: "no body
// required").
func writeBusyReply(w io.Writer, reason string) {
	writeReply(w, map[string]any{"schemaVersion": schemas.SchemaVersion, "reason": reason})
}

// handlerStateDir resolves the handler state directory exactly as the origin
// prober does: cfg.OriginProbe.StateDir, else $XDG_STATE_HOME/pg-router-
// ccpool-handler, else ~/.local/state/pg-router-ccpool-handler. It holds
// events.jsonl, the origin state, and the per-session locks.
func handlerStateDir(cfg config.Config) string {
	if sd := cfg.OriginProbe.StateDir; sd != "" {
		return sd
	}
	return originprobe.DefaultStateDir()
}
