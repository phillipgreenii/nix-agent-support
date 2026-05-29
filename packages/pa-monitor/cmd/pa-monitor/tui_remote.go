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

	home, _ := os.UserHomeDir()
	cacheDir := filepath.Join(home, ".cache", "pa-monitor")
	errLog := &tui.ErrorLogger{CacheDir: cacheDir}

	model := tui.NewModel(tui.Options{
		Tree:                 &aggregate.Tree{},
		Poller:               rp,
		Interval:             cfg.RefreshInterval,
		CacheDir:             cacheDir,
		Reporter:             nil, // cmuxstatus driven by cmux-bridge, not the TUI
		SidebarIntervalTicks: cfg.CmuxSidebarIntervalTicks,
		ErrorLogger:          errLog,
		Version:              version,
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
		OnToggleAutoResume: func(enable bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c, err := rpcclient.Dial(ctx)
			if err != nil {
				errLog.LogString(fmt.Sprintf("remote SetAutoResume dial: %v", err))
				return
			}
			defer c.Close()
			if _, err := c.C.SetAutoResume(ctx, &pb.SetAutoResumeRequest{Enabled: enable}); err != nil {
				errLog.LogString(fmt.Sprintf("remote SetAutoResume(%v): %v", enable, err))
			}
		},
		OnManualNudge: func(selector string, cancel bool) {
			ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelCtx()
			c, err := rpcclient.Dial(ctx)
			if err != nil {
				errLog.LogString(fmt.Sprintf("remote nudge dial: %v", err))
				return
			}
			defer c.Close()
			if cancel {
				if _, err := c.C.NudgeCancel(ctx, &pb.NudgeCancelRequest{Selector: selector}); err != nil {
					errLog.LogString(fmt.Sprintf("remote NudgeCancel(%s): %v", selector, err))
				}
			} else {
				if _, err := c.C.NudgeQueue(ctx, &pb.NudgeQueueRequest{Selector: selector}); err != nil {
					errLog.LogString(fmt.Sprintf("remote NudgeQueue(%s): %v", selector, err))
				}
			}
		},
	})
	prog := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
