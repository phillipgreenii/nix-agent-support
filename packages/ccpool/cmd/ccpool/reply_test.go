package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/x/exitcode"
)

func TestReplyExitCode(t *testing.T) {
	if c := replyExitCode(session.ErrBusy); c != 23 {
		t.Errorf("ErrBusy -> %d, want 23", c)
	}
	if c := replyExitCode(session.ErrCancelUnconfirmed); c != 24 {
		t.Errorf("ErrCancelUnconfirmed -> %d, want 24 (distinct from generic 1)", c)
	}
	if c := replyExitCode(errors.New("boom")); c != 1 {
		t.Errorf("other -> %d, want 1 (generic catch-all)", c)
	}
	// reply --interrupt wraps the cancel error as "interrupt: %w"; errors.Is must
	// see through the wrap so the unconfirmed-interrupt case still maps to 24.
	wrapped := fmt.Errorf("interrupt: %w", session.ErrCancelUnconfirmed)
	if c := replyExitCode(wrapped); c != 24 {
		t.Errorf("wrapped ErrCancelUnconfirmed -> %d, want 24", c)
	}
	// A confirmed dropped nudge gets its own distinct code 25 so pg-router can
	// fail-fast and hand the bead back.
	if c := replyExitCode(session.ErrPromptNotIngested); c != 25 {
		t.Errorf("ErrPromptNotIngested -> %d, want 25", c)
	}
	// A recorder that could not say whether the prompt was ingested gets exit 26,
	// distinct from the definitive 25.
	if c := replyExitCode(fmt.Errorf("confirm ingest: %w", session.ErrIngestUnknown)); c != 26 {
		t.Errorf("ErrIngestUnknown -> %d, want 26", c)
	}
}

// The reply, cancel and result exit codes are a contract with pg-router-handlers
// and pg-review (both pin the same numbers): app codes sit in 20..63, a timeout
// is the shared Timeout 6, and no refusal uses the shared Busy 9.
func TestExitCodeValues(t *testing.T) {
	for name, c := range map[string][2]int{
		"usage_limited":     {exitUsageLimited, 20},
		"needs_input":       {exitNeedsInput, 21},
		"turn_failed":       {exitTurnFailed, 22},
		"session_busy":      {exitSessionBusy, 23},
		"cancel_unconfirm":  {exitCancelUnconfirmed, 24},
		"prompt_not_ingest": {exitPromptNotIngested, 25},
		"ingest_unknown":    {exitIngestUnknown, 26},
		"result_pending":    {exitResultPending, 27},
		"timeout":           {exitcode.Timeout, 6},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %d, want %d", name, c[0], c[1])
		}
		if name != "timeout" && (c[0] < exitcode.AppMin || c[0] > exitcode.AppMax) {
			t.Errorf("%s = %d outside the app range", name, c[0])
		}
	}
}
