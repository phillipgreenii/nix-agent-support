// Package branch implements the `pg-pr branch detect` subcommand.
//
// Detect inspects a given working directory and returns the current git
// branch, the worktree root, the remote-derived "owner/repo" identifier,
// the configured base branch (Phase 1: hard-coded to origin/main), and the
// open PR number for the branch (if any).
//
// PR lookup is best-effort: when the `gh` CLI is not available, or the
// branch has no associated open PR, PRNumber is nil and Detect still
// returns successfully. The only fatal condition is "not in a git
// repository".
package branch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitenv"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
)

// Options carries injectable dependencies for Detect. Production callers
// can pass a zero value; tests inject fakes.
type Options struct {
	// Git executes git subcommands inside the given directory. If nil,
	// the default shell-out implementation is used.
	Git GitRunner

	// GH looks up the open PR associated with the current branch. If
	// nil, a default CLI-backed implementation is used. Any error from
	// GH is treated as "no PR" rather than fatal.
	GH GHClient
}

// Detect inspects cwd and returns a populated BranchInfo.
//
// It returns an error if cwd is not inside a git repository. All other
// failure modes (missing remote, non-GitHub remote, no PR, gh missing)
// degrade gracefully: the corresponding field is left zero/nil.
func Detect(ctx context.Context, cwd string, opts Options) (*api.BranchInfo, error) {
	if cwd == "" {
		return nil, errors.New("cwd is required")
	}
	if opts.Git == nil {
		opts.Git = NewCLIGitRunner()
	}
	if opts.GH == nil {
		opts.GH = NewCLIGHClient()
	}

	// Worktree root — also doubles as our "is this a git repo?" check.
	root, err := opts.Git.WorktreeRoot(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("not in a git repository: %s", cwd)
	}

	branchName, err := opts.Git.CurrentBranch(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("read current branch: %w", err)
	}

	// Remote-derived repo identifier is best-effort.
	repo := ""
	if remote, err := opts.Git.RemoteOriginURL(ctx, cwd); err == nil {
		if owner, name, ok := parseGitHubRemote(remote); ok {
			repo = owner + "/" + name
		}
	}

	// PR lookup is best-effort: any failure -> nil PR. That deliberately
	// includes an unresolvable gh credential — `branch detect` still reports the
	// local git facts, and the GH client refused before executing gh, so no
	// interactive login can be triggered here (bead pg2-ilzq9).
	var prNum *int
	if n, err := opts.GH.PRForBranch(ctx, cwd); err == nil {
		prNum = n
	}

	return &api.BranchInfo{
		Repo:         repo,
		Branch:       branchName,
		Base:         defaultBase(),
		WorktreeRoot: root,
		PRNumber:     prNum,
	}, nil
}

// defaultBase returns the placeholder base ref for Phase 1. A later phase
// will integrate config lookup.
func defaultBase() string { return "origin/main" }

// ----------------------------------------------------------------------
// GitRunner
// ----------------------------------------------------------------------

// GitRunner abstracts the small slice of git operations Detect needs.
type GitRunner interface {
	// WorktreeRoot returns `git -C dir rev-parse --show-toplevel`.
	WorktreeRoot(ctx context.Context, dir string) (string, error)
	// CurrentBranch returns `git -C dir rev-parse --abbrev-ref HEAD`.
	CurrentBranch(ctx context.Context, dir string) (string, error)
	// RemoteOriginURL returns the URL of remote.origin or an error if
	// no such remote is configured.
	RemoteOriginURL(ctx context.Context, dir string) (string, error)
}

// CLIGitRunner shells out to the system `git` binary.
type CLIGitRunner struct{}

// NewCLIGitRunner returns the default GitRunner.
func NewCLIGitRunner() GitRunner { return &CLIGitRunner{} }

func (g *CLIGitRunner) WorktreeRoot(ctx context.Context, dir string) (string, error) {
	out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g *CLIGitRunner) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (g *CLIGitRunner) RemoteOriginURL(ctx context.Context, dir string) (string, error) {
	out, err := runGit(ctx, dir, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runGit invokes `git -C dir <args...>` and returns stdout. Errors include
// captured stderr to aid debugging.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// gitenv.Command owns the child environment: a leaked GIT_DIR /
	// GIT_INDEX_FILE outranks `-C dir`, so passing dir alone is not enough to
	// keep this call inside dir. See internal/gitenv (pg2-lx41y).
	cmd := gitenv.Command(ctx, dir, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), fmt.Errorf("git %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// ----------------------------------------------------------------------
// GHClient
// ----------------------------------------------------------------------

// GHClient looks up the open PR for the branch checked out in dir. It
// returns nil (no PR) and a nil error in the "no PR" case; any
// gh-invocation failure is returned as an error so callers can choose to
// log or ignore.
type GHClient interface {
	PRForBranch(ctx context.Context, dir string) (*int, error)
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

// CLIGHClient invokes the `gh` binary. It uses `gh pr view --json
// number,baseRefName` which infers the PR from the current branch.
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

// NewCLIGHClient returns a CLI-backed GHClient.
func NewCLIGHClient() GHClient { return &CLIGHClient{} }

// commander returns the injected commander or the shared default.
func (c *CLIGHClient) commander() ghCommander {
	if c.gh != nil {
		return c.gh
	}
	return defaultGHCommander
}

// noPRPatterns are substrings that indicate "no PR exists" — not a
// real error. Empirically `gh pr view` prints "no pull requests found
// for branch ..." on stderr and exits non-zero. We treat that as success
// with nil PR.
var noPRPatterns = []string{
	"no pull requests found",
	"no open pull requests found",
}

func (c *CLIGHClient) PRForBranch(ctx context.Context, dir string) (*int, error) {
	cmd, err := c.commander().Command(ctx, "pr", "view",
		"--json", "number,baseRefName",
		"--jq", "{n: .number, b: .baseRefName}")
	if err != nil {
		// Token resolution failed: no process was created, so gh was never
		// executed. The error already wraps ErrGHAuthInvalid and names
		// `gh auth login`.
		return nil, err
	}
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrTxt := strings.ToLower(stderr.String())
		for _, pat := range noPRPatterns {
			if strings.Contains(stderrTxt, pat) {
				return nil, nil
			}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			st := strings.TrimSpace(stderr.String())
			if github.IsAuthFailure(exitErr.ExitCode(), st) {
				return nil, fmt.Errorf("gh pr view: %s: run `gh auth login`: %w",
					st, github.ErrGHAuthInvalid)
			}
			return nil, fmt.Errorf("gh pr view: %s", st)
		}
		return nil, fmt.Errorf("invoke gh: %w (is the gh CLI on PATH?)", err)
	}

	var parsed struct {
		N *int   `json:"n"`
		B string `json:"b"`
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("parse gh pr view JSON: %w", err)
	}
	return parsed.N, nil
}

// ----------------------------------------------------------------------
// Remote URL parsing
// ----------------------------------------------------------------------

var ghRemoteRE = regexp.MustCompile(`github\.com[:/]([^/]+)/(.+?)(?:\.git)?$`)

// parseGitHubRemote extracts owner/repo from a github remote URL. ok is
// false for non-GitHub URLs.
func parseGitHubRemote(url string) (owner, repo string, ok bool) {
	m := ghRemoteRE.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}
