package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// ErrBusy is returned by Send in ModeRefuseIfBusy when the session is not idle.
// Callers map it to the dedicated "busy" exit code (spec §12/§20).
var ErrBusy = errors.New("session busy")

// Send delivers prompt to the live session externalID and (unless ModeNoWait)
// blocks for the turn outcome, returning the assistant reply. Spec §8.3.
func (s *Service) Send(ctx context.Context, externalID, prompt string, mode Mode) (Result, error) {
	var res Result
	err := s.withLock(externalID, func() error {
		var e error
		res, e = s.sendLocked(ctx, externalID, prompt, mode)
		return e
	})
	return res, err
}

func (s *Service) sendLocked(ctx context.Context, externalID, prompt string, mode Mode) (Result, error) {
	tmuxName := TmuxName(s.d.Prefix, externalID)
	row, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("no such session %q", externalID)
	}
	if !s.d.Tmux.HasSession(tmuxName) {
		return Result{}, fmt.Errorf("session %q is not live (resume it first)", externalID)
	}

	idle := row.State == store.Ready || row.State == store.Idle
	switch mode {
	case ModeRefuseIfBusy:
		if !idle {
			return Result{State: row.State}, fmt.Errorf("session %q is busy (state=%s); use --interrupt or --queue-message: %w", externalID, row.State, ErrBusy)
		}
	case ModeInterrupt:
		if !idle {
			if err := s.cancelLocked(ctx, externalID); err != nil {
				return Result{}, fmt.Errorf("interrupt: %w", err)
			}
		}
	case ModeQueue:
		// no idle check — deliver into Claude's native input queue, fire-and-forget.
	}

	// Flip to working as a single generation-bumping write; snapshot THAT generation.
	if _, err := s.d.Store.Transition(ctx, externalID, store.Working, "", ""); err != nil {
		return Result{}, fmt.Errorf("mark working: %w", err)
	}
	since, err := s.currentGeneration(ctx, externalID)
	if err != nil {
		return Result{}, err
	}

	if err := s.deliverPrompt(externalID, tmuxName, prompt); err != nil {
		return Result{}, fmt.Errorf("deliver prompt: %w", err)
	}

	if mode == ModeNoWait || mode == ModeQueue {
		return Result{State: store.Working}, nil
	}

	out, err := s.d.Wait.Wait(ctx, externalID, since)
	if err != nil {
		return Result{}, fmt.Errorf("wait turn: %w", err)
	}
	return s.resolveOutcome(ctx, externalID, row.TranscriptPath, out)
}

// deliverPrompt clears the input line, then delivers the body via bracketed
// paste (leading-`/` space-guarded so a path/regex isn't run as a command,
// spec §8.3/§4), then submits with Enter. Each step is recorded to the event
// log as an ordered input action (nil-safe). The paste detail is a short note —
// NEVER the prompt body — so prompt contents stay out of the log.
func (s *Service) deliverPrompt(externalID, tmuxName, prompt string) error {
	body := guardLeadingSlash(prompt)
	if err := s.clearInput(tmuxName); err != nil {
		return err
	}
	_ = s.d.Events.Input(s.d.Now(), externalID, "clear-input", "C-u")
	if err := s.d.Tmux.Paste(tmuxName, body); err != nil {
		return err
	}
	_ = s.d.Events.Input(s.d.Now(), externalID, "paste", "prompt delivered")
	if err := s.d.Tmux.SendKeys(tmuxName, "Enter"); err != nil {
		return err
	}
	_ = s.d.Events.Input(s.d.Now(), externalID, "enter", "")
	return nil
}

// clearInput empties the prompt box so leftover text can't concatenate (spec §4).
// C-u is verified to clear the real TUI's INSERT-mode buffer (2026-06-11 live
// run: planted text was wiped, no concatenation). Harmless to the fake-claude.
func (s *Service) clearInput(tmuxName string) error {
	return s.d.Tmux.SendKeys(tmuxName, "C-u")
}

// guardLeadingSlash prepends a space when the first non-whitespace char is '/'
// so a message (path/regex) isn't interpreted as a slash-command (spec §4).
func guardLeadingSlash(prompt string) string {
	t := strings.TrimLeft(prompt, " \t")
	if strings.HasPrefix(t, "/") {
		return " " + prompt
	}
	return prompt
}

func (s *Service) resolveOutcome(ctx context.Context, externalID, transcriptPath string, out wait.Outcome) (Result, error) {
	if out.TimedOut {
		// AskUserQuestion fallback (spec §8.3 step 6): a dangling question fires
		// no Notification hook, so detect it from the transcript.
		if transcriptPath != "" {
			if awaiting, _ := s.d.Transcript.IsAwaitingInput(transcriptPath); awaiting {
				_, _ = s.d.Store.Transition(ctx, externalID, store.NeedsInput, "", "")
				// This transition fires no Notification hook (AskUserQuestion gap),
				// so the notifier must be driven here (spec §10).
				s.fireNotify(ctx, externalID, store.Working, store.NeedsInput)
				return Result{State: store.NeedsInput}, nil
			}
		}
		return Result{State: out.State, TimedOut: true}, nil
	}
	switch out.State {
	case store.Idle:
		reply := ""
		if transcriptPath != "" {
			reply, _ = s.d.Transcript.LastAssistantText(transcriptPath)
		}
		return Result{State: store.Idle, Reply: reply}, nil
	default: // needs_input, errored, etc.
		return Result{State: out.State}, nil
	}
}
