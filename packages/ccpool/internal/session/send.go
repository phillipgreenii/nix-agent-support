package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/wait"
)

// ErrBusy is returned by Send in ModeRefuseIfBusy when the session is not idle.
// Callers map it to the dedicated "busy" exit code 5 (see replyExitCode).
var ErrBusy = errors.New("session busy")

// ErrPromptNotIngested is returned by SendWithConfirm when an ingestion-confirmed
// delivery (a non-zero confirm window) delivers the prompt but the session never
// starts a turn within the window — i.e. the paste/Enter did not reach the model
// (a dropped initial nudge). Callers map it to the dedicated exit code 7. This is
// DISTINCT from a turn that started but timed out (TimedOut) or refused (ErrBusy).
var ErrPromptNotIngested = errors.New("prompt delivered but model never started a turn (not ingested)")

// ErrIngestUnknown is returned by SendWithConfirm when the ingestion confirmation
// could not reach a verdict: the recorder was unreachable or its feed failed while
// waiting, or it could not be reached once the window passed with no record. The
// prompt was delivered and the model may well be working (a recorder that lags
// under load looks the same), so callers MUST NOT treat this as a dropped prompt.
// Callers map it to the dedicated exit code 9. Only ErrPromptNotIngested, returned
// with the recorder confirmed reachable, is a definitive "not ingested".
var ErrIngestUnknown = errors.New("could not confirm whether the prompt was ingested (recorder unavailable)")

// Send delivers prompt to the live session externalID and (unless ModeNoWait)
// blocks for the turn outcome, returning the assistant reply.
func (s *Service) Send(ctx context.Context, externalID, prompt string, mode Mode) (Result, error) {
	var res Result
	err := s.withLock(externalID, func() error {
		var e error
		res, e = s.sendLocked(ctx, externalID, prompt, mode)
		return e
	})
	return res, err
}

// confirmIngestPoll is the gap between transcript checks while confirming the
// model started a turn. Small relative to the caller window so the bound is the
// window, not the poll granularity.
const confirmIngestPoll = 250 * time.Millisecond

// SendWithConfirm is Send with a post-delivery ingestion guard. When window > 0
// and mode is a fire-and-forget mode (ModeNoWait/ModeQueue), after delivering the
// prompt it polls the session transcript for a first model turn; if none appears
// within window it returns ErrPromptNotIngested (the dropped-nudge case). A zero
// window, or a waiting mode, behaves exactly like Send.
func (s *Service) SendWithConfirm(ctx context.Context, externalID, prompt string, mode Mode, window time.Duration) (Result, error) {
	var res Result
	err := s.withLock(externalID, func() error {
		var e error
		// With a feed-backed Transcript, take the feed position BEFORE delivery so
		// the confirmation waits for a record written after it (not a whole-file
		// re-read every 250 ms, and not a message from an earlier turn).
		var mark string
		fw, feed := s.d.Transcript.(FeedWaiter)
		if feed && window > 0 && (mode == ModeNoWait || mode == ModeQueue) {
			if mark, e = fw.Mark(ctx); e != nil {
				return fmt.Errorf("confirm ingest: %w", e)
			}
		} else {
			feed = false
		}
		res, e = s.sendLocked(ctx, externalID, prompt, mode)
		if e != nil {
			return e
		}
		if feed {
			return s.confirmIngestedFeed(ctx, fw, externalID, mark, window)
		}
		if window > 0 && (mode == ModeNoWait || mode == ModeQueue) {
			return s.confirmIngested(ctx, externalID, window)
		}
		return nil
	})
	return res, err
}

// FeedWaiter is the optional Transcript extension backed by the recorder's change
// feed: Mark returns the feed position now, and WaitMessage waits (long polling)
// until window elapses for a real user or assistant message of the transcript
// written after mark. The fact is "a record appeared"; ccpool decides what it
// means.
type FeedWaiter interface {
	Mark(ctx context.Context) (string, error)
	WaitMessage(ctx context.Context, transcriptPath, mark string, window time.Duration) (bool, error)
}

