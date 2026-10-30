// Command pg-claude-code-recorder is the recorder's command: the daemon (serve)
// and the client subcommands for scripts, skills and the operator. This build
// carries serve, status and coverage; the other subcommands arrive with their
// phases.
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
}

// laterSubcommands exist in the design and arrive with later phases.
var laterSubcommands = map[string]string{
	"conversations": "phase 4", "processes": "phase 4", "query": "phase 2", "search": "phase 2",
	"feed": "phase 3", "raw": "phase 3", "snapshot": "phase 3", "held": "phase 6", "where": "phase 6",
	"find": "phase 6", "extract": "phase 6", "remove": "phase 6", "doctor": "phase 6",
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
	}
	if phase, ok := laterSubcommands[args[0]]; ok {
		return a.emitError(false, wire.ErrUsage, fmt.Sprintf("%s is not available in this build (it arrives in %s)", args[0], phase))
	}
	return a.emitError(false, wire.ErrUsage, fmt.Sprintf("unknown subcommand %q; try --help", args[0]))
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{out: os.Stdout, errw: os.Stderr, stdin: os.Stdin, getenv: os.Getenv, launchState: launchctlState, ctx: ctx}
	os.Exit(a.run(os.Args[1:]))
}
