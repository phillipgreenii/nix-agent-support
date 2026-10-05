// runner.go: the exec boundary to the real `pa-monitor` CLI — mirrors
// pg-connector-issue-beads' Runner/CLIRunner split (production execs the
// real binary; tests inject a fake).
package internal

import (
	"context"
	"os/exec"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Runner is this backend's exec seam.
type Runner interface {
	Status(ctx context.Context) ([]byte, error)
	Info(ctx context.Context, selector string) ([]byte, error)
	// Search runs `pa-monitor search`. rng, when non-zero, is forwarded as
	// --since/--before (always RFC3339 instants, never the umbrella's raw
	// "7d", so pa-monitor's own bound parser need not know the day suffix).
	Search(ctx context.Context, query, sessionID string, rng scriptout.TimeRange) ([]byte, error)
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

func (CLIRunner) Search(ctx context.Context, query, sessionID string, rng scriptout.TimeRange) ([]byte, error) {
	return exec.CommandContext(ctx, "pa-monitor", searchArgs(query, sessionID, rng)...).Output()
}

// searchArgs builds the `pa-monitor search` argv (exposed for tests).
func searchArgs(query, sessionID string, rng scriptout.TimeRange) []string {
	args := []string{"search", query}
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	if !rng.Since.IsZero() {
		args = append(args, "--since", rng.Since.UTC().Format(time.RFC3339))
	}
	if !rng.Before.IsZero() {
		args = append(args, "--before", rng.Before.UTC().Format(time.RFC3339))
	}
	return args
}
