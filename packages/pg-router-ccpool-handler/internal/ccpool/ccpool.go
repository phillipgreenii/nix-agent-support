// Package ccpool is pg-router's seam onto the ccpool session manager. ALL session
// mechanics flow through Runner. The Phase-1 implementation (cli.go) shells out
// to the `ccpool` CLI; a future in-process implementation wrapping ccpool's
// session.Service is a drop-in replacement behind this same interface.
//
// This package is the per-dispatch half of this module's INTF-CCH-CCPOOL
// boundary crossing (docs/behavior/interfaces.md): starting, observing, and
// reaping a ccpool-backed handler session's own agent session. The other
// half — the once-per-process-lifetime preShutdown sweep across the default
// pool's prefix-matching sessions — lives in
// cmd/pg-router-ccpool-handler/preshutdown.go's teardownAllSessions.
package ccpool

import (
	"context"
	"errors"
	"time"
)

// ErrPromptNotIngested mirrors ccpool's exit code 7: a fire-and-forget delivery
// whose model never started a turn within the confirm window (a dropped nudge).
var ErrPromptNotIngested = errors.New("ccpool: prompt not ingested")

// IsNotIngested reports whether err is (or wraps) a ccpool exit-code-7 outcome.
func IsNotIngested(err error) bool {
	if errors.Is(err, ErrPromptNotIngested) {
		return true
	}
	var ec exitCoder
	return errors.As(err, &ec) && ec.ExitCode() == 7
}

// ErrUsageLimited mirrors ccpool's exit code 8: `ccpool new`/`reply` declined to
// accept work because an account usage window (5-hour block or weekly limit) is
// at its limit. It is the race backstop for the capacity pre-check (the window
// can fill between that check and the launch): a caller treats it as "not right
// now", never as a launch failure.
var ErrUsageLimited = errors.New("ccpool: usage limit hit")

// IsUsageLimited reports whether err is (or wraps) a ccpool exit-code-8 outcome.
func IsUsageLimited(err error) bool {
	if errors.Is(err, ErrUsageLimited) {
		return true
	}
	var ec exitCoder
	return errors.As(err, &ec) && ec.ExitCode() == 8
}

// SessionState mirrors ccpool's store states — observed session FACTS only, not
// work judgments (ADR 0015). idle (Claude Stop hook: the turn ended) and errored
// (Claude StopFailure hook: an API error) are NOT "work done"/"work failed"; the
// orchestrator re-reads the bead to judge success/failure.
type SessionState string

const (
	StateStarting   SessionState = "starting"
	StateReady      SessionState = "ready"
	StateWorking    SessionState = "working"
	StateNeedsInput SessionState = "needs_input"
	StateIdle       SessionState = "idle"    // was "done": Claude Stop hook (turn ended)
	StateErrored    SessionState = "errored" // was "failed": Claude StopFailure hook (API error)
)

// Session is one row from `ccpool list --all --json`. A session is addressed by
// ExternalID; Name is an optional, non-unique display label (ADR 0015).
type Session struct {
	ExternalID      string            `json:"external_id"`
	Name            string            `json:"name"` // optional display label; nullable, non-unique
	ClaudeSessionID string            `json:"claude_session_id"`
	State           SessionState      `json:"state"`
	Live            bool              `json:"live"`            // tmux has-session (liveness, NOT a store state)
	TranscriptPath  string            `json:"transcript_path"` // consumed by chunk B (token observation)
	CWD             string            `json:"cwd"`             // session working path (for the budget watchdog's guarded reset)
	Meta            map[string]string `json:"meta,omitempty"`  // the pgrouter.* tags DispatchMeta stamps at dispatch (MetaKeyBead/MetaKeyRole/MetaKeyPool); absent/nil for a session ccpool never received meta for
	// CloseReason is WHY ccpool closed this session (ADR 0072): "" (still open),
	// idle_ttl, cap_eviction, operator, or handler (this module's own closes,
	// stamped by CLIRunner.Close). Distinguishes an eviction/external close from
	// one the handler itself initiated.
	CloseReason string `json:"close_reason"`
}

// Capacity mirrors ccpool's session.Capacity (ADR 0072): the pool's occupancy
// as `ccpool capacity --json` reports it. Free is what an admission gate
// consults before dispatch (a later packet's concern, not this one's).
type Capacity struct {
	MaxSessions int `json:"max_sessions"`
	Live        int `json:"live"`
	Preserved   int `json:"preserved"`
	Counted     int `json:"counted"`
	Free        int `json:"free"`
	// UsageLimit is non-nil while an account usage window (the 5-hour block or
	// the weekly limit) is at its limit; ccpool then reports Free == 0, so the
	// admission gate declines without reading this field. It is carried only so
	// the decline can say WHY and until when. Absent from older ccpool output.
	UsageLimit *UsageLimit `json:"usage_limit,omitempty"`
}

// UsageLimit mirrors ccpool's usagelimit.Limit JSON: which usage window is at its
// limit and when it resets.
type UsageLimit struct {
	Window   string    `json:"window"` // "five_hour" or "seven_day"
	UsedPct  float64   `json:"used_pct"`
	ResetsAt time.Time `json:"resets_at"`
}

type SendMode int

const (
	ModeNoWait    SendMode = iota // deliver and return immediately (orchestrator default)
	ModeInterrupt                 // cancel the current turn, then deliver
	ModeQueue                     // deliver into claude's native queue (fire-and-forget)
)

// Runner is the full ccpool capability surface pg-router needs. Sessions are
// addressed by external_id; Ensure also passes an optional display name (--name).
// Close takes purge: pg-router purges (it never resumes — continuity lives in
// bd), except that a per-bead worktree session is torn down in two phases
// (bead pg2-kqegi, INV-CCH-20): a non-purge Close first, the purge only once the
// worktree is removed, so an interrupted removal never strands a worktree no row
// leads to. Cancel is present only as a chunk-B seam (90/100% budget cancels).
type Runner interface {
	Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error
	Send(ctx context.Context, externalID, prompt string, mode SendMode) error
	Cancel(ctx context.Context, externalID string) error
	Close(ctx context.Context, externalID string, purge bool) error
	List(ctx context.Context) ([]Session, error)
	// Capacity reports the pool's current occupancy (ADR 0072), for an
	// admission gate to consult before dispatch (a later packet's concern).
	Capacity(ctx context.Context) (Capacity, error)
	// SetMeta upserts one session-metadata key (`ccpool meta set`). The handler
	// uses it to refresh the supervision lease (pgrouter.lease_until, INV-CCH-18)
	// and to mark a reclaimed orphan; it is an unconditional write, never a
	// compare-and-set (mutual exclusion is the handler's own flock).
	SetMeta(ctx context.Context, externalID, key, value string) error
}
