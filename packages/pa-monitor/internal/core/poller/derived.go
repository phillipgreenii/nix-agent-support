package poller

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/limits"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
	"github.com/phillipgreenii/pa-monitor/internal/core/usage"
)

// SessionProjection is the per-session corpus + provider projection the producer
// derives once (from the Monitor + provider Cache) and the emit tick reads back
// from the published DerivedState — the tick NEVER re-reads the Monitor or the
// providers. Every field here is producer-owned and immutable once published.
type SessionProjection struct {
	// ResolvedPath / TranscriptMTime / ResolvedOK mirror Monitor.ResolvedPath.
	ResolvedPath    string
	TranscriptMTime time.Time
	ResolvedOK      bool
	// Snapshot is the folded transcript projection (Monitor.SessionSnapshot).
	Snapshot transcript.Snapshot
	// MaxActivity is max(transcript mtime, newest subagent mtime) (Monitor.MaxActivity).
	MaxActivity time.Time
	// SubagentError is the latest terminal subagent error, nil when none
	// (Monitor.SubagentError).
	SubagentError *transcript.ErrorRecord
	// Subshells is the provider subshell count for the session.
	Subshells int
}

// DerivedState is the immutable snapshot the producer goroutine assembles per
// dispatch batch and publishes via atomic.Pointer.Store. The emit tick Loads one
// consistent DerivedState and reads every corpus + provider projection from it,
// so parsing/providers run off the tick (design §3/§6). The producer builds a
// fresh value each batch and NEVER mutates a published one; the tick treats
// Sessions/maps as read-only (it copies a session before deriving Status).
type DerivedState struct {
	// GeneratedAt is the producer `now` at which this batch was assembled.
	GeneratedAt time.Time
	// Sessions is the discovered set (alive + dead-PID pre-GC). Branch,
	// TerminalHost and TranscriptMTime are producer-set; Status/Blocker/LongIdle
	// are NOT set here (the tick derives them into its own copies).
	Sessions []*session.Session
	// Projections is keyed by session id.
	Projections map[string]SessionProjection
	// PRByDir maps cwd -> PR info (producer PR lookups).
	PRByDir map[string]*session.PRInfo
	// RepoLabels maps cwd -> workspace.repo label for KNOWN repos only (C1): the
	// producer computes the repo label in the producer and publishes it here, so
	// the tick's label/gauge path never calls providerCache.RepoLabel. Absent
	// (no entry) for a non-repo cwd.
	RepoLabels map[string]string
	// Block is the current 5h block priced at GeneratedAt (Monitor.Block); nil
	// when no active block.
	Block *usage.Block
	// CostProbed / CostProbeErr mirror Monitor.CostProbed.
	CostProbed   bool
	CostProbeErr error
	// Limits is the account-global rate_limits reading (Monitor.Limits); nil when
	// no data captured yet.
	Limits *limits.Limits
	// Weekly is the current Monday-anchored week's cost at GeneratedAt
	// (Monitor.Weekly); nil when no records this week.
	Weekly *usage.WeeklyEntry
}

// RepoLabel reports the published workspace.repo label for cwd (C1). The second
// return is false for a non-repo cwd, matching provider.Cache.RepoLabel's
// contract, so the label detector reads DerivedState identically.
func (d *DerivedState) RepoLabel(cwd string) (string, bool) {
	if d == nil || d.RepoLabels == nil {
		return "", false
	}
	v, ok := d.RepoLabels[cwd]
	return v, ok
}
