package main

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/phillipgreenii/ccpool/internal/facts"
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
// is liveness-gated (SessionAlive: PID alive and procStart matching) before the verdict is trusted: a "busy" row can
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
func registryVerdict(sessionsDir, claudeSessionID, transcriptPath string, freshWindow time.Duration) (ct.ActivityVerdict, bool, error) {
	return registryVerdictWith(sessionsDir, claudeSessionID, transcriptPath, freshWindow, registryFacts{
		alive:        factReader.ProcessAlive,
		awaiting:     factReader.IsAwaitingInput,
		lastActivity: factReader.MessageActivity,
	})
}

// registryFacts is the fact source registryVerdictWith reads: whether a Process
// is alive now (the recorder's Process fact, which replaces the inline pid and
// start-time check), and the transcript facts the freshness cross-check needs.
type registryFacts struct {
	alive        func(pid int64, procStart string) (bool, error)
	awaiting     func(transcriptPath string) (bool, error)
	lastActivity func(transcriptPath string) (time.Time, bool, error)
}

// procStartLayout is the ctime-style UTC layout the recorder and the registry
// file name a Process's start time in.
const procStartLayout = "Mon Jan _2 15:04:05 2006"

// registryVerdictWith is registryVerdict with the fact source injected. The only
// error is a fact read that failed (a recorder that is down: "the recorder is not available");
// no match or a dead process is (zero, false, nil).
func registryVerdictWith(sessionsDir, claudeSessionID, transcriptPath string, freshWindow time.Duration, f registryFacts) (ct.ActivityVerdict, bool, error) {
	if claudeSessionID == "" {
		return ct.ActivityVerdict{}, false, nil
	}
	rows, err := ct.ReadSessionRegistry(sessionsDir)
	if err != nil {
		return ct.ActivityVerdict{}, false, nil
	}
	for _, reg := range rows {
		if reg.SessionID != claudeSessionID {
			continue
		}
		procStart := ""
		if !reg.ProcStart.IsZero() {
			procStart = reg.ProcStart.UTC().Format(procStartLayout)
		}
		alive, aerr := f.alive(int64(reg.PID), procStart)
		if aerr != nil {
			return ct.ActivityVerdict{}, false, aerr
		}
		if !alive {
			return ct.ActivityVerdict{}, false, nil // stale row from a dead/crashed or reused pid
		}
		awaitingInput := false
		var lastActivity time.Time
		if transcriptPath != "" {
			a, werr := f.awaiting(transcriptPath)
			if werr != nil && errors.Is(werr, facts.ErrDaemonOffline) {
				return ct.ActivityVerdict{}, false, werr
			}
			awaitingInput = werr == nil && a
			if t, ok, lerr := f.lastActivity(transcriptPath); lerr == nil && ok {
				lastActivity = t
			} else if lerr != nil && errors.Is(lerr, facts.ErrDaemonOffline) {
				return ct.ActivityVerdict{}, false, lerr
			}
		}
		return ct.ClassifyActivity(reg, awaitingInput, lastActivity, freshWindow), true, nil
	}
	return ct.ActivityVerdict{}, false, nil
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
