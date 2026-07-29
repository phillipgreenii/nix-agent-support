package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	r := route(os.Args)
	switch r.kind {
	case routeVersion:
		fmt.Println(version)
	case routeHelp:
		fmt.Println(helpText)
	case routeUsageErr:
		printUsageErr(r.msg)
		os.Exit(exitUsage)
	case routeDrain:
		os.Exit(runDrain(r.rest))
	case routeRunRole:
		os.Exit(runRunRole(r.role, r.bead))
	case routeRunQuery:
		os.Exit(runRunQuery(r.role))
	case routeConfig:
		os.Exit(runConfig(r.configMode))
	case routeSessions:
		os.Exit(runSessions())
	case routeReconcile:
		os.Exit(runReconcile())
	case routeIngestEvent:
		os.Exit(runIngestEvent(r.rest))
	}
}

// printUsageErr writes a usage diagnostic and the short usage line to stderr.
// Shared by main (top-level parse) and runDrain (drain-subcommand parse) so the
// two usage-error paths can't drift in format.
func printUsageErr(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, usageLine)
}
