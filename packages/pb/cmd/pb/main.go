package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/phillipgreenii/x/exitcode"
	"github.com/spf13/cobra"
)

// Version is overridden at build time by mkGoApp (versionPath = "main.Version").
var Version = "dev"

// nowUTC is the only real-clock call; the unit-tested core takes Now as a param.
func nowUTC() time.Time { return time.Now().UTC() }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "pb",
		Short:         "phillip-beads: pn:applied gates + drain-loop helpers",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return newExitError(exitUsage, err) })
	root.AddCommand(newGateCmd())
	root.AddCommand(newDrainCmd())
	root.AddCommand(newUnstickCmd())
	root.AddCommand(newActorCmd())
	return root
}

// Process exit codes. Commands that need a code other than the default return
// an error built with newExitError; everything else exits exitGeneric. The
// shared codes come from x/exitcode; the app-specific ones sit in its 20-63 range.
const (
	exitGeneric     = exitcode.Error       // IO or internal error
	exitUsage       = exitcode.Usage       // bad flag, argument or flag combination
	exitBD          = exitcode.Unavailable // a bd call failed (pb unstick)
	exitConflict    = exitcode.Conflict    // pb drain isolate: conflicting isolation state
	exitGateSkipped = exitcode.Degraded    // pb gate check: some gate was undeterminable
	exitGatingPart  = exitcode.Degraded    // pb gate attach-verified-child: child left deferred, gating incomplete

	// App-specific codes (exitcode.AppMin to exitcode.AppMax).
	exitNoteFailed         = 20 // pb drain reclaim: worktree removed, bead note not written
	exitChildMayBeWorkable = 21 // pb gate attach-verified-child: child not proven un-workable
	exitNonConforming      = 22 // pb unstick marker --check: a marker does not conform
)

// exitError carries a specific process exit code through the error return path.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func newExitError(code int, err error) error { return &exitError{code: code, err: err} }

// usageErrorf builds a usage error (exit 2) for a bad flag value or combination.
func usageErrorf(format string, a ...any) error {
	return newExitError(exitUsage, fmt.Errorf(format, a...))
}

// isCobraUsageError reports whether err is one of cobra's own argument
// complaints (unknown command, missing required flag, wrong argument count).
// Flag-parse errors are routed through SetFlagErrorFunc instead.
func isCobraUsageError(err error) bool {
	m := err.Error()
	return strings.HasPrefix(m, "unknown command ") ||
		strings.HasPrefix(m, "required flag(s) ") ||
		strings.HasPrefix(m, "accepts ") ||
		strings.HasPrefix(m, "unknown flag") ||
		strings.Contains(m, " arg(s), received ")
}

// exitCodeFor maps an error to its process exit code (0 for nil).
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	if isCobraUsageError(err) {
		return exitUsage
	}
	return exitGeneric
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "pb:", err)
		os.Exit(exitCodeFor(err))
	}
}
