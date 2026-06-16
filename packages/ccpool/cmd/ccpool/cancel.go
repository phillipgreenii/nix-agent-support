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
	"github.com/phillipgreenii/ccpool/internal/eventlog"
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

// buildService constructs a session.Service for the active pool (config.Load reads
// CCPOOL_POOL). Shared by cancel/close/reap.
func buildService() (*session.Service, *store.Store, int) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return nil, nil, 1
	}
	return buildServiceFor(cfg)
}

// buildServiceFor wires a session.Service from an explicit Config, so reap-all can
// govern many pools in one process — loading each pool's Config via
// config.LoadForPool and building its service without mutating CCPOOL_POOL.
func buildServiceFor(cfg config.Config) (*session.Service, *store.Store, int) {
	// Open the append-only event log; never block the command on a logging
	// failure (mirrors the hook's never-fail policy — a nil Logger is a no-op).
	el := openEventLog(cfg)
	st, err := store.Open(cfg.DBPath, clock.Real{}, store.WithEventLog(el))
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
		Events:     el,
		Socket:     cfg.Tmux.Socket,
		Prefix:     cfg.Tmux.Prefix,
		PluginDir:  cfg.Claude.PluginDir,
		ClaudeBin:  cfg.Claude.Bin,
		PoolPath:   cfg.PoolRoot,
		NewUUID:    func() string { return uuid.NewString() },
		Now:        time.Now,
		Sleep:      time.Sleep,
	})
	return svc, st, 0
}

// openEventLog opens the active pool's append-only JSONL event log. A failure is
// non-fatal — it logs to stderr and returns nil (a nil *eventlog.Logger is a
// no-op), so event logging never blocks a command (cf. the hook's never-fail
// policy). Wire the result into both store.WithEventLog and session.Deps.Events.
func openEventLog(cfg config.Config) *eventlog.Logger {
	el, err := eventlog.Open(cfg.EventLogPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "event log:", err)
		return nil
	}
	return el
}
