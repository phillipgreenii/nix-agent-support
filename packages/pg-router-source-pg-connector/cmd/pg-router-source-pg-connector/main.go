// main.go: pg-router-source-pg-connector's cobra root and process entry
// point.
//
// This binary holds NO state of its own [design: section 6.1, "holds no
// state"] and has no compile-time dependency on packages/pg-connector: it
// execs the pg-connector binary (ambient on $PATH) as a subprocess and
// parses its stdout JSON generically for every one of its three verbs
// (changes.go, sweep.go, list.go) — never as a Go import.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version" — see default.nix, matching
// packages/pg-ccaudit/cmd/pg-ccaudit/main.go's own convention).
var Version = "dev"

// errFailed is returned by a subcommand's RunE once its own diagnostic —
// either this adapter's own usage message, or pg-connector's own captured
// stderr copied verbatim (invokeOrFail, exec.go) — has already been
// written to cmd.ErrOrStderr(). run() below must not print anything
// further for it, or the same diagnosis would appear twice; any OTHER
// non-nil error reaching run() (e.g. cobra's own required-flag/arg-count
// validation, which never calls RunE at all) has not been reported yet
// and is printed there instead. Mirrors
// packages/pg-connector/cmd/pg-connector/main.go's own
// exitError-vs-plain-error split, collapsed to this adapter's own
// 2-value (0/1) exit scheme [design: section 6.1, "MUST NOT propagate a
// pg-connector CLI exit code as its own"].
var errFailed = errors.New("pg-router-source-pg-connector: command failed")

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "pg-router-source-pg-connector",
		Short:   "Exposes pg-connector's changes/list verbs as pg-router command-query rawItem sources",
		Version: Version,
		// Matches pg-connector's own root.go: diagnostics are written
		// explicitly by this package's own code (invokeOrFail's stderr
		// copy, or a usage message), never by cobra's own default
		// usage/error printing.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newChangesCmd())
	root.AddCommand(newSweepCmd())
	root.AddCommand(newListCmd())
	return root
}

// run executes root with args, writing to stdout/stderr (threaded
// explicitly rather than os.Stdout/os.Stderr so tests can assert on
// captured buffers without touching real process streams).
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return 0
	}
	if !errors.Is(err, errFailed) {
		fmt.Fprintln(stderr, err)
	}
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
