package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// waitSessionQuiet reports whether it is safe, w.r.t. still-running child
// subagents, to remove the worktree of session name (pg2-9fwft).
//
// Problem: a session may fan out Agent-tool subagents (code-review does) that
// share its worktree and outlive the bead's `bd close`. The pool cannot see
// them as bd-tracked units, and removing the worktree yanks the cwd from
// under them (and, if the path were reused for another task, would let a
// straggler read the wrong checkout).
//
// Mechanism chosen: a QUIET WINDOW on the session's transcript activity. The
// session's transcript (ccpool Session.TranscriptPath) and every
// <session>/subagents/*.jsonl beside it are written on each tool call of the
// session or any of its subagents, so "no write for the quiet window"
// (Config.WorktreeQuietWindow, or the role's own override, quietWindow below)
// means nothing is still working. We poll until quiet, bounded by
// WorktreeQuietMax; on expiry the caller leaves the worktree in place (fail
// soft; pg-disk-reclaimer / a later dispatch reclaims it). Window <= 0
// disables the check.
//
// Rejected alternatives:
//   - Process-cwd check (lsof): the session's own idle claude process holds
//     the worktree as cwd and Agent-tool subagents run inside that same
//     process, so it cannot distinguish "idle" from "subagents working".
//   - Fixed grace delay only: either too short (straggler still running) or
//     needlessly long for every dispatch.
//   - Asking the session for its subagent tree (ListAgents): needs a
//     cross-process protocol change the pool has no channel for.
//
// Straggler-subagent risk and the per-role override (bead pg2-uyahp,
// INV-CCH-23): the check is a HEURISTIC, not a proof. A transcript is written
// when a message or tool call/result completes, so a subagent in one long
// generation or one long tool call (a multi-minute `go test`) is silent for
// that whole span and looks quiet once the window elapses. Shortening the
// window widens that exposure; every transcript write resets it. The 2 min
// default is the safe choice for a role that fans out subagents. A role that
// finishes in one short, flat pass (review) pays ~2.5-3.5 min of counted-slot
// hold per dispatch for it, so it MAY set a shorter window
// (roles.CCPoolConfig.WorktreeQuietWindow). The consequence of a wrong "quiet"
// is bounded: the worktree removal is non-force (git refuses a dirty tree), the
// settled-session close is non-purge, and a removed worktree only ever strands
// a subagent that was still running after the session's own bead had already
// completed.
//
// Fail-safe directions: session absent or no transcript path (nothing
// observable) => proceed, matching needsInputAlive's "absent => gone".
func (r *ccpoolRun) waitSessionQuiet(ctx context.Context, name string, window time.Duration) bool {
	if window <= 0 {
		return true
	}
	sessions, err := r.deps.CC.List(ctx)
	if err != nil {
		return false // can't tell => don't delete
	}
	var transcript string
	for _, s := range sessions {
		if s.ExternalID == name {
			transcript = s.TranscriptPath
			break
		}
	}
	if transcript == "" {
		return true
	}
	latest := r.deps.LatestActivity
	if latest == nil {
		latest = LatestTranscriptActivity
	}
	poll := r.deps.Cfg.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	deadline := r.deps.clock().Add(r.deps.Cfg.WorktreeQuietMax)
	for {
		now := r.deps.clock()
		t, ok := latest(transcript)
		if !ok || now.Sub(t) >= window {
			return true
		}
		if !now.Before(deadline) {
			return false
		}
		if r.deps.waitPoll(ctx, poll) != nil {
			return false
		}
	}
}

// quietWindow is the quiet window that applies to a dispatch of role cc: the
// role's own WorktreeQuietWindow when it sets one (> 0), else the handler-wide
// Config.WorktreeQuietWindow. A handler-wide value <= 0 disables the check
// outright and is NOT overridable by a role, so it stays an operator kill
// switch. A nil cc is treated as "no override".
func (r *ccpoolRun) quietWindow(cc *roles.CCPoolConfig) time.Duration {
	window := r.deps.Cfg.WorktreeQuietWindow
	if window > 0 && cc != nil && cc.WorktreeQuietWindow > 0 {
		return cc.WorktreeQuietWindow
	}
	return window
}

// LatestTranscriptActivity returns the newest mtime among transcriptPath and
// the sibling <transcript-without-.jsonl>/subagents/*.jsonl files.
func LatestTranscriptActivity(transcriptPath string) (time.Time, bool) {
	var newest time.Time
	found := false
	consider := func(p string) {
		if fi, err := os.Stat(p); err == nil && (!found || fi.ModTime().After(newest)) {
			newest, found = fi.ModTime(), true
		}
	}
	consider(transcriptPath)
	subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(transcriptPath, ".jsonl"), "subagents", "*.jsonl"))
	for _, p := range subs {
		consider(p)
	}
	return newest, found
}
