// Command pg-rescue-bead is a pg-rescue failure handler that defers the work
// by filing a self-contained bead, or annotating an existing one, through
// pg-connector's beads issue backend. It exits 3 (deferred) on success.
package main

import (
	"io"
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/beadhandler"
	"github.com/phillipgreenii/pg-rescue/internal/environ"
)

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}

// run is main without the process: the handler's arguments, its environment
// (KEY=VALUE entries) and its streams in, its exit code out. The tests call it
// in process; the handler reads no standard input.
func run(args, env []string, _ io.Reader, stdout, stderr io.Writer) int {
	rt := beadhandler.DefaultRuntime()
	rt.Getenv = environ.Getenv(env)
	rt.Environ = func() []string { return env }
	return beadhandler.Main(rt, args, stdout, stderr)
}
