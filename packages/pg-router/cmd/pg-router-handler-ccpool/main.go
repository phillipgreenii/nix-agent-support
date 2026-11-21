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
	"os"

	"github.com/phillipgreenii/pg-router/api/handlerkit"
)

var version = "dev"

func main() {
	w := newWiring()
	a := w.adapter()
	a.JSONLog = true
	a.Setup = w.sweeper.InstallProductionGauges
	handlerkit.Main(a)
}

// run is the command with its production wiring.
func run(args []string) int { return newWiring().run(args) }

func (w wiring) run(args []string) int {
	return handlerkit.Execute(w.adapter(), args, os.Stdout, os.Stderr)
}

func (w wiring) adapter() handlerkit.Adapter {
	return handlerkit.Adapter{
		Name:    "pg-router-handler-ccpool",
		Version: version,
		Usage:   usageLine,
		Help:    helpText,
		Commands: map[string]func([]string) int{
			"register":      runRegister,
			"self-status":   runSelfStatus,
			"dispatch":      w.runDispatch,
			"postStartup":   runPostStartup,
			"preShutdown":   runPreShutdown,
			"validate":      runValidate,
			"origin":        w.runOrigin,
			"pool-capacity": runPoolCapacity,
			"split":         runSplit,
			"query":         w.runQuery,
			"janitor":       w.sweeper.RunJanitor,
		},
	}
}
