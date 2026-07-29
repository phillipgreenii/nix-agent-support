package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// runWaitUntilAgentsFinished implements the `wait-until-agents-finished`
// subcommand. Streams WatchState and exits 0 when all agents have been
// idle for N consecutive ticks.
//
// "Idle" here is the ADR 0024 R3 busy notion: only `status == working` counts as
// busy (the loop below sums WorkingN, exactly as the daemon's IsAnyBusy does). A
// `blocked` session — notably `blocker = usage_limit`, i.e. a session that still
// has work but is waiting out the 5h/weekly usage window — does NOT hold this
// gate open and is indistinguishable from idle here.
//
// Callers MUST therefore read exit 0 as "idle reached", NOT as "work finished":
// it can be returned mid-window with blocked work pending that auto-resume will
// pick up at the window reset. A caller that must not proceed until pending work
// is genuinely done MUST additionally check for blocked sessions (e.g. the
// `sessions: N working, N blocked, N idle` line from `pa-monitor status`) rather
// than relying on this exit code alone. This is declared intent, not a defect:
// ADR 0024 R3 stands until a superseding decision, so do NOT "fix" it here.
//
// Exit codes:
//
//	0 = idle reached (no session `working` for N ticks; blocked counts as idle)
//	1 = timeout
//	2 = daemon unavailable past reconnect-grace; also bad flags (see below)
//	3 = invalid args — NOT reachable today: the flag.ExitOnError flag set
//	    below exits 2 itself, so fs.Parse never returns an error
func runWaitUntilAgentsFinished(args []string) {
	fs := flag.NewFlagSet("wait-until-agents-finished", flag.ExitOnError)
	maxWaitS := fs.Int("maximum-wait", 7200, "Maximum total wait in seconds")
	consecutive := fs.Int("consecutive-idle-checks", 3, "Consecutive idle observations required")
	graceS := fs.Int("reconnect-grace", 30, "Seconds to wait for daemon return mid-wait")
	if err := fs.Parse(args); err != nil {
		os.Exit(3)
	}

	deadline := time.Now().Add(time.Duration(*maxWaitS) * time.Second)
	graceDur := time.Duration(*graceS) * time.Second

	streak := 0

	for {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "wait-until-agents-finished: timeout")
			os.Exit(1)
		}

		ctx, cancel := context.WithCancel(context.Background())
		client, err := rpcclient.Dial(ctx)
		if err != nil {
			cancel()
			// Daemon unavailable. Apply reconnect grace if we already
			// observed at least one tick; otherwise fail immediately.
			if streak == 0 {
				fmt.Fprintln(os.Stderr, "daemon unreachable")
				os.Exit(2)
			}
			if err := waitForDaemon(graceDur); err != nil {
				fmt.Fprintf(os.Stderr, "wait-until-agents-finished: %v\n", err)
				os.Exit(2)
			}
			streak = 0 // reset; daemon may have restarted
			continue
		}

		stream, err := client.C.WatchState(ctx, &pb.WatchStateRequest{PushIntervalMs: 1000})
		if err != nil {
			cancel()
			_ = client.Close()
			continue
		}

		// Watchdog: no push from the daemon within 2× the requested push
		// interval is treated as a hung daemon; reconnect. Default
		// PushIntervalMs = 1000ms here → 2s budget.
		const pushBudget = 2 * time.Second
		type recvResult struct {
			msg *pb.DaemonState
			err error
		}
		recvCh := make(chan recvResult, 1)
		next := func() {
			go func() {
				m, e := stream.Recv()
				recvCh <- recvResult{m, e}
			}()
		}
		next()

	streamLoop:
		for {
			select {
			case <-ctx.Done():
				break streamLoop
			case <-time.After(pushBudget):
				fmt.Fprintln(os.Stderr, "wait: push missed, reconnecting")
				break streamLoop
			case r := <-recvCh:
				if r.err != nil {
					break streamLoop
				}
				next()
				st := r.msg
				if st == nil {
					continue
				}
				// WorkingN only, per ADR 0024 R3 — a `blocked` session
				// (e.g. blocker = usage_limit) does not hold this gate
				// open. See the exit-code contract above.
				anyBusy := false
				for _, d := range st.GetDirs() {
					if d.GetWorkingN() > 0 {
						anyBusy = true
						break
					}
				}
				if anyBusy {
					streak = 0
				} else {
					streak++
					if streak >= *consecutive {
						cancel()
						_ = client.Close()
						fmt.Fprintln(os.Stderr, "all idle")
						os.Exit(0)
					}
				}
			}
		}

		cancel()
		_ = client.Close()

		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "wait-until-agents-finished: timeout")
			os.Exit(1)
		}
		// Brief pause before reconnect.
		time.Sleep(500 * time.Millisecond)
	}
}

// waitForDaemon polls Dial until it succeeds or grace expires.
func waitForDaemon(grace time.Duration) error {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		client, err := rpcclient.Dial(ctx)
		cancel()
		if err == nil {
			_ = client.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not return within grace %s", grace)
}

// runConfigShow implements the `config show` subcommand. Prints the
// loaded config so users can verify nix-rendered values.
func runConfigShow(args []string) {
	// Lightweight — no RPC needed; just load the config file the daemon
	// would load and print key fields.
	cfg, err := configLoad()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config show: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("plan_tier:                 %s\n", cfg.PlanTier)
	fmt.Printf("burn_window_short:         %s\n", cfg.BurnWindowShort)
	fmt.Printf("burn_window_long:          %s\n", cfg.BurnWindowLong)
	fmt.Printf("refresh_interval:          %s\n", cfg.RefreshInterval)
	fmt.Printf("caffeinate_grace:          %s\n", cfg.CaffeinateGrace)
	fmt.Printf("working_threshold:         %s\n", cfg.WorkingThreshold)
	fmt.Printf("idle_threshold:            %s\n", cfg.IdleThreshold)
	fmt.Printf("cmux_sidebar_enable:       %v\n", cfg.CmuxSidebarEnable)
	fmt.Printf("auto_restart_on_version_mismatch: %v\n", cfg.AutoRestartOnVersionMismatch)
	for _, d := range cfg.Decorators {
		fmt.Printf("decorator:                 %s -> %s (timeout %dms, env %d)\n", d.Name, d.Command, d.TimeoutMS, len(d.Env))
	}
}
