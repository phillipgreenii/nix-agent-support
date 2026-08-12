package worktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
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

// ghCommander builds a token-protected `gh` command. *github.CLI is the
// production implementation; tests inject a fake. Declared here so this package
// depends on the one method it needs.
type ghCommander interface {
	Command(ctx context.Context, args ...string) (*exec.Cmd, error)
}

// defaultGHCommander is the shared gateway used when no commander is injected.
// Construction does no I/O; the token resolves lazily and is cached.
var defaultGHCommander ghCommander = github.NewCLI()

// CLIGHClient invokes the `gh` binary.
//
// It never execs gh directly: the command is built by a token-protected
// commander (bead pg2-ilzq9), so a missing/expired credential yields an
// ErrGHAuthInvalid-wrapped error with no process created — an unauthenticated gh
// (which would start its own interactive login) is unreachable from here.
type CLIGHClient struct {
	// gh is the commander used to build the `gh` invocation. nil selects the
	// shared default, so the zero value is safe.
	gh ghCommander
}

// NewCLIGHClient returns a GHClient backed by the system `gh` binary.
func NewCLIGHClient() GHClient { return &CLIGHClient{} }

// commander returns the injected commander or the shared default.
func (c *CLIGHClient) commander() ghCommander {
	if c.gh != nil {
		return c.gh
	}
	return defaultGHCommander
}

func (c *CLIGHClient) PRExists(ctx context.Context, owner, repo string, pr int) (*PRInfo, error) {
	repoFlag := fmt.Sprintf("%s/%s", owner, repo)
	cmd, err := c.commander().Command(ctx, "pr", "view", fmt.Sprintf("%d", pr),
		"--repo", repoFlag, "--json", "number")
	if err != nil {
		// Token resolution failed: no process was created, so gh was never
		// executed. The error already wraps ErrGHAuthInvalid and names
		// `gh auth login`.
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrTxt := strings.TrimSpace(stderr.String())
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if github.IsAuthFailure(exitErr.ExitCode(), stderrTxt) {
				return nil, fmt.Errorf("gh pr view %d --repo %s: %s: run `gh auth login`: %w",
					pr, repoFlag, stderrTxt, github.ErrGHAuthInvalid)
			}
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
