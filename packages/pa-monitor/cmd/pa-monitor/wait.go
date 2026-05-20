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
// Exit codes:
//
//	0 = idle reached
//	1 = timeout
//	2 = daemon unavailable past reconnect-grace
//	3 = invalid args
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

		stream, err := client.C.WatchState(ctx, &pb.WatchStateRequest{HeartbeatIntervalMs: 1000})
		if err != nil {
			cancel()
			client.Close()
			continue
		}

		for {
			msg, err := stream.Recv()
			if err != nil {
				break // stream closed; outer loop retries
			}
			st := msg.GetState()
			if st == nil {
				continue
			}
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
					client.Close()
					fmt.Fprintln(os.Stderr, "all idle")
					os.Exit(0)
				}
			}
		}

		cancel()
		client.Close()

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
			client.Close()
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
	fmt.Printf("topup_pool_usd:            %.2f\n", cfg.TopupPoolUSD)
	fmt.Printf("burn_window_short:         %s\n", cfg.BurnWindowShort)
	fmt.Printf("burn_window_long:          %s\n", cfg.BurnWindowLong)
	fmt.Printf("refresh_interval:          %s\n", cfg.RefreshInterval)
	fmt.Printf("headless_interval:         %s\n", cfg.HeadlessInterval)
	fmt.Printf("caffeinate_grace:          %s\n", cfg.CaffeinateGrace)
	fmt.Printf("working_threshold:         %s\n", cfg.WorkingThreshold)
	fmt.Printf("idle_threshold:            %s\n", cfg.IdleThreshold)
	fmt.Printf("consecutive_idle_checks:   %d\n", cfg.ConsecutiveIdleChecks)
	fmt.Printf("maximum_wait:              %s\n", cfg.MaximumWait)
	fmt.Printf("cmux_sidebar_enable:       %v\n", cfg.CmuxSidebarEnable)
}
