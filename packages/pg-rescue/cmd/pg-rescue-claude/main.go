// Command pg-rescue-claude is a pg-rescue failure handler that runs
// `claude -p` synchronously and returns the agent's verdict.
package main

import (
	"io"
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/claudehandler"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version" — matching every sibling Go binary's own convention).
var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}

// run is main without the process: the handler's arguments, its environment
// (KEY=VALUE entries) and its streams in, its exit code out. The tests call it
// in process.
func run(args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	rt := claudehandler.DefaultRuntime()
	rt.Environ, rt.Stdin, rt.Stdout, rt.Stderr = env, stdin, stdout, stderr
	return claudehandler.Main(args, rt)
}
