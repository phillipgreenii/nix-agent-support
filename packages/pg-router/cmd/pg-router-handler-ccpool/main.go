// Command pg-router-handler-ccpool is the ccpool adapter of pg-router: the
// handler a [[role]] names with `handler` when the role launches a ccpool
// session per dispatched event. It is today's ccpool executor (internal/
// executor/ccpool.go) behind the new request shape: pg-router core starts it
// per call, it reads the role's name and [role.config] from the `role` object
// of the JSON request on stdin, decodes that config strictly on every call,
// and runs the ccpool dispatch (prior-attempt planning, admission, isolation,
// launch, prompt, wait and budget watchdog, completion and failure policy,
// cleanup).
//
// Subcommands: dispatch, postStartup, preShutdown and validate (INTF-HANDLER,
// DEC-WIRE-1: JSON on stdin, JSON on stdout, a coarse exit code), the outbound
// manager-contract subcommands register and self-status (the launchd agent runs
// register; they speak to the core's socket), the ccpool-specific subcommands
// origin, pool-capacity, janitor, split and query (query is the beads-shaped INTF-SOURCE
// participant, bead pg2-12gr4.10.7), version and help. An unknown subcommand is
// a usage error.
//
// Sessions outlive daemon restarts (ADR 0085), so besides dispatch this adapter
// carries the lifecycle: postStartup runs the post-startup reconcile and orphan
// and worktree sweeps for the request's role (sweeps.go), the same passes a
// dispatch runs before its executor; preShutdown runs the shutdown teardown
// (preshutdown.go).
//
// The shape of [role.config] is this adapter's own, defined by the decoder in
// roleconfig.go: the ccpool-role fields of the former per-role JSON, names
// unchanged: a `ccpool` table and a top-level `sessionEnv` map. Nothing else
// is accepted.
//
// Built from the same Go module as pg-router-ccpool-handler (bead
// pg2-12gr4.10.5); both import the shared internal library, and the external
// id format, session naming and dispatch metadata are the library's, so
// sessions started by either binary are adopted by the other.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/phillipgreenii/pg-router/api/participant"
	"github.com/phillipgreenii/pg-router/internal/handlers/cliutil"
	"github.com/phillipgreenii/x/jsonllogger"
)

var version = "dev"

func main() {
	w := newWiring()
	w.sweeper.InstallProductionGauges()
	slog.SetDefault(jsonllogger.NewLogger(os.Stderr, jsonllogger.WithService("pg-router-handler-ccpool")))
	os.Exit(w.run(os.Args[1:]))
}

// run is the command with its production wiring.
func run(args []string) int { return newWiring().run(args) }

func (w wiring) run(args []string) int {
	if len(args) == 0 {
		cliutil.PrintUsageErr("pg-router-handler-ccpool: a subcommand is required", usageLine)
		return participant.ExitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(helpText)
		return participant.ExitOK
	case "-v", "--version", "version":
		fmt.Println(version)
		return participant.ExitOK
	case "register":
		return runRegister(args[1:])
	case "self-status":
		return runSelfStatus(args[1:])
	case "dispatch":
		return w.runDispatch(args[1:])
	case "postStartup":
		return runPostStartup(args[1:])
	case "preShutdown":
		return runPreShutdown(args[1:])
	case "validate":
		return runValidate(args[1:])
	case "origin":
		return w.runOrigin(args[1:])
	case "pool-capacity":
		return runPoolCapacity(args[1:])
	case "split":
		return runSplit(args[1:])
	case "query":
		return w.runQuery(args[1:])
	case "janitor":
		return w.sweeper.RunJanitor(args[1:])
	default:
		cliutil.PrintUsageErr(fmt.Sprintf("pg-router-handler-ccpool: unknown subcommand %q", args[0]), usageLine)
		return participant.ExitUsage
	}
}
