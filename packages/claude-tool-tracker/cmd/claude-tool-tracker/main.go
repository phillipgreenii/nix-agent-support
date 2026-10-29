// Command claude-tool-tracker appends tool-event lines to the tracker's event
// log (`append`) and records Claude Code hook input (`hook`). See README.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/events"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/claude-tool-tracker/internal/hook"
)

// Version is stamped by the build (mkGoApp ldflags).
var Version = "dev"

// hookDeadline bounds hook mode so a stuck stdin or filesystem never holds a
// tool call.
const hookDeadline = 2 * time.Second

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: claude-tool-tracker append|hook|--version")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hook":
		// Hook mode: print nothing, exit 0 on every path.
		go func() {
			time.Sleep(hookDeadline)
			os.Exit(0)
		}()
		hook.Run(os.Stdin, time.Now())
		os.Exit(0)
	case "append":
		os.Exit(runAppend(os.Args[2:], os.Stdin, os.Stderr))
	case "--version", "version":
		fmt.Println(Version)
	default:
		fmt.Fprintf(os.Stderr, "claude-tool-tracker: unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func runAppend(args []string, stdin io.Reader, stderr io.Writer) int {
	fs := flag.NewFlagSet("append", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session-id", "", "Claude Code session id (required)")
	source := fs.String("source", "", "producer, [a-z0-9-]+ (required)")
	kind := fs.String("kind", "", "event kind <source>.<name> (required)")
	toolUse := fs.String("tool-use-id", "", "the call's id (required for tool-call kinds)")
	agent := fs.String("agent-id", "", "subagent id, when the call is a subagent's")
	ts := fs.Int64("ts", 0, "event time in ms since epoch (default: now)")
	eventID := fs.String("event-id", "", "event id (default: generated)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	body, err := io.ReadAll(io.LimitReader(stdin, 16<<20))
	if err != nil {
		fmt.Fprintln(stderr, "claude-tool-tracker append: reading stdin:", err)
		return 1
	}
	data := map[string]any{}
	if s := strings.TrimSpace(string(body)); s != "" {
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		if err := dec.Decode(&data); err != nil || data == nil || dec.More() {
			fmt.Fprintln(stderr, "claude-tool-tracker append: stdin must be one JSON object")
			return 1
		}
	}
	now := time.Now()
	if *ts == 0 {
		*ts = now.UnixMilli()
	}
	if *eventID == "" {
		*eventID = events.NewEventID(now)
	}
	env := &events.Envelope{
		Schema: events.Schema, EventID: *eventID, TS: *ts, Source: *source, Kind: *kind,
		SessionID: *session, ToolUseID: *toolUse, AgentID: *agent, Data: data,
	}
	if _, err := events.Append(env); err != nil {
		fmt.Fprintln(stderr, "claude-tool-tracker append:", err)
		return 1
	}
	return 0
}
