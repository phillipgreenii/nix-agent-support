package main

import (
	"os"
	"path/filepath"
	"time"

	ct "github.com/phillipgreenii/claude-transcript"
)

// registryWaitingFreshWindow is ccpool's tolerance for the registry "waiting"
// freshness cross-check (ClassifyActivity). A "waiting" flag is trusted iff the
// transcript has not advanced well past statusUpdatedAt + this window. Long
// enough that a genuine human-blocked wait stays fresh across a status poll;
// short enough that an abandoned wait whose transcript later advanced is demoted
// to idle. The library leaves this policy to the caller (see ClassifyActivity).
const registryWaitingFreshWindow = 5 * time.Minute

// registryVerdict resolves the shared claude-transcript activity verdict for one
// ccpool session. It is the cmd-layer adapter the state.Gather resolver wraps.
//
// ccpool sessions are keyed by ClaudeSessionID; the per-process Claude registry
// is keyed by PID (~/.claude/sessions/<pid>.json) with a sessionId field — so
// the join is: sweep the dir, match reg.SessionID == claudeSessionID. The match
// is PID-GATED (PidAlive) before the verdict is trusted: a "busy" row can
// survive a crash (the file lingers until GC), so a dead pid reports
// (zero, false). awaitingInput and lastActivity come from the transcript (both
// error-tolerant). A missing dir, no match, or a dead pid all return
// (zero verdict, false) — the classifier then ignores the registry and falls
// back to its pane+row precedence.
//
// Mapping back to ccpool state (applied by state.Classify):
//
//	ct.Active          -> state.Working
//	ct.WaitingForHuman -> state.WaitingForHuman
//	ct.Idle            -> state.Idle
func registryVerdict(sessionsDir, claudeSessionID, transcriptPath string, freshWindow time.Duration) (ct.ActivityVerdict, bool) {
	if claudeSessionID == "" {
		return ct.ActivityVerdict{}, false
	}
	rows, err := ct.ReadSessionRegistry(sessionsDir)
	if err != nil {
		return ct.ActivityVerdict{}, false
	}
	for _, reg := range rows {
		if reg.SessionID != claudeSessionID {
			continue
		}
		if !ct.PidAlive(reg.PID) {
			return ct.ActivityVerdict{}, false // stale row from a dead/crashed pid
		}
		awaitingInput := false
		if transcriptPath != "" {
			if a, aerr := ct.IsAwaitingInput(transcriptPath); aerr == nil {
				awaitingInput = a
			}
		}
		var lastActivity time.Time
		if transcriptPath != "" {
			if t, ok := ct.LastMessageActivity(transcriptPath); ok {
				lastActivity = t
			}
		}
		return ct.ClassifyActivity(reg, awaitingInput, lastActivity, freshWindow), true
	}
	return ct.ActivityVerdict{}, false
}

// defaultSessionsDir returns ~/.claude/sessions (the Claude Code per-process
// session registry directory). Empty when the home dir is unresolved.
func defaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}
