// Package ccpool is pr-pool's seam onto the ccpool session manager. ALL session
// mechanics flow through Runner. The Phase-1 implementation (cli.go) shells out
// to the `ccpool` CLI; a future in-process implementation wrapping ccpool's
// session.Service is a drop-in replacement behind this same interface.
package ccpool

import (
	"context"
	"errors"
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
	ExternalID      string       `json:"external_id"`
	Name            string       `json:"name"` // optional display label; nullable, non-unique
	ClaudeSessionID string       `json:"claude_session_id"`
	State           SessionState `json:"state"`
	Live            bool         `json:"live"`            // tmux has-session (liveness, NOT a store state)
	TranscriptPath  string       `json:"transcript_path"` // consumed by chunk B (token observation)
	CWD             string       `json:"cwd"`             // session working path (for the budget watchdog's guarded reset)
}

type SendMode int

const (
	ModeNoWait    SendMode = iota // deliver and return immediately (orchestrator default)
	ModeInterrupt                 // cancel the current turn, then deliver
	ModeQueue                     // deliver into claude's native queue (fire-and-forget)
)

// Runner is the full ccpool capability surface pr-pool needs. Sessions are
// addressed by external_id; Ensure also passes an optional display name (--name).
// Close takes purge: pr-pool always purges (it never resumes — continuity lives
// in bd). Cancel is present only as a chunk-B seam (90/100% budget cancels).
type Runner interface {
	Ensure(ctx context.Context, externalID, name, cwd string, env, meta map[string]string) error
	Send(ctx context.Context, externalID, prompt string, mode SendMode) error
	Cancel(ctx context.Context, externalID string) error
	Close(ctx context.Context, externalID string, purge bool) error
	List(ctx context.Context) ([]Session, error)
}
