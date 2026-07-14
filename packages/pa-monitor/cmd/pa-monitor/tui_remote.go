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
	"github.com/phillipgreenii/pa-monitor/internal/reexec"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
	"github.com/phillipgreenii/pa-monitor/internal/tui"
)

// tuiReexec is the re-exec entrypoint, a package var mirroring bridgeReexec so
// the TUI restart wire has an injectable seam. Production points at reexec.Run;
// on success it never returns.
var tuiReexec = reexec.Run

// runTUIRemote launches the TUI against a running daemon over gRPC.
// The local poller is bypassed; state is delivered by a StreamingPoller that
// subscribes to the daemon's server-streaming WatchState RPC (one long-lived
// stream, not a GetState call per render tick).
//
// This mode is intentionally minimal: the existing TUI Model is unchanged
// and consumes the aggregate.Tree the poller reconstructs from the wire
// DaemonState. The daemon owns caffeinate, nudge dispatch, and session
// lifecycle.
func runTUIRemote() {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}

	rp, err := rpcclient.NewStreamingPoller(cfg.RefreshInterval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote poller: %v\n", err)
		os.Exit(2)
	}
	defer func() { _ = rp.Close() }()

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
	defer func() { _ = connEmit.Shutdown(emitCtx) }()

	// Sample the poller's connection state on a ticker and publish the gauge.
	// IsOffline() is reliable: the StreamingPoller marks itself disconnected the
	// moment its WatchState stream drops (and while redialing), so the flag holds
	// throughout a disconnect window.
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

		AutoRestartOnVersionMismatch: cfg.AutoRestartOnVersionMismatch,
		// Seed the attempt counter from the env so a re-exec'd TUI inherits the
		// running count; the daemon-version convergence resets it (see Model).
		ReexecAttemptBase: reexec.Attempt(os.Environ()),
		RecordReexec:      connEmit.RecordReexec, // nil-safe even if connEmit==nil
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
				defer func() { _ = c.Close() }()
				resp, err := c.C.Caffeinate(ctx, &pb.CaffeinateRequest{Action: action})
				if err != nil {
					return tui.CaffeinateErrMsg{Err: fmt.Errorf("caffeinate %s: %w", action, err)}
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
				defer func() { _ = c.Close() }()
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
				defer func() { _ = c.Close() }()
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
	// Run the program in a loop so a re-exec request (version mismatch, feature
	// enabled) can restart the process in place. bubbletea restores the terminal
	// before Run() returns, so the exec inherits a clean TTY. On exec failure the
	// TUI MUST NOT silently exit: it re-enters Run() in warn-only (gave-up) mode.
	for {
		prog := tea.NewProgram(model, tea.WithAltScreen())
		finalModel, err := prog.Run()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fm, ok := finalModel.(*tui.Model)
		if !ok || !fm.ReexecRequested() {
			return // normal quit (user pressed q / ctrl-c)
		}

		attempt := fm.ReexecAttempt()
		connEmit.RecordReexec(attempt, otel.ReexecOutcomeAttempt)
		fmt.Fprintln(os.Stderr, "pa-monitor: restarting to match the new daemon build…")
		if rerr := tuiReexec(os.Args[0], os.Args, os.Environ(), attempt); rerr != nil {
			// A broken exec will not fix itself: record it, tell the user to
			// restart manually, flip the model into warn-only mode so the next
			// poll does not immediately re-request, and re-enter Run().
			connEmit.RecordReexec(attempt, otel.ReexecOutcomeExecFailed)
			fmt.Fprintf(os.Stderr, "pa-monitor: auto-restart failed (%v) — restart this TUI manually\n", rerr)
			fm.MarkReexecGaveUp()
			model = fm
			continue
		}
		return // unreachable on exec success (execve replaced the process)
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
