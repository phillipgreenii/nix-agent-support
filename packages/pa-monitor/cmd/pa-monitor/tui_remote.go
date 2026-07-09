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
	"github.com/phillipgreenii/pa-monitor/internal/otel"
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

	config.ApplyOTelEnv(cfg.OTel)
	emitCtx, emitCancel := context.WithCancel(context.Background())
	defer emitCancel()
	connEmit, emitErr := otel.NewConnectionEmitter(emitCtx, otel.ConnOptions{
		ServiceName:    "pa-monitor",
		ServiceVersion: version,
		Component:      "tui",
	})
	if emitErr != nil {
		connEmit = nil
	}
	defer connEmit.Shutdown(emitCtx)

	// Sample the poller's connection state on a ticker and publish the gauge.
	// IsOffline() is reliable: every backoff in RemotePoller is preceded by
	// client=nil, so client==nil holds throughout a disconnect window.
	go func() {
		const sample = 10 * time.Second
		t := time.NewTicker(sample)
		defer t.Stop()
		announced := false
		for {
			select {
			case <-emitCtx.Done():
				return
			case <-t.C:
				connected := !rp.IsOffline()
				connEmit.RecordDaemonConnected(connected)
				if !connected && !announced {
					announced = true
					connEmit.LogEvent("daemon.disconnect", map[string]string{"component": "tui"})
				}
				if connected && announced {
					announced = false
					connEmit.LogEvent("daemon.reconnect", map[string]string{"component": "tui"})
				}
			}
		}
	}()

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
		StaleAfter:           cfg.StaleAfter,
		OnCaffeinateToggle: func(want bool) tea.Cmd {
			action := "off"
			if want {
				action = "on"
			}
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				c, err := rpcclient.Dial(ctx)
				if err != nil {
					return tui.CaffeinateErrMsg{Err: fmt.Errorf("dial: %w", err)}
				}
				defer c.Close()
				resp, err := c.C.Caffeinate(ctx, &pb.CaffeinateRequest{Action: action})
				if err != nil {
					return tui.CaffeinateErrMsg{Err: fmt.Errorf("Caffeinate %s: %w", action, err)}
				}
				return tui.CaffeinateResultMsg{Active: resp.GetActive()}
			}
		},
		OnToggleAutoResume: func(want bool) tea.Cmd {
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				c, err := rpcclient.Dial(ctx)
				if err != nil {
					return tui.AutoResumeErrMsg{Err: fmt.Errorf("dial: %w", err)}
				}
				defer c.Close()
				resp, err := c.C.SetAutoResume(ctx, &pb.SetAutoResumeRequest{Enabled: want})
				if err != nil {
					return tui.AutoResumeErrMsg{Err: fmt.Errorf("SetAutoResume(%v): %w", want, err)}
				}
				return tui.AutoResumeResultMsg{Enabled: resp.GetEnabled()}
			}
		},
		OnManualNudge: func(selector string, cancel bool) tea.Cmd {
			return func() tea.Msg {
				ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancelCtx()
				c, err := rpcclient.Dial(ctx)
				if err != nil {
					errLog.LogString(fmt.Sprintf("remote nudge dial: %v", err))
					return tui.NudgeErrMsg{Err: fmt.Errorf("dial: %w", err)}
				}
				defer c.Close()
				if cancel {
					resp, err := c.C.NudgeCancel(ctx, &pb.NudgeCancelRequest{Selector: selector})
					if err != nil {
						errLog.LogString(fmt.Sprintf("remote NudgeCancel(%s): %v", selector, err))
						return tui.NudgeErrMsg{Err: err}
					}
					return nudgeCancelResultMsg(resp)
				}
				resp, err := c.C.NudgeQueue(ctx, &pb.NudgeQueueRequest{Selector: selector})
				if err != nil {
					errLog.LogString(fmt.Sprintf("remote NudgeQueue(%s): %v", selector, err))
					return tui.NudgeErrMsg{Err: err}
				}
				return nudgeQueueResultMsg(resp)
			}
		},
	})
	prog := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// nudgeQueueResultMsg maps a NudgeQueue RPC response onto the TUI's
// NudgeResultMsg. Extracted as a pure function so the field wiring
// (Queued ← queued_session_ids, Already ← already_queued_session_ids) is unit
// testable without a live RPC client — a field swap would otherwise pass every
// existing test.
func nudgeQueueResultMsg(resp *pb.NudgeQueueResponse) tui.NudgeResultMsg {
	return tui.NudgeResultMsg{
		Queued:  resp.GetQueuedSessionIds(),
		Already: resp.GetAlreadyQueuedSessionIds(),
	}
}

// nudgeCancelResultMsg maps a NudgeCancel RPC response onto the TUI's
// NudgeResultMsg (Cancel=true, Cancelled ← cancelled_session_ids). See
// nudgeQueueResultMsg for why this is a standalone pure function.
func nudgeCancelResultMsg(resp *pb.NudgeCancelResponse) tui.NudgeResultMsg {
	return tui.NudgeResultMsg{Cancel: true, Cancelled: resp.GetCancelledSessionIds()}
}
