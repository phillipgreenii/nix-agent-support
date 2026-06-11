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
)

func runCancel(args []string) int {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: ccpool cancel <name>")
		return 2
	}
	svc, st, code := buildService()
	if code != 0 {
		return code
	}
	defer st.Close()
	if err := svc.Cancel(context.Background(), fs.Arg(0)); err != nil {
		if errors.Is(err, session.ErrCancelUnconfirmed) {
			fmt.Fprintf(os.Stderr, "cancel may not have landed for %q — re-run `ccpool cancel %s` or `ccpool attach %s`\n",
				fs.Arg(0), fs.Arg(0), fs.Arg(0))
		} else {
			fmt.Fprintln(os.Stderr, "cancel:", err)
		}
		return cancelExitCode(err)
	}
	return 0
}

// cancelExitCode maps a cancel error to its CLI exit code (spec §20 + 6=unconfirmed).
func cancelExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, session.ErrCancelUnconfirmed):
		return 6
	default:
		return 1
	}
}

// buildService constructs a session.Service wired with real adapters + lock.
// Shared by cancel/close (and usable by new/reply if refactored later).
func buildService() (*session.Service, *store.Store, int) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return nil, nil, 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return nil, nil, 1
	}
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
		Sleep:      time.Sleep,
	})
	return svc, st, 0
}
