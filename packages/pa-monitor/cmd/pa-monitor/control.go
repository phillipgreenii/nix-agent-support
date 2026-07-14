package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
	"github.com/phillipgreenii/pa-monitor/internal/rpcclient"
)

// onOff renders a bool as the "on"/"off" word form used by the two caffeinate
// indicators.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// caffeinateProcessString renders the caffeination PROCESS state as a word.
// The grace state carries its remaining seconds. Distinct from the MODE so the
// incident case (mode on + process off) is unambiguous in the CLI.
func caffeinateProcessString(p pb.CaffeinateProcess, graceRemainingS uint32) string {
	switch p {
	case pb.CaffeinateProcess_CAFFEINATE_PROCESS_ON:
		return "on (holding)"
	case pb.CaffeinateProcess_CAFFEINATE_PROCESS_GRACE:
		if graceRemainingS > 0 {
			return fmt.Sprintf("grace (%ds)", graceRemainingS)
		}
		return "grace"
	case pb.CaffeinateProcess_CAFFEINATE_PROCESS_ERROR:
		return "error"
	default:
		return "off"
	}
}

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
	defer func() { _ = client.Close() }()

	resp, err := client.C.Caffeinate(ctx, &pb.CaffeinateRequest{Action: action})
	if err != nil {
		fmt.Fprintf(os.Stderr, "caffeinate: %v\n", err)
		os.Exit(2)
	}
	// Show BOTH indicators: MODE (the toggle just set) and PROCESS (what the
	// subprocess is doing). GetMode falls back to GetActive for daemons that
	// predate the two-indicator split.
	mode := resp.GetMode() || resp.GetActive()
	fmt.Printf("caffeinate: mode %s · process %s",
		onOff(mode), caffeinateProcessString(resp.GetProcess(), resp.GetGraceRemainingS()))
	if resp.GetCause() != "" {
		fmt.Printf(" (%s)", resp.GetCause())
	}
	if u := resp.GetUntil(); u != nil {
		fmt.Printf(" until %s", u.AsTime().Local().Format("15:04:05"))
	}
	fmt.Println()
}

// nudgeFlags holds the parsed flags for the nudge subcommand.
type nudgeFlags struct {
	selector string
	text     string
	cancel   bool
}

// parseNudgeFlags parses the argument list for the nudge subcommand.
// Returns an error if the selector is missing or invalid.
func parseNudgeFlags(args []string) (nudgeFlags, error) {
	if len(args) == 0 {
		return nudgeFlags{}, fmt.Errorf("missing selector (session:<id> | path:<p> | cmux:<id>)")
	}
	f := nudgeFlags{selector: args[0]}
	if _, err := parseSelector(f.selector); err != nil {
		return nudgeFlags{}, err
	}
	for i := 1; i < len(args); i++ {
		switch {
		case strings.HasPrefix(args[i], "--text="):
			f.text = strings.TrimPrefix(args[i], "--text=")
		case args[i] == "--cancel":
			f.cancel = true
		}
	}
	return f, nil
}

// runNudge implements `nudge <selector> [--text=...] [--cancel]`.
//
// <selector> is one of:
//
//	session:<id>
//	path:<workspace-path>
//	cmux:<workspace-id>
//
// Without --cancel, enqueues a nudge via NudgeQueue (daemon dispatches on
// next idle tick). With --cancel, cancels any pending queued nudge via
// NudgeCancel.
func runNudge(args []string) {
	f, err := parseNudgeFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "nudge: %v\n", err)
		os.Exit(3)
	}
	selRaw := f.selector
	text := f.text
	doCancel := f.cancel

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := rpcclient.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon unreachable")
		os.Exit(2)
	}
	defer func() { _ = client.Close() }()

	if doCancel {
		resp, err := client.C.NudgeCancel(ctx, &pb.NudgeCancelRequest{Selector: selRaw})
		if err != nil {
			fmt.Fprintf(os.Stderr, "nudge: %v\n", err)
			os.Exit(2)
		}
		if len(resp.GetCancelledSessionIds()) == 0 {
			fmt.Println("nudge cancel: nothing queued")
		} else {
			fmt.Printf("nudge cancelled for: %s\n", strings.Join(resp.GetCancelledSessionIds(), ", "))
		}
		return
	}

	resp, err := client.C.NudgeQueue(ctx, &pb.NudgeQueueRequest{Selector: selRaw, Text: text})
	if err != nil {
		fmt.Fprintf(os.Stderr, "nudge: %v\n", err)
		os.Exit(2)
	}
	if len(resp.GetQueuedSessionIds()) == 0 && len(resp.GetAlreadyQueuedSessionIds()) == 0 {
		fmt.Println("nudge: no sessions matched")
		return
	}
	if len(resp.GetQueuedSessionIds()) > 0 {
		fmt.Printf("queued nudge for: %s\n", strings.Join(resp.GetQueuedSessionIds(), ", "))
	}
	if len(resp.GetAlreadyQueuedSessionIds()) > 0 {
		fmt.Printf("already queued for: %s\n", strings.Join(resp.GetAlreadyQueuedSessionIds(), ", "))
	}
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
	defer func() { _ = client.Close() }()

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
	if extra := formatSessionInfo(resp); extra != "" {
		fmt.Print(extra)
	}
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
