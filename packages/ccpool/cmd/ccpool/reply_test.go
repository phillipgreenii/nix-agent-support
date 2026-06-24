package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
)

func TestReplyExitCode(t *testing.T) {
	if c := replyExitCode(session.ErrBusy); c != 5 {
		t.Errorf("ErrBusy -> %d, want 5", c)
	}
	if c := replyExitCode(session.ErrCancelUnconfirmed); c != 6 {
		t.Errorf("ErrCancelUnconfirmed -> %d, want 6 (distinct from generic 1)", c)
	}
	if c := replyExitCode(errors.New("boom")); c != 1 {
		t.Errorf("other -> %d, want 1 (generic catch-all)", c)
	}
	// reply --interrupt wraps the cancel error as "interrupt: %w"; errors.Is must
	// see through the wrap so the unconfirmed-interrupt case still maps to 6.
	wrapped := fmt.Errorf("interrupt: %w", session.ErrCancelUnconfirmed)
	if c := replyExitCode(wrapped); c != 6 {
		t.Errorf("wrapped ErrCancelUnconfirmed -> %d, want 6", c)
	}
	// A confirmed dropped nudge gets its own distinct code 7 so pr-pool can
	// fail-fast and hand the bead back.
	if c := replyExitCode(session.ErrPromptNotIngested); c != 7 {
		t.Errorf("ErrPromptNotIngested -> %d, want 7", c)
	}
}
