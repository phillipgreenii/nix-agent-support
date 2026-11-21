package main

import (
	"context"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/daemon"
)

// runStatus is the `status [--json]` mode: the kernel's status skeleton
// (daemon.RunStatus) over this daemon's collector, which asks the daemon for its
// report (or builds the static one when it does not answer) and exits by its
// health: 0 healthy, 2 degraded, 3 down.
func runStatus(args []string, cfg *config.Config, stdout, stderr io.Writer) int {
	return daemon.RunStatus(context.Background(), "pg-connector-issue-jira", usage, args, stdout, stderr,
		func(ctx context.Context) daemon.StatusReport {
			r := client.CollectStatus(ctx, clientOptions(cfg))
			return daemon.FuncReport{
				Text: func(w io.Writer) error { return status.Render(w, r) },
				JSON: func(w io.Writer) error { return status.RenderJSON(w, r) },
				Code: r.Exit(),
			}
		})
}
