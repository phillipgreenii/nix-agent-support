package main

import (
	"context"
	"fmt"
	"os"

	"github.com/phillipgreenii/pg-router/internal/telemetry"
)

var version = "dev"

func main() {
	os.Exit(run())
}

// run holds everything main() used to do directly, so that
// telemetry.Init's shutdown (below) is guaranteed to flush before the
// process exits. A deferred call in main() itself would never run: every
// branch below ends by returning an exit code that main() feeds straight
// to os.Exit, and os.Exit bypasses deferred functions outright — so the
// flush has to live inside a function main() calls and returns FROM,
// not inside main() itself.
func run() int {
	// Telemetry (design section 5): called unconditionally, for every
	// subcommand, mirroring packages/pg-pr/cmd/pg-pr/main.go's own
	// Init call. Init never returns an error in practice — a missing
	// OTEL_EXPORTER_OTLP_ENDPOINT installs a no-op LoggerProvider (the
	// common case for a one-shot operator invocation with no OTLP env set),
	// and a bad endpoint logs one stderr warning and continues.
	shutdown, _ := telemetry.Init(context.Background(), "pg-router", version)
	defer func() { _ = shutdown(context.Background()) }()

	r := route(os.Args)
	switch r.kind {
	case routeVersion:
		fmt.Println(version)
		return exitOK
	case routeHelp:
		fmt.Println(helpText)
		return exitOK
	case routeUsageErr:
		printUsageErr(r.msg)
		return exitUsage
	case routeRun:
		return runRun(r.only, r.disable, r.metricsAddr)
	case routeRunUntilIdle:
		return runRunUntilIdle(r.only, r.disable)
	case routeRunRole:
		return runRunRole(r.role, r.eventJSON, r.json)
	case routeRunQuery:
		return runRunQuery(r.query, r.json)
	case routeConfig:
		return runConfig(r.configMode, r.json)
	case routeIngestEvent:
		return runIngestEvent(r.rest)
	case routePushInject:
		return runPushInject(r.rest)
	case routeStatus:
		return runStatus(r.rest)
	case routeTUI:
		return runTUI(r.rest)
	case routeSelfStatus:
		return runSelfStatus(r.rest)
	case routePause:
		return runPause(r.gate)
	case routeResume:
		return runResume(r.gate, r.allGates)
	}
	return exitOK
}

// printUsageErr writes a usage diagnostic and the short usage line to stderr.
// Shared by main (top-level parse) and runDrain (drain-subcommand parse) so the
// two usage-error paths can't drift in format.
func printUsageErr(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	fmt.Fprintln(os.Stderr, usageLine)
}
