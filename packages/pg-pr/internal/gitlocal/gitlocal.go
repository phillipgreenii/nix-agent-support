// Package gitlocal provides pure-git helpers used by the `pg-pr pr files`
// and `pg-pr pr commits` subcommands. Everything here operates on the local
// repository — no GitHub API calls.
package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/gitenv"
)

// FileChange is one entry from `git diff --numstat`.
type FileChange struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

// Commit is one entry from `git log`.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Author  string `json:"author"`
}

// Runner abstracts `git` invocations so tests can inject canned output.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// CLIRunner shells out to the system git binary.
type CLIRunner struct{}

// NewCLIRunner returns the production Runner.
func NewCLIRunner() Runner { return &CLIRunner{} }

func (CLIRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
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
			return stdout.Bytes(), fmt.Errorf("git %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return stdout.Bytes(), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// ChangedFiles returns the file changes between base and HEAD using
// `git diff --numstat base...HEAD`. base defaults to "origin/main".
func ChangedFiles(ctx context.Context, r Runner, dir, base string) ([]FileChange, error) {
	if r == nil {
		r = NewCLIRunner()
	}
	if base == "" {
		base = "origin/main"
	}
	out, err := r.Run(ctx, dir, "diff", "--numstat", fmt.Sprintf("%s...HEAD", base))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	files := make([]FileChange, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		fc := FileChange{Path: parts[2]}
		// `-` indicates binary file.
		if parts[0] == "-" && parts[1] == "-" {
			fc.Binary = true
		} else {
			_, _ = fmt.Sscanf(parts[0], "%d", &fc.Additions)
			_, _ = fmt.Sscanf(parts[1], "%d", &fc.Deletions)
		}
		files = append(files, fc)
	}
	return files, nil
}

// Commits returns the commits between base and HEAD using `git log
// base..HEAD`. base defaults to "origin/main". Bodies are trimmed.
func Commits(ctx context.Context, r Runner, dir, base string) ([]Commit, error) {
	if r == nil {
		r = NewCLIRunner()
	}
	if base == "" {
		base = "origin/main"
	}
	out, err := r.Run(
		ctx, dir, "log",
		fmt.Sprintf("%s..HEAD", base),
		"--format=%H%x00%s%x00%b%x00%an <%ae>",
		"-z",
	)
	if err != nil {
		return nil, err
	}
	// With -z, git separates log records with a single NUL byte rather than
	// LF, and the format string we use ends with the author. So records are
	// delimited by exactly one \x00 *after* the 4 fields. Each record itself
	// contains 4 fields delimited by \x00, so the stream is a flat NUL-
	// separated sequence of fields, grouped 4 at a time.
	output := strings.TrimRight(string(out), "\x00")
	if output == "" {
		return []Commit{}, nil
	}
	fields := strings.Split(output, "\x00")
	commits := make([]Commit, 0, len(fields)/4)
	for i := 0; i+3 < len(fields); i += 4 {
		commits = append(commits, Commit{
			SHA:     fields[i],
			Subject: fields[i+1],
			Body:    strings.TrimSpace(fields[i+2]),
			Author:  fields[i+3],
		})
	}
	return commits, nil
}
