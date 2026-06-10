package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/lock"
	"github.com/phillipgreenii/ccpool/internal/notify"
	"github.com/phillipgreenii/ccpool/internal/session"
	"github.com/phillipgreenii/ccpool/internal/store"
	"github.com/phillipgreenii/ccpool/internal/tmux"
	ct "github.com/phillipgreenii/claude-transcript"
)

// transcriptAdapter satisfies session.Transcript using the shared module.
type transcriptAdapter struct{}

func (transcriptAdapter) LastAssistantText(p string) (string, error) { return ct.LastAssistantText(p) }
func (transcriptAdapter) IsAwaitingInput(p string) (bool, error)     { return ct.IsAwaitingInput(p) }

func runReply(args []string) int {
	fs := flag.NewFlagSet("reply", flag.ExitOnError)
	noWait := fs.Bool("no-wait", false, "deliver and return immediately")
	queue := fs.Bool("queue-message", false, "deliver into Claude's native queue (fire-and-forget)")
	interrupt := fs.Bool("interrupt", false, "cancel the current turn, then deliver")
	pos := parseInterspersed(fs, args) // flags may follow the positional name/prompt
	if len(pos) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ccpool reply <name> <prompt> [--no-wait] [--queue-message] [--interrupt]")
		return 2
	}
	name := pos[0]
	prompt := pos[1]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()

	home, _ := os.UserHomeDir()
	svc := session.New(session.Deps{
		Tmux:       tmux.NewClient(cfg.Tmux.Socket),
		Trust:      truster{path: filepath.Join(home, ".claude.json")},
		Store:      st,
		Wait:       storeWaiter{st: st, timeout: time.Duration(cfg.Wait.Timeout)},
		Transcript: transcriptAdapter{},
		Lock:       lock.New(cfg.RuntimeDir),
		Notify:     notify.FromConfig(cfg.Notify.Adapter, cfg.Notify.Command),
		NotifyOn:   cfg.Notify.On,
		Socket:     cfg.Tmux.Socket,
		Prefix:     cfg.Tmux.Prefix,
		PluginDir:  cfg.Claude.PluginDir,
		ClaudeBin:  cfg.Claude.Bin,
		NewUUID:    func() string { return uuid.NewString() },
		Now:        time.Now,
	})

	cwd := cfg.Claude.DefaultCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// Resume if cold, then send.
	if _, err := svc.Ensure(context.Background(), name, cwd, cfg.Claude.DefaultModel); err != nil {
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
	res, err := svc.Send(context.Background(), name, prompt, mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reply:", err)
		if errors.Is(err, session.ErrBusy) {
			return 5 // dedicated "busy" exit code (spec §12/§20)
		}
		return 1
	}
	switch res.State {
	case store.Done:
		fmt.Println(res.Reply)
		return 0
	case store.NeedsInput:
		fmt.Fprintln(os.Stderr, "needs input — attach with: ccpool attach", name)
		return 2
	case store.Failed:
		fmt.Fprintln(os.Stderr, "turn failed")
		return 3
	}
	if res.TimedOut {
		fmt.Fprintln(os.Stderr, "timed out")
		return 4
	}
	return 0
}
