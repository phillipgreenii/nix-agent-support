package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
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
// session or any of its subagents, so "no write for WorktreeQuietWindow"
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
// Fail-safe directions: session absent or no transcript path (nothing
// observable) => proceed, matching needsInputAlive's "absent => gone".
func (r *ccpoolRun) waitSessionQuiet(ctx context.Context, name string) bool {
	window := r.deps.Cfg.WorktreeQuietWindow
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
