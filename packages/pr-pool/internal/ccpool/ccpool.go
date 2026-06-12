// Package ccpool is pr-pool's seam onto the ccpool session manager. ALL session
// mechanics flow through Runner. The Phase-1 implementation (cli.go) shells out
// to the `ccpool` CLI; a future in-process implementation wrapping ccpool's
// session.Service is a drop-in replacement behind this same interface.
package ccpool

import "context"

// SessionState mirrors ccpool's store states.
type SessionState string

const (
	StateStarting   SessionState = "starting"
	StateReady      SessionState = "ready"
	StateWorking    SessionState = "working"
	StateNeedsInput SessionState = "needs_input"
	StateDone       SessionState = "done"
	StateFailed     SessionState = "failed"
)

// Session is one row from `ccpool list --all --json`.
type Session struct {
	Name           string       `json:"name"`
	State          SessionState `json:"state"`
	Live           bool         `json:"live"`            // tmux has-session (liveness, NOT a store state)
	TranscriptPath string       `json:"transcript_path"` // consumed by chunk B (token observation)
	CWD            string       `json:"cwd"`             // session working path (for the budget watchdog's guarded reset)
}

type SendMode int

const (
	ModeNoWait    SendMode = iota // deliver and return immediately (orchestrator default)
	ModeInterrupt                 // cancel the current turn, then deliver
	ModeQueue                     // deliver into claude's native queue (fire-and-forget)
)

// Runner is the full ccpool capability surface pr-pool needs. Cancel is present
// only as a chunk-B seam (90/100% budget cancels); Phase-1 never calls it.
type Runner interface {
	Ensure(ctx context.Context, name, cwd string, env map[string]string) error
	Send(ctx context.Context, name, prompt string, mode SendMode) error
	Cancel(ctx context.Context, name string) error
	Close(ctx context.Context, name string) error
	List(ctx context.Context) ([]Session, error)
}
