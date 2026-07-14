package main

import (
	"context"
	"fmt"
	"os"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// parseAutoResumeArgs validates and returns the action string for the
// auto-resume subcommand.  action must be one of "on", "off", or "toggle".
func parseAutoResumeArgs(args []string) (string, error) {
	action := "toggle"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "on", "off", "toggle":
		return action, nil
	default:
		return "", fmt.Errorf("auto-resume: action must be on|off|toggle, got %q", action)
	}
}

// runAutoResume implements `auto-resume on|off|toggle`.
func runAutoResume(args []string) {
	action, err := parseAutoResumeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon unreachable")
		os.Exit(2)
	}
	defer func() { _ = client.Close() }()

	var enabled bool
	if action == "toggle" {
		state, err := client.C.GetState(ctx, &pb.GetStateRequest{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "auto-resume: %v\n", err)
			os.Exit(2)
		}
		enabled = !state.GetAutoResumeEnabled()
	} else {
		enabled = action == "on"
	}

	resp, err := client.C.SetAutoResume(ctx, &pb.SetAutoResumeRequest{Enabled: enabled})
	if err != nil {
		fmt.Fprintf(os.Stderr, "auto-resume: %v\n", err)
		os.Exit(2)
	}

	state := "off"
	if resp.GetEnabled() {
		state = "on"
	}
	fmt.Printf("auto-resume %s\n", state)
}
