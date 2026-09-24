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
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/beads"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/ccpool"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/worktree"
	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/schemas"
	"github.com/phillipgreenii/x/gitclient"
)

// gitWorktreeOpener is the production worktree.Opener wired into
// servePreShutdown by runPreShutdown below: gitclient.New adapted to
// worktree.Opener's covariant WorktreeManager return (mirrors
// internal/executor.Deps.gitOpener's identical adapter closure). It anchors
// directly at whatever dir it is given (closeUnlessNeedsInput passes s.CWD
// itself, never RepoRoot) -- proven equivalent to `git worktree remove` run
// from the repo root, since `git -C <worktree> worktree remove <worktree>`
// removes a linked worktree from within itself just as well. Tests call
// teardownAllSessions/closeUnlessNeedsInput directly with a fake
// worktree.Opener instead of this default.
var gitWorktreeOpener worktree.Opener = func(ctx context.Context, dir string) (gitclient.WorktreeManager, error) {
	return gitclient.New(ctx, dir)
}

// runPreShutdown implements the `preShutdown` INTF-HANDLER subcommand
// (pg2-oju6w.15): dispatched once per daemon shutdown, at the same point
// pg-router's own core used to call the now-deleted Orchestrator.TeardownAll
// — this is that same once-per-process sweep, relocated into this handler's
// own process (ccpool session lifecycle is entirely this participant's own
// business now, never the core's), not a redesign.
//
// SUPERSEDED (bead pg2-asr8z, 2026-09-22 — Phillip Green II, drain session):
// this subcommand used to get its own preShutdown call once PER ENABLED
// ROLE ("decision #1"'s no-dedup ruling: "harmless [...] and an accepted
// consequence of the no-dedup decision, not something to 'fix' here").
// Production evidence disproved "harmless" — see
// cmd/pg-router/run.go's preShutdownAll, whose own doc now carries the full
// story. The sweep this subcommand triggers is GLOBAL (matches on
// SessionPrefix only, no per-role scoping in ccpool.Session, and
// --role-config is not even read by this subcommand — see below), so it is
// invariant across roles regardless of which one's preShutdown call
// triggered it; preShutdownAll now calls this exactly ONCE per shutdown,
// never once per enabled role.
func runPreShutdown(args []string) int {
	fs := flag.NewFlagSet("preShutdown", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	roleConfig := fs.String("role-config", os.Getenv(envRoleConfig), "path to this process's own role config JSON (or "+envRoleConfig+")")
	cfgPath := fs.String("config", os.Getenv(envConfig), "path to this process's own launch config JSON (or "+envConfig+")")
	switch err := fs.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Print(helpText)
		return conformance.ExitOK
	case err != nil:
		fmt.Fprintln(os.Stderr, "preShutdown:", err)
		return conformance.ExitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "preShutdown: unexpected argument:", fs.Arg(0))
		fmt.Fprintln(os.Stderr, "preShutdown takes its request as JSON on stdin, never as arguments")
		return conformance.ExitUsage
	}

	// --role-config is deliberately NOT loaded here: teardownAllSessions
	// below sweeps by SessionPrefix alone (config.Config, not the per-role
	// roleFile) — every enabled role shares the same sweep, per this
	// function's own doc comment above. Parsed and accepted anyway (like
	// dispatch's own --role-config) purely for CLI-surface symmetry; unused
	// on purpose.
	_ = roleConfig
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "preShutdown:", err)
		return conformance.ExitError
	}

	return servePreShutdown(ccpool.NewCLIRunner(cfg), gitWorktreeOpener, beads.NewCLIRunnerForRepo(cfg.RepoRoot), cfg.SessionPrefix, cfg.RepoRoot, os.Stdin, os.Stdout)
}

