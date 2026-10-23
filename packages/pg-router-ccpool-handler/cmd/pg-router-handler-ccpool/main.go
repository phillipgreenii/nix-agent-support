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
// Subcommands: dispatch and validate (INTF-HANDLER, DEC-WIRE-1: JSON on stdin,
// JSON on stdout, a coarse exit code), the operator tool origin, version and
// help. The lifecycle requests (postStartup, preShutdown) and the remaining
// ccpool-specific subcommands (pool-capacity, split, query, register,
// self-status) are not part of this binary yet: they are the next packets of
// the same split (pg2-12gr4.10.6 and .10.7), and an unknown subcommand is a
// usage error.
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
	"os"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/cliutil"
	"github.com/phillipgreenii/pg-router/conformance"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		cliutil.PrintUsageErr("pg-router-handler-ccpool: a subcommand is required", usageLine)
		return conformance.ExitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(helpText)
		return conformance.ExitOK
	case "-v", "--version", "version":
		fmt.Println(version)
		return conformance.ExitOK
	case "dispatch":
		return runDispatch(args[1:])
	case "validate":
		return runValidate(args[1:])
	case "origin":
		return runOrigin(args[1:])
	default:
		cliutil.PrintUsageErr(fmt.Sprintf("pg-router-handler-ccpool: unknown subcommand %q", args[0]), usageLine)
		return conformance.ExitUsage
	}
}
