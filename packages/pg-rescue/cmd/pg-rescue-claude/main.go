// Command pg-rescue-claude is a pg-rescue failure handler that runs
// `claude -p` synchronously and returns the agent's verdict.
package main

import (
	"os"

	"github.com/phillipgreenii/pg-rescue/internal/claudehandler"
)

// Version is injected at build time by mkGoApp (versionPath defaults to
// "main.Version" — matching every sibling Go binary's own convention).
var Version = "dev"

func main() {
	os.Exit(claudehandler.Main(os.Args[1:], claudehandler.DefaultRuntime()))
}
