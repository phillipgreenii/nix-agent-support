// runner.go: the exec boundary to the real `pa-monitor` CLI — mirrors
// pg-connector-issue-beads' Runner/CLIRunner split (production execs the
// real binary; tests inject a fake).
package internal

import (
	"context"
	"os/exec"
)

// Runner is this backend's exec seam.
type Runner interface {
	Status(ctx context.Context) ([]byte, error)
	Info(ctx context.Context, selector string) ([]byte, error)
	Search(ctx context.Context, query, sessionID string) ([]byte, error)
}

// CLIRunner execs the real pa-monitor binary (resolved on $PATH).
type CLIRunner struct{}

func NewCLIRunner() *CLIRunner { return &CLIRunner{} }

func (CLIRunner) Status(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "pa-monitor", "status", "--json").Output()
}

func (CLIRunner) Info(ctx context.Context, selector string) ([]byte, error) {
	return exec.CommandContext(ctx, "pa-monitor", "info", selector, "--json").Output()
}

func (CLIRunner) Search(ctx context.Context, query, sessionID string) ([]byte, error) {
	args := []string{"search", query}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	return exec.CommandContext(ctx, "pa-monitor", args...).Output()
}
