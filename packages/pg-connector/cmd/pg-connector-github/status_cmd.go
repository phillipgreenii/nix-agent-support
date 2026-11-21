package main

import (
	"context"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemon"
)

// runStatus is the `status [--json]` subcommand: the kernel's status skeleton
// (daemon.RunStatus) over this daemon's collector, which asks the daemon for its
// report (or builds the local one when it does not answer) and exits by its
// health: 0 healthy, 2 degraded, 3 down (INV-EXIT-1).
func runStatus(args []string, cfg *config.Config, stdout, stderr io.Writer) int {
	return daemon.RunStatus(context.Background(), "pg-connector-github", usage, args, stdout, stderr,
		func(ctx context.Context) daemon.StatusReport {
			c := status.Collector{Static: status.StaticFromConfig(cfg, Version)}
			if d, err := time.ParseDuration(clientBudgetOverride); err == nil && d > 0 {
				c.Budget = d
			}
			r := c.Collect(ctx)
			return daemon.FuncReport{
				Text: func(w io.Writer) error { return status.WriteText(w, r) },
				JSON: func(w io.Writer) error { return status.WriteJSON(w, r) },
				Code: r.Health.ExitCode(),
			}
		})
}
