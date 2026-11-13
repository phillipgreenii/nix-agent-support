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

	"github.com/phillipgreenii/x/exitcode"
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
// shared-exit-code scheme (x/exitcode; see exitError) [design: section
// 6.1, "MUST NOT propagate a pg-connector CLI exit code as its own"].
var errFailed = errors.New("pg-router-source-pg-connector: command failed")

// exitError is an errFailed that carries this adapter's own exit code from
// the shared x/exitcode set (Usage, Unavailable, Denied, ...). It matches
// errFailed under errors.Is, so run() still prints nothing further for it
// (its diagnostic is already on stderr), and run() returns its code.
// errFailed itself, unwrapped, is the generic exitcode.Error.
type exitError struct{ code int }

func (e *exitError) Error() string { return errFailed.Error() }

func (e *exitError) Is(target error) bool { return target == errFailed }

// failWith returns an exitError for code.
func failWith(code int) error { return &exitError{code: code} }

// errUsage is returned by a verb's RunE for a usage error its own
// validation caught after cobra's own parsing succeeded (exitcode.Usage).
var errUsage = failWith(exitcode.Usage)

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

	// A verb's RunE runs only after cobra has resolved the verb and
	// validated its flags, required flags and argument count, so an error
	// reaching run() before any RunE started is cobra's own usage rejection
	// (unknown verb or flag, missing required flag, wrong argument count).
	validated := false
	for _, c := range root.Commands() {
		inner := c.RunE
		c.RunE = func(cmd *cobra.Command, args []string) error {
			validated = true
			return inner(cmd, args)
		}
	}

	err := root.Execute()
	if err == nil {
		return exitcode.OK
	}
	if !errors.Is(err, errFailed) {
		fmt.Fprintln(stderr, err)
	}
	var ee *exitError
	switch {
	case errors.As(err, &ee):
		return ee.code
	case !validated:
		return exitcode.Usage
	default:
		return exitcode.Error
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
