package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/x/exitcode"
)

// exitError carries a specific process exit code without printing anything
// to stderr — the JSON body already written to stdout by the fan-out/
// targeted-op command is the reported outcome, matching the wire
// protocol's own "only stdout JSON is the contract" convention. These are
// pg-connector's OWN CLI exit codes (see outcome.go), a different layer
// from the wire protocol's per-backend 0/1 exec exit codes.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("pg-connector: exit %d", e.code) }

func main() {
	// SIGINT and SIGTERM cancel the command's context, so a backend child in
	// flight is ended (its whole process group, via x/cmdrun) instead of
	// outliving this process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runContext(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(args []string) int { return runContext(context.Background(), args) }

func runContext(ctx context.Context, args []string) int {
	root := newRootCmd()
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitcode.OK
	}

	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}

	fmt.Fprintln(os.Stderr, err)
	if isConfigError(err) {
		return exitcode.Config
	}
	return exitcode.Error
}