// servePreShutdown is runPreShutdown's testable core, factored out so a test
// can drive it against a fake ccpool.Runner and capture its reply without
// touching a real ccpool binary or os.Stdin/os.Stdout — mirrors
// conformance.Participant.Serve's own (stdin, stdout) shape.
func servePreShutdown(cc ccpool.Runner, open worktree.Opener, br beads.Runner, sessionPrefix, repoRoot string, stdin io.Reader, stdout io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "preShutdown: read request from stdin:", err)
		return conformance.ExitError
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		writeErrorReply(stdout, "malformed JSON: "+err.Error())
		return conformance.ExitError
	}
	if err := conformance.Check("handler.preShutdown", v); err != nil {
		writeErrorReply(stdout, err.Error())
		return conformance.ExitError
	}
	var req lifecycleRequest
	_ = json.Unmarshal(raw, &req)

	closed := teardownAllSessions(context.Background(), cc, open, br, sessionPrefix, repoRoot)
	slog.Info("preShutdown: teardown", "closed", closed)

	writeReply(stdout, map[string]any{
		"schemaVersion": schemas.SchemaVersion,
		"id":            req.ID,
		"outcome":       "ok",
	})
	return conformance.ExitOK
}

// teardownAllSessions closes every session whose name carries prefix — this
// process's own sessions and strays left by a crashed prior run — EXCEPT
// sessions in needs_input WHOSE BEAD IS STILL OPEN, which are preserved (left
// alive) so the operator can still `ccpool attach` after the pass. A
// needs_input session whose own bead has ALREADY been closed is reconciled
// like any other closable session (closeUnlessNeedsInput's own doc comment;
// pg2-5sirm). For every session it does close, it also best-effort removes
// that session's own worktree (s.CWD) via open, undoing internal/worktree.
// Ensure's creation side (pg2-a8h6c — the short-term stopgap; the full
// redesign is pg2-4roho). Sessions outside the prefix are left untouched.
// Returns the number actually closed.
//
// This is the once-per-process-lifetime sweep half of this module's
// INTF-CCH-CCPOOL boundary crossing (docs/behavior/interfaces.md) — not
// scoped to one dispatch, unlike internal/ccpool's own per-dispatch
// start/observe/reap half.
//
// Ported verbatim from packages/pg-router's own (now-deleted)
// Orchestrator.teardownAll — this module's own local re-implementation, not
// an import (Go's internal-package visibility rule; docs/adr/0065's
// Addendum), since that package no longer exists in this module.
func teardownAllSessions(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, prefix, repoRoot string) (closed int) {
	sessions, err := cc.List(ctx)
	if err != nil {
		slog.Warn("preShutdown: teardown list failed", "err", err)
		return 0
	}
	for _, s := range sessions {
		if !strings.HasPrefix(s.ExternalID, prefix) {
			continue
		}
		if closeUnlessNeedsInput(ctx, cc, open, br, repoRoot, s) {
			closed++
		}
	}
	return closed
}

// closeUnlessNeedsInput tears down one session UNLESS it is in needs_input
// AND its own bead is still open, in which case it is PRESERVED (left alive,
// worktree untouched) so the operator can still `ccpool attach
// <external_id>`. Returns true iff the session was actually closed (purged).
//
// A needs_input session whose bead has ALREADY been closed is reconciled —
// closed like any other session — rather than preserved forever: this is
// pg2-5sirm's fix for the gap its own investigation proved live (bead
// zr-50s7h.2: closed 2026-09-19, review fully posted; its own ccpool session
// nonetheless sat in needs_input, live, burning CPU, for ~2.5 days because
// nothing besides this preShutdown sweep ever revisits a needs_input session,
// and the sweep itself unconditionally preserved every one of them). Once the
// bead is closed there is nothing left for the operator to attach and act
// on, so preserving the session buys nothing and only leaks a live process
// and its worktree.
//
// beadAlreadyClosed (below) fails CLOSED (preserve) on every ambiguous case —
// no bead metadata on the session at all, or a bd lookup error — so this
// reconciliation only ever fires on an UNAMBIGUOUSLY closed bead; it can
// never purge a session out from under an operator who still has open work
// to attach to.
//
// The actual purge — cc.Close plus the best-effort worktree removal — is
// closeSessionAndWorktree below, shared verbatim with reconcile.go's
// reconcileClosedBeadSessions (pg2-hrppg): the periodic counterpart to this
// once-per-shutdown sweep, which reconciles a closed-bead session's
// StateIdle/StateNeedsInput row while the daemon is still up, rather than
// leaving it to leak until the next shutdown.
func closeUnlessNeedsInput(ctx context.Context, cc ccpool.Runner, open worktree.Opener, br beads.Runner, repoRoot string, s ccpool.Session) bool {
	if s.State == ccpool.StateNeedsInput && !beadAlreadyClosed(ctx, br, s) {
		slog.Info("preShutdown: teardown preserving needs_input session for operator attach",
			"session", s.ExternalID, "attach", "ccpool attach "+s.ExternalID)
		return false
	}
	return closeSessionAndWorktree(ctx, cc, open, repoRoot, s)
}

