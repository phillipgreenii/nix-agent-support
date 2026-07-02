package aggregate

import (
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/ccusage"
	"github.com/phillipgreenii/pa-monitor/internal/core/session"
	"github.com/phillipgreenii/pa-monitor/internal/core/transcript"
)

type SessionEnrichment struct {
	ContextTokens     int
	Model             string
	FirstPrompt       string
	SubagentCount     int
	SubshellCount     int
	SessionTokens     int                     // cumulative output_tokens across session
	BurnRateShort     float64                 // tokens/min, short window
	BurnRateLong      float64                 // tokens/min, long window
	CostUSD           float64                 // estimated share, filled by Build
	AwaitingInput     bool                    // true when last assistant turn contains unresolved AskUserQuestion
	RateLimitResetsAt time.Time               // non-zero: session paused; window resets at this time
	LastError         *transcript.ErrorRecord // most recent api error from snapshot; nil if none
	// LastErrorRetryable is pa-monitor's auto-resume verdict for LastError
	// (transient server/network → true). Tracked separately from the shared
	// ErrorRecord because the daemon flips it to false on escalation,
	// independent of the record's intrinsic RetryClass. Zero when LastError nil.
	LastErrorRetryable bool
	PendingNudge       *PendingNudge // set by daemon nudger; nil when no intents pending

	// Nudge history (watermarks): populated by the daemon's lifecycle
	// annotation loop from WatermarkStore. Zero/empty when the session
	// has never received a nudge.
	LastNudgedAt     time.Time // wall clock of the most recent successful nudge fire
	LastNudgeSources []string  // sources that fired together at LastNudgedAt
}

// PendingNudge surfaces which nudge sources are currently queued for this
// session. Populated by the daemon's nudger before serialization to
// clients; producers/dispatcher are the source of truth.
type PendingNudge struct {
	Sources []string // subset of {"window_reset","disrupted","manual"}
}

type Directory struct {
	Path         string
	Branch       string
	PRInfo       *session.PRInfo
	Sessions     []*SessionView
	WorkingN     int
	IdleN        int
	DormantN     int
	WaitingN     int
	TotalTokens  int
	TotalCostUSD float64
	BurnRateSum  float64 // NEW: sum of children's BurnRateShort
}

type SessionView struct {
	*session.Session
	SessionEnrichment
}

type Tree struct {
	Dirs           []*Directory
	ActiveBlock    *ccusage.Block
	ActiveWeek     *ccusage.WeeklyEntry // populated by daemon when ccusage weekly data is available
	PlanCapUSD     float64
	WeekCapUSD     float64 // used by week tracker integration
	GeneratedAt    time.Time
	CCUsageProbed  bool      // true once the first ccusage probe has run
	CCUsageErr     error     // non-nil if ccusage exec failed
	WindowResetsAt time.Time // global: max RateLimitResetsAt across all sessions (zero = none)

	// Authoritative status-line rate_limits windows (ADR 0021 §6). These are
	// account-global and DISTINCT from WindowResetsAt / RateLimitResetsAt (the
	// daemon's pause / limit-hit concept): they carry Claude Code's server-side
	// 5h/7d used_percentage.
	//
	// A nil *float64 means "unknown/stale", distinct from a real 0% reading. A
	// zero SevenDayResetsAt / LimitsCapturedAt time.Time likewise means unknown
	// (never 1970). Phase 0 observed seven_day absent on this account, so these
	// are commonly unset. No consumer reads them yet — Phase 1 is persistence +
	// proto plumbing only.
	FiveHourPct      *float64  // 5h used_percentage; nil = unknown
	SevenDayPct      *float64  // 7d used_percentage; nil = unknown
	FiveHourResetsAt time.Time // 5h window reset; zero = unknown
	SevenDayResetsAt time.Time // 7d window reset; zero = unknown
	LimitsCapturedAt time.Time // capture ts of the limits reading; zero = unknown
}

// Sessions returns a flat list of every SessionView across all Directories.
// Intended for callers that operate per-session without caring about
// directory grouping (label decoration, telemetry emission, etc.).
func (t *Tree) Sessions() []*SessionView {
	if t == nil {
		return nil
	}
	var n int
	for _, d := range t.Dirs {
		n += len(d.Sessions)
	}
	out := make([]*SessionView, 0, n)
	for _, d := range t.Dirs {
		out = append(out, d.Sessions...)
	}
	return out
}

// TopupShouldDisplay returns true when the current 5h block's actual cost has
// reached or exceeded the plan cap — meaning the user is consuming top-up tokens.
func (t *Tree) TopupShouldDisplay() bool {
	if t.ActiveBlock == nil || t.PlanCapUSD <= 0 {
		return false
	}
	return t.ActiveBlock.CostUSD >= t.PlanCapUSD
}

// AuthFailedCount returns the number of sessions whose most recent error is a
// terminal authentication failure (HTTP 401 → run /login). Because a 401 is
// account-wide, any positive count means the credentials are broken for the
// whole user. Safe on a nil tree.
func (t *Tree) AuthFailedCount() int {
	n := 0
	for _, s := range t.Sessions() {
		le := s.SessionEnrichment.LastError
		if le != nil && le.IsTerminal && le.Kind == transcript.ErrAuthFailed {
			n++
		}
	}
	return n
}
