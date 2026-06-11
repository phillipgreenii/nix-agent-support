package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
)

// ErrCancelUnconfirmed means the Escape burst did not visibly interrupt the
// turn. cancelLocked leaves the row `working` and returns this so callers fail
// safely (the CLI exits non-zero; send --interrupt aborts) rather than racing a
// possibly-live turn (spec §3.2).
var ErrCancelUnconfirmed = errors.New("cancel could not be confirmed (turn may still be running)")

const (
	escapeBurst   = 3                      // number of Escapes per cancel (spec §3.2; pinned §3.3)
	escapeSpacing = 200 * time.Millisecond // gap between Escapes
)

// interruptLanded reports whether the captured pane shows the turn stopped.
// "Interrupted" is the marker observed in the live 6/7 mid-turn run (spec §3.1).
// "declined" is a HYPOTHESIS for the AskUserQuestion-cancel case, to confirm (or
// drop/replace) against real Claude in Task 9 — the exact marker set is pinned
// there (spec §3.3 / §19). Correctness comes from the burst; this only gates the
// idle-vs-unconfirmed branch.
func interruptLanded(pane string) bool {
	return strings.Contains(pane, "Interrupted") || strings.Contains(pane, "declined")
}

// Cancel interrupts the current turn (Escape) and resets the session to idle.
// No Stop hook fires on a user interrupt (spec §4/§8.5), so Cancel resets state
// itself rather than waiting. It also clears the restored input buffer.
func (s *Service) Cancel(ctx context.Context, name string) error {
	return s.withLock(name, func() error { return s.cancelLocked(ctx, name) })
}

func (s *Service) cancelLocked(ctx context.Context, name string) error {
	tmuxName := s.d.Prefix + name
	if !s.d.Tmux.HasSession(tmuxName) {
		return fmt.Errorf("session %q is not live", name)
	}
	// Nothing to interrupt if already idle (standalone cancel on a ready/done
	// session); just normalize to ready without bursting/verifying.
	if row, ok, err := s.d.Store.GetByName(ctx, name); err == nil && ok &&
		(row.State == store.Ready || row.State == store.Done) {
		_, err := s.d.Store.Transition(ctx, name, store.Ready, "", "")
		return err
	}
	// Brute-force a burst of Escapes spanning the thinking->streaming window; a
	// single Escape missed 1/7 in live verification (spec §3.1/§3.2).
	for i := 0; i < escapeBurst; i++ {
		if i > 0 {
			s.sleep(escapeSpacing)
		}
		if err := s.d.Tmux.SendKeys(tmuxName, "Escape"); err != nil {
			return fmt.Errorf("send Escape: %w", err)
		}
	}
	if err := s.clearInput(tmuxName); err != nil {
		return err
	}
	// Verify the interrupt landed; do NOT falsely report idle on a miss.
	pane, err := s.d.Tmux.CapturePane(tmuxName)
	if err != nil {
		return fmt.Errorf("verify cancel: %w", err)
	}
	if !interruptLanded(pane) {
		return ErrCancelUnconfirmed // row left as-is (working); caller fails safely
	}
	_, err = s.d.Store.Transition(ctx, name, store.Ready, "", "")
	return err
}

// Close ends the local REPL: clear input, send /exit, wait briefly for the tmux
// session to vanish, else force-kill (spec §8.4). The conversation stays
// resumable; --purge additionally deletes the store row.
func (s *Service) Close(ctx context.Context, name string, purge bool) error {
	return s.withLock(name, func() error {
		tmuxName := s.d.Prefix + name
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
			return s.d.Store.Delete(ctx, name)
		}
		return nil
	})
}

// deliverCommand sends a raw slash-command (e.g. /exit) — NOT space-guarded,
// unlike a message (spec §8.4): clear input, paste the command, submit.
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
