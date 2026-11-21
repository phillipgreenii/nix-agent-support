// main.go: pg-router-source-pg-desk's cobra root wiring and process entry
// point. The adapter holds no state and has no compile-time dependency on
// packages/pg-desk: it execs the pg-desk binary (ambient on $PATH) and
// parses its stdout JSON generically.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/phillipgreenii/pg-router/api/handlerkit"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version").
var Version = "dev"

// errFailed is returned by RunE once its own diagnostic has already been
// written to stderr; run must not print it again. Any OTHER non-nil error
// (cobra's own flag/arg validation) has not been reported yet and is
// printed there.
var errFailed = errors.New("pg-router-source-pg-desk: command failed")

// run executes the adapter with args, writing to stdout/stderr (threaded
// explicitly so tests can capture them). The exit scheme is 0/1, plus 2 for a settle usage error.
func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)

	if err := root.ExecuteContext(context.Background()); err != nil {
		if errors.Is(err, errUsage) {
			return 2
		}
		if !errors.Is(err, errFailed) {
			fmt.Fprintln(stderr, err)
		}
		return 1
	}
	return 0
}

func main() {
	handlerkit.Main(handlerkit.Adapter{Name: "pg-router-source-pg-desk", Run: run})
}
