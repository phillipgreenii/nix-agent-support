package session

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/phillipgreenii/ccpool/internal/pane"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// ErrCancelUnconfirmed means the Escape burst did not visibly interrupt the
// turn. cancelLocked leaves the row `working` and returns this so callers fail
// safely (the CLI exits non-zero; send --interrupt aborts) rather than racing a
// possibly-live turn.
var ErrCancelUnconfirmed = errors.New("cancel could not be confirmed (turn may still be running)")

const (
	// Escape burst, pinned empirically (not derived): a SINGLE Escape missed 1 of 7
	// live cancels, so the burst spans the thinking->streaming window instead of
	// betting on one keystroke. See cancelLocked for the measurement.
	escapeBurst   = 3                      // number of Escapes per cancel
	escapeSpacing = 200 * time.Millisecond // gap between Escapes

	// Pane-stability confirmation tunables (pg2-33gl fix). cancelLocked confirms a
	// cancel landed when cancelStableRun consecutive CapturePane reads are
	// byte-identical (the turn stopped animating), giving up after cancelMaxSamples
	// total reads. The stability window (cancelStableRun-1)*cancelStableInterval =
	// 1.2s exceeds the ~1s thinking-counter tick, so a LIVE turn (whose counter
	// ticks ≥1/s and whose glyph animates, or whose prose appends) can never
	// accumulate K identical reads; a stopped/rewound/idle pane is static and does.
	cancelStableInterval = 400 * time.Millisecond // gap between captures (I)
	cancelStableRun      = 4                      // identical consecutive reads to confirm (K)
	cancelMaxSamples     = 16                     // total captures before giving up (N) ≈ 6s budget
)

// confirmStable polls the pane until it is STATIC — cancelStableRun consecutive
// CapturePane reads are byte-identical — meaning the turn stopped animating, and
// the latest pane carries no live counter. It is render-independent: it does NOT
// look for "Interrupted", "⏺", "Thought for Ns", or any affordance string, so it
// works in BOTH the thinking-rewind path and the streaming-interrupt path (whose
// panes RETAIN those markers as static text) and is immune to a stale "Interrupted"
// line. Count-bounded (cancelMaxSamples), NOT clock-bounded: the loop never reads
// the clock, so it is deterministic under the frozen-Now / no-op-sleep test fakes.
func (s *Service) confirmStable(tmuxName string) (bool, error) {
	prev, err := s.d.Tmux.CapturePane(tmuxName)
	if err != nil {
		return false, fmt.Errorf("verify cancel: %w", err)
	}
	run := 1 // one sample so far
	for i := 1; i < cancelMaxSamples; i++ {
		s.sleep(cancelStableInterval) // nil-safe no-op in tests
		cur, err := s.d.Tmux.CapturePane(tmuxName)
		if err != nil {
			return false, fmt.Errorf("verify cancel: %w", err)
		}
		if cur == prev {
			run++
		} else {
			run = 1
			prev = cur
		}
		if run >= cancelStableRun && !pane.ReLiveCounter.MatchString(cur) {
			return true, nil
		}
	}
	return false, nil
}

// Cancel interrupts the current turn (Escape) and resets the session to ready.
// No Stop hook fires on a user interrupt (verified against Claude Code 2.1.170),
// so Cancel resets state itself rather than waiting for a Stop that never comes.
// It also clears the input buffer, into which the interrupt restores the
// cancelled prompt.
func (s *Service) Cancel(ctx context.Context, externalID string) error {
	return s.withLock(externalID, func() error { return s.cancelLocked(ctx, externalID) })
}

func (s *Service) cancelLocked(ctx context.Context, externalID string) error {
	tmuxName := TmuxName(s.d.Prefix, externalID)
	if !s.d.Tmux.HasSession(tmuxName) {
		return fmt.Errorf("session %q is not live", externalID)
	}
	// Nothing to interrupt if already idle (standalone cancel on a ready/idle
	// session); just normalize to ready without bursting/verifying.
	if row, ok, err := s.d.Store.GetByExternalID(ctx, externalID); err == nil && ok &&
		(row.State == store.Ready || row.State == store.Idle) {
		_, err := s.d.Store.Transition(ctx, externalID, store.Ready, "", "")
		return err
	}
	// Brute-force a burst of Escapes spanning the thinking->streaming window; a
	// single Escape missed 1/7 in live verification (2026-06-11, real Claude on an
	// isolated socket; the miss landed in the thinking->streaming transition).
	for i := 0; i < escapeBurst; i++ {
		if i > 0 {
			s.sleep(escapeSpacing)
		}
		if err := s.d.Tmux.SendKeys(tmuxName, "Escape"); err != nil {
			return fmt.Errorf("send Escape: %w", err)
		}
	}
	// Record the Escape burst as ONE ordered input action (detail = the count).
	_ = s.d.Events.Input(s.d.Now(), externalID, "escape-burst", strconv.Itoa(escapeBurst))
	if err := s.clearInput(tmuxName); err != nil {
		return err
	}
	// Confirm the turn actually stopped by polling until the pane goes STATIC
	// (pane-stability), instead of grepping for an "Interrupted" marker that the
	// thinking-rewind path never prints and that stale scrollback false-matches.
	// Do NOT falsely report idle on a miss.
	confirmed, err := s.confirmStable(tmuxName)
	if err != nil {
		return err
	}
	if !confirmed {
		return ErrCancelUnconfirmed // row left as-is (working); caller fails safely
	}
	_, err = s.d.Store.Transition(ctx, externalID, store.Ready, "", "")
	return err
}

// Close ends the local REPL: clear input, send /exit, wait briefly for the tmux
// session to vanish, else force-kill. The row is NOT mutated on a non-purge
// close — ccpool no longer fabricates a settled state (ADR 0015); the
// row keeps its last OBSERVED state and is pruned later, once the Claude session
// is gone from disk. --purge additionally deletes the store row immediately.
func (s *Service) Close(ctx context.Context, externalID string, purge bool) error {
	return s.withLock(externalID, func() error {
		tmuxName := TmuxName(s.d.Prefix, externalID)
		if s.d.Tmux.HasSession(tmuxName) {
			// deliverCommand clears the input line itself, so no separate clear here.
			if err := s.deliverCommand(tmuxName, "/exit"); err != nil {
				return err
			}
			if !s.waitGone(tmuxName, 3*time.Second) {
				if err := s.d.Tmux.KillSession(tmuxName); err != nil {
					return fmt.Errorf("force kill: %w", err)
				}
			}
		}
		if purge {
			return s.d.Store.Delete(ctx, externalID)
		}
		// Non-purge close: do nothing else. No fabricated state.
		return nil
	})
}

// deliverCommand sends a raw slash-command (e.g. /exit) — NOT space-guarded,
// unlike a message: clear input, paste the command, submit.
func (s *Service) deliverCommand(tmuxName, cmd string) error {
	if err := s.clearInput(tmuxName); err != nil {
		return err
	}
	if err := s.d.Tmux.Paste(tmuxName, cmd); err != nil {
		return err
	}
	return s.d.Tmux.SendKeys(tmuxName, "Enter")
}

// waitGone polls liveness until the session disappears or the budget elapses.
func (s *Service) waitGone(tmuxName string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !s.d.Tmux.HasSession(tmuxName) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !s.d.Tmux.HasSession(tmuxName)
}
