// Command pg-rescue wraps a command and, when it fails, tries an ordered,
// caller-named chain of failure handlers. It also provides the `result` and
// `check` subcommands.
package main

import (
	"os"
	"syscall"

	"github.com/phillipgreenii/pg-rescue/internal/app"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version" — matching every sibling Go binary's own convention).
var Version = "dev"

func main() {
	// Everything the wrapper writes under the state root is private to the
	// user, whatever the user's umask.
	syscall.Umask(0o077)
	os.Exit(app.Main(app.DefaultRuntime(Version), os.Args[1:], os.Stdout, os.Stderr))
}
