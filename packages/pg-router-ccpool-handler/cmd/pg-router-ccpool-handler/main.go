// Command pg-router-ccpool-handler is this module's wire-facing CLI
// entrypoint: it makes the moved ccpool/command executor logic (internal/
// executor) a real INTF-HANDLER participant, and gives the moved beads query
// source a placeholder INTF-SOURCE participant (query subcommands are wired
// for real by docket pg2-oju6w's Task 5.8 — this module's `query` today
// always replies with zero events, a schema-valid stub).
//
// Subcommands (interfaces.md's common manager contract plus INTF-HANDLER/
// INTF-SOURCE): register, self-status, dispatch, query. register/self-status
// are OUTBOUND: this process reaches pg-router's core over DEC-WIRE-2's
// socket (an injected --socket/--token, or the PG_ROUTER_SOCKET/PG_ROUTER_TOKEN
// env vars pg-router's own cmd/pg-router uses for the same purpose). dispatch/
// query are INBOUND: the default CLI transport (DEC-WIRE-1) — JSON on stdin,
// JSON on stdout, a coarse exit code — exactly what pg-router's core would
// invoke this binary with once Task 5.4's wire client lands.
package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/pg-router/conformance"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsageErr("pg-router-ccpool-handler: a subcommand is required")
		return conformance.ExitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(helpText)
		return conformance.ExitOK
	case "-v", "--version", "version":
		fmt.Println(version)
		return conformance.ExitOK
	case "register":
		return runRegister(args[1:])
	case "self-status":
		return runSelfStatus(args[1:])
	case "dispatch":
		return runDispatch(args[1:])
	case "query":
		return runQuery(args[1:])
	default:
		printUsageErr(fmt.Sprintf("pg-router-ccpool-handler: unknown subcommand %q", args[0]))
		return conformance.ExitUsage
	}
}

// printUsageErr writes a usage diagnostic and the short usage line to stderr,
// mirroring cmd/pg-router/main.go's own printUsageErr.
func printUsageErr(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, usageLine)
}
