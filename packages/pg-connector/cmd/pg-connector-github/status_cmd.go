package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
)

// runStatus is the `status [--json]` subcommand: ask the daemon for its report
// (or build the local one when it does not answer), print it as text or as the
// JSON document, and exit by its health: 0 healthy, 2 degraded, 3 down
// (INV-EXIT-1).
func runStatus(args []string, cfg *config.Config, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		default:
			fmt.Fprintf(stderr, "pg-connector-github status: unknown argument %q\n\n%s", a, usage)
			return exitUsage
		}
	}
	c := status.Collector{Static: status.StaticFromConfig(cfg, Version)}
	if d, err := time.ParseDuration(clientBudgetOverride); err == nil && d > 0 {
		c.Budget = d
	}
	r := c.Collect(context.Background())
	write := status.WriteText
	if asJSON {
		write = status.WriteJSON
	}
	if err := write(stdout, r); err != nil {
		fmt.Fprintf(stderr, "pg-connector-github status: %v\n", err)
		return 1
	}
	return r.Health.ExitCode()
}
