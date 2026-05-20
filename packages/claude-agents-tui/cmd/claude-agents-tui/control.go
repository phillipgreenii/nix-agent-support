package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	pb "github.com/phillipgreenii/claude-agents-tui/internal/proto"
	"github.com/phillipgreenii/claude-agents-tui/internal/rpcclient"
)

// runCaffeinate implements `caffeinate on|off|toggle`.
func runCaffeinate(args []string) {
	action := "toggle"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "on", "off", "toggle":
	default:
		fmt.Fprintf(os.Stderr, "caffeinate: action must be on|off|toggle, got %q\n", action)
		os.Exit(3)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon unreachable")
		os.Exit(2)
	}
	defer client.Close()

	resp, err := client.C.Caffeinate(ctx, &pb.CaffeinateRequest{Action: action})
	if err != nil {
		fmt.Fprintf(os.Stderr, "caffeinate: %v\n", err)
		os.Exit(2)
	}
	state := "off"
	if resp.GetActive() {
		state = "on"
	}
	fmt.Printf("caffeinate: %s", state)
	if resp.GetCause() != "" {
		fmt.Printf(" (%s)", resp.GetCause())
	}
	fmt.Println()
}

// runNudge implements `nudge <selector> [--text=...]`.
//
// <selector> is one of:
//
//	session:<id>
//	path:<workspace-path>
//	cmux:<workspace-id>
func runNudge(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "nudge: missing selector (session:<id> | path:<p> | cmux:<id>)")
		os.Exit(3)
	}
	sel, err := parseSelector(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "nudge: %v\n", err)
		os.Exit(3)
	}
	text := ""
	for i := 1; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--text=") {
			text = strings.TrimPrefix(args[i], "--text=")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon unreachable")
		os.Exit(2)
	}
	defer client.Close()

	resp, err := client.C.Nudge(ctx, &pb.NudgeRequest{Selector: sel, Text: text})
	if err != nil {
		fmt.Fprintf(os.Stderr, "nudge: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("nudge sent to %d session(s), %d errors", resp.GetSentCount(), resp.GetErrorCount())
	if resp.GetPostWindow() {
		fmt.Print(" (post-window)")
	}
	fmt.Println()
}

// runInfo implements `info <selector>` — prints session detail (when
// selector starts with session: or cmux:) or directory rollup (when
// path:).
func runInfo(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "info: missing selector")
		os.Exit(3)
	}
	sel, err := parseSelector(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "info: %v\n", err)
		os.Exit(3)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon unreachable")
		os.Exit(2)
	}
	defer client.Close()

	if path := sel.GetPath(); path != "" {
		resp, err := client.C.GetPathInfo(ctx, &pb.GetPathInfoRequest{Path: path})
		if err != nil {
			fmt.Fprintf(os.Stderr, "info: %v\n", err)
			os.Exit(2)
		}
		d := resp.GetDirectory()
		if d == nil {
			fmt.Fprintln(os.Stderr, "info: no directory matched")
			os.Exit(1)
		}
		fmt.Printf("path:     %s\n", d.GetPath())
		fmt.Printf("branch:   %s\n", d.GetBranch())
		fmt.Printf("sessions: %d working, %d idle, %d dormant\n", d.GetWorkingN(), d.GetIdleN(), d.GetDormantN())
		fmt.Printf("tokens:   %d\n", d.GetTotalTokens())
		fmt.Printf("cost:     $%.2f\n", d.GetTotalCostUsd())
		return
	}

	resp, err := client.C.GetSessionInfo(ctx, &pb.GetSessionInfoRequest{Selector: sel})
	if err != nil {
		fmt.Fprintf(os.Stderr, "info: %v\n", err)
		os.Exit(2)
	}
	v := resp.GetView()
	if v == nil {
		fmt.Fprintln(os.Stderr, "info: no session matched")
		os.Exit(1)
	}
	fmt.Printf("session_id:     %s\n", v.GetSessionId())
	fmt.Printf("status:         %s\n", v.GetStatus())
	fmt.Printf("model:          %s\n", v.GetModel())
	fmt.Printf("cwd:            %s\n", v.GetCwd())
	fmt.Printf("branch:         %s\n", v.GetBranch())
	fmt.Printf("context_tokens: %d\n", v.GetContextTokens())
	fmt.Printf("subagents:      %d\n", v.GetSubagentCount())
	fmt.Printf("subshells:      %d\n", v.GetSubshellCount())
	if len(resp.GetLabelPairs()) > 0 {
		fmt.Println("labels:")
		for _, kv := range resp.GetLabelPairs() {
			fmt.Printf("  %s\n", kv)
		}
	}
}

// runCmuxBridge implements `cmux-bridge` — runs inside a cmux pane,
// streams daemon state, drives the cmux sidebar. v1 is a minimal
// long-running process that polls and reports; full integration with
// cmuxstatus.Reporter ships when cmuxstatus loses its TUI coupling.
func runCmuxBridge(args []string) {
	fmt.Fprintln(os.Stderr, "cmux-bridge: not yet wired (Plan 3 follow-up)")
	os.Exit(2)
}

// parseSelector maps a CLI selector string into a proto Selector.
//
// Accepted forms:
//
//	session:<id>
//	cmux:<id>
//	path:<absolute-or-relative-path>
//	<bare-id>  → defaults to session:<id> when not containing a slash
//	<path>     → defaults to path:<path> when contains a slash
func parseSelector(s string) (*pb.Selector, error) {
	if i := strings.IndexByte(s, ':'); i > 0 {
		prefix := s[:i]
		rest := s[i+1:]
		switch prefix {
		case "session":
			return &pb.Selector{Target: &pb.Selector_SessionId{SessionId: rest}}, nil
		case "path":
			return &pb.Selector{Target: &pb.Selector_Path{Path: rest}}, nil
		case "cmux":
			return &pb.Selector{Target: &pb.Selector_CmuxWorkspaceId{CmuxWorkspaceId: rest}}, nil
		}
	}
	if strings.ContainsAny(s, "/.") {
		return &pb.Selector{Target: &pb.Selector_Path{Path: s}}, nil
	}
	return &pb.Selector{Target: &pb.Selector_SessionId{SessionId: s}}, nil
}
