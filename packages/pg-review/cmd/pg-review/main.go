// Command pg-review is the self-review tool: a CLI grouped by audience
// (human, agent, job). See the package README.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/cli"
)

// Version is set at build time via -ldflags (mkGoApp's default target).
var Version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := cli.Execute(ctx, Version, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
