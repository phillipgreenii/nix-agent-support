package nudger

import (
	"context"
)

// Deliverer delivers a nudge's text to a target PID. The call blocks: Deliver
// returns once the outcome is known, or ctx is done, or an
// implementation-defined timeout elapses.
type Deliverer interface {
	Deliver(ctx context.Context, targetPID int, text string) error
}
