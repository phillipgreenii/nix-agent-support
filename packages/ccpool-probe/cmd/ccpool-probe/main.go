// main.go: ccpool-probe's cobra root and process entry point.
//
// This binary exposes two subcommands mirroring pg-desk's own
// heartbeat-item/heartbeat split (packages/pg-desk/cmd/pg-desk/heartbeat_item.go,
// heartbeat.go), same shape as its sibling packages/pg-router-probe's own
// main.go: tick-item is a trivial, side-effect-free item emitter so
// pg-router has something to dispatch on a schedule; run is the real work
// (two deterministic health checks over ccpool's own operational health,
// deduplicated bd issue filing via pg-connector, exit-code signaling)
// [design: "pg-router-probe and ccpool-probe" intro, its two-bullet
// subcommand list].
//
// Like pg-router-probe, this binary needs the distinct exit-code scheme (0
// clean / 2 usage error / 3 total failure / 4 partial), not the generic
// 0/1 cobra-failure split most sibling adapters use — see exitcode.go's
// exitCodeError and this file's run().
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version" — matching every sibling Go binary's own convention).
var Version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "ccpool-probe",
		Short:   "Deterministic health probe over ccpool's own operational health, from pg-router's point of view",
		Version: Version,
		// Diagnostics are written explicitly by this package's own code
		// (never cobra's own default usage/error printing), matching
		// pg-router-probe's root command convention.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTickItemCmd())
	root.AddCommand(newRunCmd())
	return root
}

// run executes root with args, writing to stdout/stderr (threaded
// explicitly rather than os.Stdout/os.Stderr so tests can assert on
// captured buffers without touching real process streams — mirrors
// pg-router-probe's own run() shape).
//
// Exit-code mapping: nil error -> 0. An *exitCodeError (returned only by
// this binary's own RunE bodies, via runProbe) carries its own explicit
// code (2/3/4) and, if non-empty, a message printed to stderr before
// returning it. Any OTHER error reaching here is a cobra-level failure
// that never even called RunE — a missing required flag, a bad arg count,
// an unrecognized flag — which is definitionally a usage error, so it maps
// to exit 2 as well [design: "Exit codes" paragraph].
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return 0
	}
	var ece *exitCodeError
	if errors.As(err, &ece) {
		if ece.msg != "" {
			fmt.Fprintln(stderr, ece.msg)
		}
		return ece.code
	}
	fmt.Fprintln(stderr, err)
	return 2
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
