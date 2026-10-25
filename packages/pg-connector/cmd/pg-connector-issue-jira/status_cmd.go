package main

import (
	"context"
	"fmt"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/ops/status"
)

// parseStatusArgs reads the arguments after the status mode word: --json is the
// only one.
func parseStatusArgs(args []string) (asJSON bool, err error) {
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		default:
			return false, fmt.Errorf("unknown argument %q", a)
		}
	}
	return asJSON, nil
}

// runStatus is the `status [--json]` mode: ask the daemon for its report (or
// build the static one when it does not answer), print it as text or as the
// JSON document, and exit by its health: 0 healthy, 2 degraded, 3 down.
func runStatus(args []string, cfg *config.Config, stdout, stderr io.Writer) int {
	asJSON, err := parseStatusArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira status: %v\n\n%s", err, usage)
		return exitUsage
	}
	r := client.CollectStatus(context.Background(), clientOptions(cfg))
	render := status.Render
	if asJSON {
		render = status.RenderJSON
	}
	if err := render(stdout, r); err != nil {
		fmt.Fprintf(stderr, "pg-connector-issue-jira status: %v\n", err)
		return 1
	}
	return r.Exit()
}
