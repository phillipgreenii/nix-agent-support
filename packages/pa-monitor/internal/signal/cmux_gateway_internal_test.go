package signal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestNewCmuxError_TimedOutFromContext guards the PRIMARY (production) timeout-
// detection clauses of newCmuxError: an expired run context, or an error that
// wraps context.DeadlineExceeded, must set TimedOut independently of the
// string-signature fallback. The exported Send path can only reach the string
// fallback in tests (Send builds its own fresh 5s context and the injected
// RunCmd returns synthetic errors), so without a whitebox test the ctx-threading
// clause is unexercised and a regression (ctx not threaded, wrong sentinel)
// would pass CI while silently breaking timeout detection for a real
// deadline-exceeded exec. (pg2-gweng, from the pg2-b4nx review.)
func TestNewCmuxError_TimedOutFromContext(t *testing.T) {
	// Clause (a): ctx.Err() == DeadlineExceeded, with an error text that has NO
	// timeout signature, so TimedOut can ONLY come from the context — isolating
	// the ctx-threading path from the string fallback.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	if !errors.Is(expired.Err(), context.DeadlineExceeded) {
		t.Fatalf("precondition: expired ctx.Err() = %v, want DeadlineExceeded", expired.Err())
	}
	if ce := newCmuxError(expired, CmuxSend, errors.New("exit status 1")); !ce.TimedOut {
		t.Error("clause (a): expected TimedOut=true from an expired context, got false")
	}

	// Clause (b): fresh ctx, but the error wraps context.DeadlineExceeded.
	if ce := newCmuxError(context.Background(), CmuxSend, fmt.Errorf("run cmux: %w", context.DeadlineExceeded)); !ce.TimedOut {
		t.Error("clause (b): expected TimedOut=true from err wrapping DeadlineExceeded, got false")
	}

	// Negative guard: fresh ctx + a non-timeout error text must NOT be flagged
	// (the over-correction guard, mirroring the daemon exit-status-1 case).
	if ce := newCmuxError(context.Background(), CmuxSend, errors.New("exit status 1")); ce.TimedOut {
		t.Error("expected TimedOut=false for a plain non-timeout error, got true")
	}
}
