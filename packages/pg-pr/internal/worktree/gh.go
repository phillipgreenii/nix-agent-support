package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// GHClient abstracts the small slice of `gh` CLI behavior the worktree
// package needs. Currently this is just "does PR N exist on owner/repo?"
// — implemented via `gh pr view --json number`.
type GHClient interface {
	// PRExists returns nil-ish info about the PR (just the number, for now)
	// if it exists. A non-nil error indicates the PR could not be looked up
	// (auth missing, gh not on PATH, or PR not found).
	PRExists(ctx context.Context, owner, repo string, pr int) (*PRInfo, error)
}

// PRInfo is a minimal placeholder; later phases may grow it.
type PRInfo struct {
	Number int `json:"number"`
}

// CLIGHClient invokes the `gh` binary.
type CLIGHClient struct{}

// NewCLIGHClient returns a GHClient backed by the system `gh` binary.
func NewCLIGHClient() GHClient { return &CLIGHClient{} }

func (c *CLIGHClient) PRExists(ctx context.Context, owner, repo string, pr int) (*PRInfo, error) {
	repoFlag := fmt.Sprintf("%s/%s", owner, repo)
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", fmt.Sprintf("%d", pr),
		"--repo", repoFlag, "--json", "number")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrTxt := strings.TrimSpace(stderr.String())
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Surface gh's stderr — it usually says "no such PR" or
			// "authentication required".
			return nil, fmt.Errorf("gh pr view %d --repo %s: %s",
				pr, repoFlag, stderrTxt)
		}
		// Likely "gh: command not found".
		return nil, fmt.Errorf("invoke gh: %w (is the gh CLI on PATH?)", err)
	}

	var info PRInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("parse gh pr view JSON: %w", err)
	}
	return &info, nil
}
