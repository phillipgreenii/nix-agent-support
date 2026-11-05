// Command pg-claude-code-recorder is the recorder's command: the daemon (serve)
// and the client subcommands for scripts, skills and the operator. This build
// carries every subcommand of the design.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/platform"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// Version is stamped by mkGoApp's default ldflag (main.Version).
var Version = "dev"

// app carries what a subcommand touches, so tests inject it.
type app struct {
	out, errw io.Writer
	stdin     io.Reader
	getenv    func(string) string
	// launchState reports the launch agent's state for the offline status.
	launchState func(label string) string
	ctx         context.Context
	// pollEvery is how often coverage --wait-caught-up polls (1s when zero).
	pollEvery time.Duration
	// recentBudget bounds how long status waits for the crash counts (500ms when zero).
	recentBudget time.Duration
	// isTTY reports whether the command runs interactively (standard input is a
	// terminal when nil), which decides whether remove may ask.
	isTTY func() bool
}

func (a *app) run(args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(a.errw, "usage: pg-claude-code-recorder <serve|status|coverage|version> [flags]; try --help")
		return wire.ExitUsage
	}
	switch args[0] {
	case "--version", "version":
		_, _ = fmt.Fprintf(a.out, "pg-claude-code-recorder %s (cgo=%t)\n", Version, platform.CgoReady())
		return wire.ExitOK
	case "--help", "-h", "help":
		a.help()
		return wire.ExitOK
	case "serve":
		return a.serve(args[1:])
	case "status":
		return a.status(args[1:])
	case "coverage":
		return a.coverage(args[1:])
	case "query", "search":
		return a.request(args[0], args[1:])
	case "find":
		return a.find(args[1:])
	case "feed":
		return a.feed(args[1:])
	case "snapshot":
		return a.snapshot(args[1:])
	case "raw":
		return a.raw(args[1:])
	case "rebuild":
		return a.rebuild(args[1:])
	case "conversations":
		return a.conversations(args[1:])
	case "processes":
		return a.processes(args[1:])
	case "held":
		return a.held(args[1:])
	case "where":
		return a.where(args[1:])
	case "remove":
		return a.remove(args[1:])
	case "extract":
		return a.extract(args[1:])
	case "doctor":
		return a.doctor(args[1:])
	case "sweep":
		return a.sweep(args[1:])
	}
	return a.emitError(false, wire.ErrUsage, fmt.Sprintf("unknown subcommand %q; try --help", args[0]))
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{out: os.Stdout, errw: os.Stderr, stdin: os.Stdin, getenv: os.Getenv, launchState: launchctlState, ctx: ctx}
	os.Exit(a.run(os.Args[1:]))
}
