package daemon

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/otel"
)

// pa_monitor.sessions.errored previously counted every session with a LastError
// set, INCLUDING superseded errors on sessions that had recovered — so the
// "Sessions errored" panel read e.g. 5 when 0 sessions were currently erroring
// (bead pg2-...). It must count only sessions CURRENTLY blocked on an error /
// usage-limit, matching the ADR-0024 status model.
func TestBuildErroredCountsOnlyCountsCurrentlyBlocked(t *testing.T) {
	term := &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: time.Now()}

	// Currently blocked on the error -> counts.
	blocked := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "a", Status: session.Blocked, Blocker: session.ErrorBlocker},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: term},
	}
	// Blocked on a usage limit -> counts.
	usage := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "b", Status: session.Blocked, Blocker: session.UsageLimit},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: &transcript.ErrorRecord{Kind: transcript.ErrRateLimit, IsTerminal: true, At: time.Now()}},
	}
	// Recovered: working now, but still carries the historical (superseded)
	// error -> must NOT count.
	recovered := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "c", Status: session.Working},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: term},
	}
	// Idle with a stale error -> must NOT count.
	idleStale := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "d", Status: session.Idle},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: term},
	}

	counts := buildErroredCounts(treeWithSessions(blocked, usage, recovered, idleStale))
	total := 0
	for _, c := range counts {
		total += c
	}
	if total != 2 {
		t.Fatalf("only the 2 currently-blocked sessions should count; got total=%d counts=%v", total, counts)
	}
	if counts[otel.ErroredKey{Kind: string(transcript.ErrServerError), IsTerminal: true}] != 1 {
		t.Errorf("expected the terminal server_error key to count exactly 1; counts=%v", counts)
	}
}

// A session blocked on a HUMAN blocker (auth failure / awaiting input) carries a
// terminal LastError but is NOT an "error"-blocked session under ADR-0024 — it
// needs a human, and auth has its own surface. It MUST be excluded from
// sessions.errored (a behavior change: the old "any LastError" logic counted
// auth failures).
func TestBuildErroredCountsExcludesHumanBlockers(t *testing.T) {
	authFail := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "a", Status: session.Blocked, Blocker: session.HumanAuthn},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: &transcript.ErrorRecord{Kind: transcript.ErrAuthFailed, IsTerminal: true, At: time.Now()}},
	}
	awaitingHuman := &aggregate.SessionView{
		Session:           &session.Session{SessionID: "b", Status: session.Blocked, Blocker: session.HumanInput},
		SessionEnrichment: aggregate.SessionEnrichment{LastError: &transcript.ErrorRecord{Kind: transcript.ErrServerError, IsTerminal: true, At: time.Now()}},
	}
	counts := buildErroredCounts(treeWithSessions(authFail, awaitingHuman))
	total := 0
	for _, c := range counts {
		total += c
	}
	if total != 0 {
		t.Fatalf("human-blocked sessions must NOT count as errored; got total=%d counts=%v", total, counts)
	}
}