// confirmIngestedFeed is confirmIngested on the feed: one wait that returns on the
// matching record, ErrPromptNotIngested when the window passes without one while
// the recorder still answers, or ErrIngestUnknown when the recorder cannot say.
func (s *Service) confirmIngestedFeed(ctx context.Context, fw FeedWaiter, externalID, mark string, window time.Duration) error {
	row, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
	if err != nil {
		return fmt.Errorf("confirm ingest: %w", err)
	}
	if !ok || row.TranscriptPath == "" {
		// No transcript path stamped yet: no turn observed (the safe direction).
		return ErrPromptNotIngested
	}
	started, err := fw.WaitMessage(ctx, row.TranscriptPath, mark, window)
	if err != nil {
		// The feed failed: nothing was observed either way.
		return fmt.Errorf("%w: %v", ErrIngestUnknown, err)
	}
	if !started {
		// A window that passes with no record proves "not ingested" only if the
		// recorder is itself answering; a recorder that lags or is down looks the
		// same. Probe it (a fresh feed position) before giving the verdict.
		if _, perr := fw.Mark(ctx); perr != nil {
			return fmt.Errorf("%w: no record within %s and the recorder did not answer afterwards: %v", ErrIngestUnknown, window, perr)
		}
		return ErrPromptNotIngested
	}
	return nil
}

// confirmIngested polls the session's transcript until a real message event
// appears (the model started a turn) or the window's worth of polls elapses.
// Returns nil on the first observed turn, ErrPromptNotIngested otherwise. A row
// without a transcript path (the hook has not stamped one yet) is tolerated — the
// resolver returns (zero,false) and we keep polling until the bound, then fail (no
// transcript ⇒ no turn observed ⇒ treat as not ingested, the safe direction for a
// worker). Bounded by iteration count (window/poll, min 1) so a fixed test clock
// still terminates; production uses the same bound with a real injected sleep so
// the wall time is ~window.
func (s *Service) confirmIngested(ctx context.Context, externalID string, window time.Duration) error {
	iters := int(window / confirmIngestPoll)
	if iters < 1 {
		iters = 1
	}
	for i := 0; i < iters; i++ {
		row, ok, err := s.d.Store.GetByExternalID(ctx, externalID)
		if err != nil {
			return fmt.Errorf("confirm ingest: %w", err)
		}
		if ok && row.TranscriptPath != "" {
			if _, started := s.d.Transcript.FirstMessageActivity(row.TranscriptPath); started {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		s.sleep(confirmIngestPoll)
	}
	return ErrPromptNotIngested
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

	if err := s.deliverPrompt(ctx, externalID, tmuxName, row.ClaudeSessionID, prompt); err != nil {
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
// paste (leading-`/` space-guarded so a path/regex isn't run as a command), then
// submits with Enter. Each step is recorded to the event log as an ordered input
// action (nil-safe). The paste detail is a short note — NEVER the prompt body —
// so prompt contents stay out of the log.
func (s *Service) deliverPrompt(ctx context.Context, externalID, tmuxName, claudeSessionID, prompt string) error {
	body := guardLeadingSlash(prompt)
	if s.d.Control != nil && claudeSessionID != "" {
		err := s.d.Control.Submit(ctx, claudeSessionID, body)
		if err == nil {
			_ = s.d.Events.Input(s.d.Now(), externalID, "control-submit", "prompt delivered")
			return nil
		}
		if !errors.Is(err, ErrNoControl) {
			return err
		}
	}
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

// clearInput empties the prompt box so leftover text can't concatenate with the
// next send (leftover text broke a /exit during the design spike).
// C-u is verified to clear the real TUI's INSERT-mode buffer (2026-06-11 live
// run: planted text was wiped, no concatenation). Harmless to the fake-claude.
func (s *Service) clearInput(tmuxName string) error {
	return s.d.Tmux.SendKeys(tmuxName, "C-u")
}

// guardLeadingSlash prepends a space when the first non-whitespace char is '/'
// so a message (path/regex) isn't interpreted as a slash-command. Verified
// against Claude Code 2.1.170: a leading-'/' body is treated as a command even
// when PASTED, yielding `Unknown command` and firing NO Stop — so the waiter
// would hang to timeout. A single leading space delivers it as a normal message.
// Intentional commands take deliverCommand instead, which is NOT guarded.
func guardLeadingSlash(prompt string) string {
	t := strings.TrimLeft(prompt, " \t")
	if strings.HasPrefix(t, "/") {
		return " " + prompt
	}
	return prompt
}

func (s *Service) resolveOutcome(ctx context.Context, externalID, transcriptPath string, out wait.Outcome) (Result, error) {
	if out.TimedOut {
		// AskUserQuestion fallback: a dangling question fires no Notification
		// hook, so detect it from the transcript.
		if transcriptPath != "" {
			if awaiting, _ := s.d.Transcript.IsAwaitingInput(transcriptPath); awaiting {
				_, _ = s.d.Store.Transition(ctx, externalID, store.NeedsInput, "", "")
				// This transition fires no Notification hook (AskUserQuestion gap),
				// so the notifier must be driven here.
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
