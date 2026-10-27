// Command pg-rescue wraps a command and, when it fails, tries an ordered,
// caller-named chain of failure handlers. It also provides the `result` and
// `check` subcommands.
package main

import (
	"io"
	"os"
	"syscall"

	"github.com/phillipgreenii/pg-rescue/internal/app"
	"github.com/phillipgreenii/pg-rescue/internal/environ"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version" — matching every sibling Go binary's own convention).
var Version = "dev"

func main() {
	// Everything the wrapper writes under the state root is private to the
	// user, whatever the user's umask. This is process-wide state, so it stays
	// here and not in run.
	syscall.Umask(0o077)
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}

// run is main without the process: the wrapper's arguments, its environment
// (KEY=VALUE entries) and its streams in, its exit code out. The tests call it
// in process. The wrapper's PATH lookups use the process PATH, which in
// production is the one in env.
func run(args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	rt := app.DefaultRuntime(Version)
	rt.Getenv = environ.Getenv(env)
	rt.Environ = func() []string { return env }
	if home := rt.Getenv("HOME"); home != "" {
		rt.Home = home
	}
	rt.Stdin = stdin
	return app.Main(rt, args, stdout, stderr)
}
