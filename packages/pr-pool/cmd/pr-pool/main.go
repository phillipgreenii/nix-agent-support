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
	case routeRun:
		os.Exit(runRun(r.only, r.disable))
	case routeRunUntilIdle:
		os.Exit(runRunUntilIdle(r.only, r.disable))
	case routeRunRole:
		os.Exit(runRunRole(r.role, r.bead, r.json))
	case routeRunQuery:
		os.Exit(runRunQuery(r.role, r.query, r.json))
	case routeConfig:
		os.Exit(runConfig(r.configMode, r.json))
	case routeSessions:
		os.Exit(runSessions())
	case routeReconcile:
		os.Exit(runReconcile())
	case routeIngestEvent:
		os.Exit(runIngestEvent(r.rest))
	case routePushInject:
		os.Exit(runPushInject(r.rest))
	case routeStatus:
		os.Exit(runStatus(r.rest))
	case routeSelfStatus:
		os.Exit(runSelfStatus(r.rest))
	case routePause:
		os.Exit(runPause(r.gate))
	case routeResume:
		os.Exit(runResume(r.gate, r.allGates))
	}
}

// printUsageErr writes a usage diagnostic and the short usage line to stderr.
// Shared by main (top-level parse) and runDrain (drain-subcommand parse) so the
// two usage-error paths can't drift in format.
func printUsageErr(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, usageLine)
}
