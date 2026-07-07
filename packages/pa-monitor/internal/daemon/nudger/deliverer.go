package nudger

import (
	"context"
	"errors"
)

// Deliverer delivers a nudge's text to a target PID. Implementations may be
// synchronous (the existing in-daemon signal path, e.g. tmux/ghostty/vscode)
// or asynchronous (routed through a cmux-bridge stream and awaiting an ack),
// but both present the same blocking-call shape to callers: Deliver returns
// once the outcome is known, or ctx is done, or an implementation-defined
// timeout elapses.
type Deliverer interface {
	Deliver(ctx context.Context, targetPID int, text string) error
}

// ErrNoBridge indicates that targetPID has no live cmux-bridge to deliver
// through: either it has no resolvable cmux server ancestor, or that
// server's bridge is not currently connected.
var ErrNoBridge = errors.New("no live bridge for target")
