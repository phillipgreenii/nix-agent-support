package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/phillipgreenii/pa-monitor/internal/config"
	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
	"github.com/phillipgreenii/pa-monitor/internal/core/caffeinate"
	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
	"github.com/phillipgreenii/pa-monitor/internal/tui"
)

// runTUIRemote launches the TUI against a running daemon over gRPC.
// The local poller is bypassed; all state comes from GetState calls.
//
// This mode is intentionally minimal: the existing TUI Model is unchanged
// and consumes the aggregate.Tree the RemotePoller reconstructs from the
// wire DaemonState. The daemon owns caffeinate, nudge dispatch, and
// session lifecycle.
func runTUIRemote() {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}

	rp, err := rpcclient.NewRemotePoller()
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote poller: %v\n", err)
		os.Exit(2)
	}

	// Caffeinate manager is daemon-owned in remote mode; the local
	// model still constructs one (the TUI's render path expects it),
	// but its Spawn/Kill/IsAlive are no-ops because the user's
	// `C` keybinding in remote mode should drive the daemon instead.
	// Full remote-caffeinate wiring is a follow-up; for now the local
	// stub keeps the render happy.
	noop := func() error { return nil }
	noopSpawn := func(int) error { return nil }
	mgr := &caffeinate.Manager{
		Grace:   cfg.CaffeinateGrace,
		Spawn:   noopSpawn,
		Kill:    noop,
		IsAlive: func() bool { return false },
		Now:     time.Now,
		PID:     os.Getpid(),
	}

	home, _ := os.UserHomeDir()
	cacheDir := filepath.Join(home, ".cache", "pa-monitor")
	errLog := &tui.ErrorLogger{CacheDir: cacheDir}

	model := tui.NewModel(tui.Options{
		Tree:                 &aggregate.Tree{},
		Poller:               rp,
		Interval:             cfg.RefreshInterval,
		Caffeinate:           mgr,
		CacheDir:             cacheDir,
		Signalers:            nil, // daemon dispatches nudges
		AutoResumeDelay:      cfg.AutoResumeDelay,
		AutoResumeMessage:    cfg.AutoResumeMessage,
		Reporter:             nil, // cmuxstatus driven by cmux-bridge, not the TUI
		SidebarIntervalTicks: cfg.CmuxSidebarIntervalTicks,
		ErrorLogger:          errLog,
		OnCaffeinateToggle: func(on bool) {
			action := "off"
			if on {
				action = "on"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c, err := rpcclient.Dial(ctx)
			if err != nil {
				errLog.LogString(fmt.Sprintf("remote caffeinate dial: %v", err))
				return
			}
			defer c.Close()
			if _, err := c.C.Caffeinate(ctx, &pb.CaffeinateRequest{Action: action}); err != nil {
				errLog.LogString(fmt.Sprintf("remote caffeinate %s: %v", action, err))
			}
		},
	})
	prog := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
