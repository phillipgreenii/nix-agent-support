// Command pg-rescue-notify is a pg-rescue reference handler: it posts a macOS
// notification about the failure and declines, so the chain continues.
package main

import (
	"io"
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/environ"
	"github.com/phillipgreenii/pg-rescue/internal/notify"
)

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}

// run is main without the process: the handler's arguments, its environment
// (KEY=VALUE entries) and its streams in, its exit code out. The tests call it
// in process; the handler reads no standard input. Like every handler it finds
// osascript on the process PATH.
func run(args, env []string, _ io.Reader, stdout, stderr io.Writer) int {
	rt := notify.DefaultRuntime()
	rt.Getenv = environ.Getenv(env)
	return notify.Run(rt, args, stdout, stderr)
}
