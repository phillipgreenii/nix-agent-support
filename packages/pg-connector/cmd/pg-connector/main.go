package main

import (
	"errors"
	"fmt"
	"os"
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
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	root := newRootCmd()
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return 0
	}

	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}

	fmt.Fprintln(os.Stderr, err)
	return 1
}
