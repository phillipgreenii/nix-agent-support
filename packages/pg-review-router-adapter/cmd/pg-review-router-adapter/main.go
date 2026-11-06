// pg-review-router-adapter sits between a pg-router command role and
// pg-review. It owns every router concern so pg-review itself stays free of
// router concepts:
//
//	pg-review-router-adapter run <item-id> <schedule_id> <lens> <scope> <window>
//	pg-review-router-adapter maintain
//
// "run" turns a dispatch into `pg-review job run`; "maintain" runs
// `pg-review job maintain`. Both map pg-review exit codes to router semantics
// (see mapExit) and write their log to stderr. The adapter makes no scheduling
// decision: it has no time-of-day, no last-run memory and no clock.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Version is injected at build time by mkGoApp.
var Version = "dev"

func main() {
	// The router sends SIGTERM to the whole process group, so pg-review gets
	// it too. The adapter only has to survive it and wait for the child.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, sigs))
}

func run(args []string, stdout, stderr io.Writer, sigs <-chan os.Signal) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr, sigs)
	case "maintain":
		return cmdMaintain(args[1:], stdout, stderr, sigs)
	case "--version", "version":
		fmt.Fprintln(stdout, "pg-review-router-adapter", Version)
		return 0
	default:
		fmt.Fprintf(stderr, "pg-review-router-adapter: unknown subcommand %q\n%s\n", args[0], usage)
		return exitUsage
	}
}

const usage = "usage: pg-review-router-adapter run <item-id> <schedule_id> <lens> <scope> <window> | maintain"
