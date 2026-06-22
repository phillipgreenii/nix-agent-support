package main

import (
	"context"
	"fmt"
	"os"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// runStatus implements the `status` subcommand — one-shot dump of
// daemon state.
func runStatus(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, rpcclient.DaemonUnavailableMessage("<unknown>"))
		os.Exit(2)
	}
	defer client.Close()

	state, err := client.C.GetState(ctx, &pb.GetStateRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: GetState: %v\n", err)
		os.Exit(2)
	}

	var working, idle, dormant int
	for _, d := range state.GetDirs() {
		working += int(d.GetWorkingN())
		idle += int(d.GetIdleN())
		dormant += int(d.GetDormantN())
	}
	fmt.Printf("client:        pa-monitor %s\n", version)
	fmt.Printf("daemon:        pa-monitor %s\n", state.GetDaemonVersion())
	fmt.Printf("uptime:        %ds\n", state.GetDaemonUptimeSeconds())
	fmt.Printf("plan_tier:     %s\n", state.GetPlanTier())
	fmt.Printf("sessions:      %d working, %d idle, %d dormant\n", working, idle, dormant)

	// Collect per-session details (LastError + PendingNudge) and print
	// annotations only when at least one session has something noteworthy.
	var details []*pb.SessionDetail
	for _, d := range state.GetDirs() {
		for _, sv := range d.GetSessions() {
			sid := sv.GetSessionId()
			if sid == "" {
				continue
			}
			sel := &pb.Selector{Target: &pb.Selector_SessionId{SessionId: sid}}
			sd, err := client.C.GetSessionInfo(ctx, &pb.GetSessionInfoRequest{Selector: sel})
			if err != nil {
				// Skip sessions we can't query; don't abort the whole status.
				continue
			}
			details = append(details, sd)
		}
	}
	if banner := formatAuthFailureBanner(details); banner != "" {
		fmt.Print(banner)
	}

	if b := state.GetActiveBlock(); b != nil {
		fmt.Printf("block %s:  cost $%.2f / cap $%.2f (%.1f%%)\n",
			b.GetId(), b.GetCostUsd(), state.GetPlanCapUsd(), b.GetWindowPct()*100)
	}
	if w := state.GetActiveWeek(); w != nil {
		fmt.Printf("week  %s:  cost $%.2f / cap $%.2f (%.1f%%)\n",
			w.GetId(), w.GetCostUsd(), state.GetWeekCapUsd(), w.GetWindowPct()*100)
	}
	fmt.Printf("caffeinate:    mode %s · process %s\n",
		onOff(state.GetCaffeinateMode()), caffeinateProcessString(state.GetCaffeinateProcess(), state.GetCaffeinateGraceRemainingS()))
	fmt.Printf("auto_resume:   %v\n", state.GetAutoResumeEnabled())

	if annotation := formatStatusSessions(details); annotation != "" {
		fmt.Print(annotation)
	}
}

// runAgentsBusyCheck implements the `agents-busy-check` subcommand.
//
// Exit codes:
//
//	0 = daemon up AND ≥1 agent busy
//	1 = daemon up AND no agents busy
//	2 = daemon unreachable (without --consider-daemon-down-as-busy)
//	0 = daemon unreachable (with --consider-daemon-down-as-busy)
func runAgentsBusyCheck(args []string) {
	considerDownBusy := false
	for _, a := range args {
		if a == "--consider-daemon-down-as-busy" {
			considerDownBusy = true
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		if considerDownBusy {
			fmt.Fprintln(os.Stderr, "daemon unreachable; --consider-daemon-down-as-busy: treating as busy")
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "daemon unreachable")
		os.Exit(2)
	}
	defer client.Close()

	resp, err := client.C.IsAnyBusy(ctx, &pb.IsAnyBusyRequest{})
	if err != nil {
		if considerDownBusy {
			fmt.Fprintf(os.Stderr, "IsAnyBusy err: %v; --consider-daemon-down-as-busy: treating as busy\n", err)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "IsAnyBusy: %v\n", err)
		os.Exit(2)
	}

	if resp.GetAnyBusy() {
		fmt.Fprintf(os.Stderr, "agents busy: %d\n", resp.GetBusyCount())
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "all idle")
	os.Exit(1)
}
