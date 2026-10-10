package main

import (
	"errors"
	"fmt"
	"os"
	"time"

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
	root.AddCommand(newGateCmd())
	root.AddCommand(newDrainCmd())
	root.AddCommand(newUnstickCmd())
	return root
}

// Process exit codes. Commands that need a code other than the default return
// an error built with newExitError; everything else exits exitGeneric.
const (
	exitGeneric = 1 // usage, IO or internal error
	exitBD      = 2 // a bd call failed (pb unstick)
)

// exitError carries a specific process exit code through the error return path.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func newExitError(code int, err error) error { return &exitError{code: code, err: err} }

// exitCodeFor maps an error to its process exit code (0 for nil).
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return exitGeneric
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "pb:", err)
		os.Exit(exitCodeFor(err))
	}
}