// closeSessionAndWorktree purges s via cc.Close(purge=true), then
// best-effort removes s's own working directory (ccpool.Session.CWD) as a
// linked git worktree via open/gitclient.WorktreeManager.RemoveWorktree —
// and, once that removal actually succeeds, deletes s's own
// pg-router/<beadID> anchor branch (deleteAnchorBranch below). This must
// fail SOFT (log + continue), never abort the caller's sweep or be promoted
// to a nonzero overall exit: both callers — closeUnlessNeedsInput's
// once-per-shutdown sweep and reconcile.go's periodic
// reconcileClosedBeadSessions — sweep sessions regardless of which
// IsolationConfig.Type (internal/executor/isolation.go) launched them, so
// s.CWD is not always a registered linked worktree of any repository — for
// "none" isolation it's RepoRoot itself (git refuses to remove a repo's main
// working tree), and for "path"/"workforest" isolation it's an unrelated
// directory that may not even be inside a git repository at all. Either
// failure shape (open erroring because cwd isn't inside any repo, or
// RemoveWorktree itself erroring because it isn't a registered linked
// worktree of the repo it IS inside) is expected and equally harmless — same
// fail-soft posture cc.Close's own error handling below already has. Since
// the branch delete only ever runs after a successful RemoveWorktree, it
// naturally never fires for either of those isolation types either — only a
// genuine "worktree" isolation session ever has a pg-router/<beadID> anchor
// branch to delete in the first place.
// Returns true iff cc.Close succeeded (the session was actually purged); a
// worktree-removal or branch-delete failure never changes that.
//
// The branch-delete step is bead pg2-ci75j's own fix, applied here at this
// THIRD call site by pg2-tpa18: ci75j's own acceptance criterion named only
// internal/executor.cleanupWorktree and the nix mkWorktreeSweepScript,
// leaving this preShutdown/reconcile call site explicitly out of scope —
// x/gitclient's RemoveWorktree only ever ran `git worktree remove`, never
// touching the branch it was created on, so every session purged through
// this function orphaned its pg-router/<beadID> anchor branch forever until
// now.
func closeSessionAndWorktree(ctx context.Context, cc ccpool.Runner, open worktree.Opener, repoRoot string, s ccpool.Session) bool {
	if err := cc.Close(ctx, s.ExternalID, true); err != nil {
		slog.Warn("teardown: close failed", "session", s.ExternalID, "err", err)
		return false
	}
	if wm, err := open(ctx, s.CWD); err != nil {
		slog.Warn("teardown: worktree remove failed (cwd may not be inside a git repository)",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
	} else if err := wm.RemoveWorktree(ctx, s.CWD, true); err != nil {
		slog.Warn("teardown: worktree remove failed (cwd may not be a linked worktree)",
			"session", s.ExternalID, "cwd", s.CWD, "err", err)
	} else {
		slog.Info("teardown: worktree removed", "session", s.ExternalID, "cwd", s.CWD)
		deleteAnchorBranch(ctx, open, repoRoot, s.ExternalID, s.Meta[ccpool.MetaKeyBead])
	}
	return true
}

// deleteAnchorBranch removes s's own throwaway pg-router/<beadID> anchor
// branch (worktree.Ensure's own doc comment: "a dedicated branch
// pg-router/<beadID>") now that closeSessionAndWorktree has confirmed its
// worktree is gone. x/gitclient's RemoveWorktree runs only `git worktree
// remove` and never touches the branch (bead pg2-ci75j's root cause) --
// without this call, the branch is orphaned in repoRoot forever, exactly the
// ~100+-stray-branch state pg2-ci75j found in the ZR monorepo at its other
// two call sites.
//
// Deliberately reopens the gitclient at repoRoot rather than reusing the
// worktree.Opener result the caller already holds: that manager was opened
// at s.CWD, which RemoveWorktree just removed, so it can no longer spawn git
// at all -- a branch is a repo-level ref that must be deleted from a still-
// existing checkout, the same anchor worktree.Ensure itself used to create
// the branch in the first place (internal/worktree/worktree.go's Ensure
// opens repoRoot, not the not-yet-created worktree path, to create it).
//
// beadID comes from the session's own pgrouter.bead metadata
// (ccpool.MetaKeyBead) -- the SAME source beadAlreadyClosed above already
// trusts for this session's bead identity, rather than re-deriving one from
// s.CWD's basename. An empty beadID (an older session dispatched before
// pg2-5sirm's meta round-trip, or a non-"worktree"-isolation stray this
// sweep still matched by prefix alone) is a no-op: "pg-router/" alone is not
// a real branch, and there is nothing safe to delete.
//
// These are pg-router's own internal per-dispatch anchor branches, reset at
// HEAD on every dispatch (worktree.Ensure's own ResetBranch/-B) -- never a
// user branch with independent value -- so once the worktree is confirmed
// removed the branch has fully served its purpose. Most are never merged
// into anything, so deletion uses force (`git branch -D`) rather than plain
// `-d`, which would refuse constantly for exactly that reason.
//
// Fails soft exactly like RemoveWorktree above: any error here (open
// failure, the branch already gone, a client that isn't a BranchManager, a
// transient git error) is logged and left for the next sweep, never
// escalated -- an orphaned branch costs disk, not correctness.
func deleteAnchorBranch(ctx context.Context, open worktree.Opener, repoRoot, externalID, beadID string) {
	if beadID == "" {
		return
	}
	branch := "pg-router/" + beadID
	root, err := open(ctx, repoRoot)
	if err != nil {
		slog.Warn("teardown: branch delete: open repo root failed (left for next sweep)",
			"session", externalID, "branch", branch, "err", err)
		return
	}
	bm, ok := root.(gitclient.BranchManager)
	if !ok {
		return
	}
	if err := bm.DeleteBranch(ctx, branch, true); err != nil {
		slog.Warn("teardown: branch delete failed (left for next sweep)",
			"session", externalID, "branch", branch, "err", err)
		return
	}
	slog.Info("teardown: branch deleted", "session", externalID, "branch", branch)
}

// beadAlreadyClosed reports whether s's own bead — read from its
// pgrouter.bead session metadata, ccpool.MetaKeyBead, stamped at dispatch by
// ccpool.DispatchMeta and round-tripped back through `ccpool list --json`'s
// own meta object (internal/ccpool.Session.Meta) — is already closed. This
// reads the LIVE session metadata rather than re-deriving a bead id by
// parsing ExternalID/Name (roles.Role.ExternalID/DisplayName's own
// "<prefix><name>-<beadid>[-<stamp>]" shape is not safely reversible here:
// this sweep runs across every enabled role sharing SessionPrefix, so it
// never has a single role.Name to strip, and both the prefix and a role name
// can themselves contain "-").
//
// Fails CLOSED (false — preserve, the existing/safe default) on every
// ambiguous case: no bead metadata on the session at all (an older session
// dispatched before pg2-5sirm, or a non-"worktree"-isolation stray this sweep
// still matches by prefix alone), or a `bd show` error (bd unreachable) —
// never risk purging a session an operator still needs to attach to just
// because bd could not be reached.
//
// Shared verbatim by both callers of this file's own sweeps: closeUnlessNeedsInput
// (needs_input only) and reconcile.go's reconcileClosedBeadSessions
// (idle or needs_input, pg2-hrppg) — the log line below is deliberately
// state-agnostic ("preserving session"), not "preserving needs_input
// session", since an idle session can hit this same ambiguous-lookup path.
func beadAlreadyClosed(ctx context.Context, br beads.Runner, s ccpool.Session) bool {
	beadID := s.Meta[ccpool.MetaKeyBead]
	if beadID == "" {
		return false
	}
	status, err := beads.Status(ctx, br, beadID)
	if err != nil {
		slog.Warn("teardown: bead-status lookup failed; preserving session",
			"session", s.ExternalID, "bead", beadID, "err", err)
		return false
	}
	return status == "closed"
}
