// Command pg-router-handler-command is the command adapter of pg-router: the
// handler a [[role]] names with `handler` when the role runs a templated argv
// as a subprocess. It is today's command executor (internal/executor/
// command.go) behind the new request shape: pg-router core starts it per
// call, it reads the role's name and [role.config] from the `role` object of
// the JSON request on stdin, decodes that config strictly on every call, and
// runs the role's argv in its own process group, under the minimal base env
// when the role sets strictBaseEnv.
//
// Subcommands (INTF-HANDLER, DEC-WIRE-1: JSON on stdin, JSON on stdout, a
// coarse exit code): dispatch, postStartup, preShutdown, validate, version and
// help. The ccpool-specific subcommands (origin, pool-capacity, split, query)
// are not part of this binary.
//
// The shape of [role.config] is this adapter's own, defined by the decoder in
// roleconfig.go: `command.argv` (array of strings, each templated) and the
// optional `strictBaseEnv` (boolean) beside it. Nothing else is accepted.
//
// Built from the same Go module as pg-router-ccpool-handler (bead
// pg2-12gr4.10.4); both import the shared internal library.
package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/pg-router-handlers/internal/cliutil"
	"github.com/phillipgreenii/pg-router/conformance"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		cliutil.PrintUsageErr("pg-router-handler-command: a subcommand is required", usageLine)
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
	case "postStartup":
		return runLifecycle("postStartup", args[1:])
	case "preShutdown":
		return runLifecycle("preShutdown", args[1:])
	case "validate":
		return runValidate(args[1:])
	default:
		cliutil.PrintUsageErr(fmt.Sprintf("pg-router-handler-command: unknown subcommand %q", args[0]), usageLine)
		return conformance.ExitUsage
	}
}
