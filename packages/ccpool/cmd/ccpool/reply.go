package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/store"
	ct "github.com/phillipgreenii/claude-transcript"
)

// transcriptAdapter satisfies session.Transcript using the shared module.
type transcriptAdapter struct{}

func (transcriptAdapter) LastAssistantText(p string) (string, error) { return ct.LastAssistantText(p) }
func (transcriptAdapter) IsAwaitingInput(p string) (bool, error)     { return ct.IsAwaitingInput(p) }
func (transcriptAdapter) FirstMessageActivity(p string) (time.Time, bool) {
	return ct.LastMessageActivity(p)
}

func runReply(args []string) int {
	fs := flag.NewFlagSet("reply", flag.ExitOnError)
	noWait := fs.Bool("no-wait", false, "deliver and return immediately")
	queue := fs.Bool("queue-message", false, "deliver into Claude's native queue (fire-and-forget)")
	interrupt := fs.Bool("interrupt", false, "cancel the current turn, then deliver")
	confirmIngest := fs.Duration("confirm-ingest", 0, "after a fire-and-forget delivery, confirm the model started a turn within this window; exit 7 if not (0 = no check)")
	pos := parseInterspersed(fs, args) // flags may follow the positional external_id/prompt
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ccpool reply <external_id> <prompt> [--no-wait] [--queue-message] [--interrupt] [--confirm-ingest dur]")
		return 2
	}
	externalID := pos[0]
	prompt := pos[1]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	el := openEventLog(cfg)
	st, err := store.Open(cfg.DBPath, clock.Real{}, store.WithEventLog(el))
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()

	svc := session.New(newSessionDeps(cfg, st, el))

	cwd := cfg.Claude.DefaultCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// Resume if cold, then send.
	if _, err := svc.Ensure(context.Background(), externalID, cwd, cfg.Claude.DefaultModel, session.EnsureOpts{}); err != nil {
		fmt.Fprintln(os.Stderr, "ensure:", err)
		return 1
	}
	mode := session.ModeRefuseIfBusy
	switch {
	case *interrupt:
		mode = session.ModeInterrupt
	case *queue:
		mode = session.ModeQueue
	case *noWait:
		mode = session.ModeNoWait
	}
	res, err := svc.SendWithConfirm(context.Background(), externalID, prompt, mode, *confirmIngest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reply:", err)
		return replyExitCode(err)
	}
	// Fire-and-forget modes deliver and return immediately with no reply. Record a
	// pending turn keyed by a fresh turn-id and PRINT that id so the caller can
	// retrieve the reply later via `ccpool result <turn-id>` (pg2-12ko). The Stop
	// hook lazily stamps the transcript anchor onto this turn when it completes.
	if mode == session.ModeNoWait || mode == session.ModeQueue {
		turnID := uuid.NewString()
		if err := st.InsertTurn(context.Background(), store.Turn{TurnID: turnID, ExternalID: externalID, Prompt: prompt}); err != nil {
			fmt.Fprintln(os.Stderr, "reply:", err)
			return 1
		}
		fmt.Println(turnID)
		return 0
	}
	switch res.State {
	case store.Idle:
		fmt.Println(res.Reply)
		return 0
	case store.NeedsInput:
		fmt.Fprintln(os.Stderr, "needs input — attach with: ccpool attach", externalID)
		return 2
	case store.Errored:
		fmt.Fprintln(os.Stderr, "turn failed")
		return 3
	}
	if res.TimedOut {
		fmt.Fprintln(os.Stderr, "timed out")
		return 4
	}
	return 0
}

// replyExitCode maps a Send error to reply's CLI exit code. ErrBusy -> 5;
// ErrCancelUnconfirmed -> 6 (the `--interrupt`-could-not-confirm case — sendLocked
// wraps it as "interrupt: %w", so errors.Is sees through the wrap). 1 stays the
// generic catch-all: exit code 1 must not carry a specific meaning, so a
// distinguishable outcome gets its own code >=2 (mirrors standalone cancel's 6).
func replyExitCode(err error) int {
	switch {
	case errors.Is(err, session.ErrBusy):
		return 5
	case errors.Is(err, session.ErrCancelUnconfirmed):
		return 6
	case errors.Is(err, session.ErrPromptNotIngested):
		return 7
	default:
		return 1
	}
}
