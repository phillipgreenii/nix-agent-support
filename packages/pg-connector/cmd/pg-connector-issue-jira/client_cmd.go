package main

import (
	"context"
	"io"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/client"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/logs"
)

// clientBudgetOverride is a build-time seam (-ldflags "-X main.clientBudgetOverride=4s"),
// never set in a shipped build: when it parses as a positive duration it
// replaces the call budget of a forwarded read and of the status mode, so a
// real-binary test need not wait out the 30 s production budget to see a
// daemon-down answer. Like the daemon's serveHooks it is a link-time string,
// not an environment variable: the client reads the caller's environment only
// through the origin tool it runs for a write.
var clientBudgetOverride = ""

// clientOptions is the client's configuration for the loaded config.
func clientOptions(cfg *config.Config) client.Options {
	o := client.Options{
		Config:    cfg,
		Build:     Version,
		ClientLog: logs.NewClientLog(cfg.StateDir, Version),
	}
	if d, err := time.ParseDuration(clientBudgetOverride); err == nil && d > 0 {
		o.Budget = d
	}
	return o
}

// runClient answers one scriptout request.
func runClient(cfg *config.Config, stdin io.Reader, stdout io.Writer) int {
	return client.Run(context.Background(), clientOptions(cfg), stdin, stdout)
}
