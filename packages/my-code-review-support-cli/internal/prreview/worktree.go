package prreview

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// WorktreeManager handles git worktree operations.
type WorktreeManager struct {
	executor CommandExecutor
	workDir  string
}

// NewWorktreeManager creates a new WorktreeManager.
func NewWorktreeManager(workDir string, executor CommandExecutor) *WorktreeManager {
	return &WorktreeManager{
		executor: executor,
		workDir:  workDir,
	}
}

// FetchAndVerify fetches a branch and verifies its commit SHA matches the expected value.
func (m *WorktreeManager) FetchAndVerify(ctx context.Context, branch, expectedSHA string) error {
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)
	_, stderr, err := m.executor.Execute(ctx, m.workDir,
		"git", "fetch", "origin", refspec,
	)
	if err != nil {
		return fmt.Errorf("%w: fetch branch %s: %s", ErrGitOperationFailed, branch, string(stderr))
	}

	stdout, stderr, err := m.executor.Execute(ctx, m.workDir,
		"git", "rev-parse", fmt.Sprintf("origin/%s", branch),
	)
	if err != nil {
		return fmt.Errorf("%w: rev-parse branch %s: %s", ErrGitOperationFailed, branch, string(stderr))
	}

	actualSHA := strings.TrimSpace(string(stdout))
	if actualSHA != expectedSHA {
		return fmt.Errorf("%w: expected %s, got %s for branch %s", ErrCommitMismatch, expectedSHA, actualSHA, branch)
	}

	return nil
}

// CreateWorktree creates a detached worktree for the given branch.
// If a worktree already exists at the path, it is removed first.
func (m *WorktreeManager) CreateWorktree(ctx context.Context, branch, path string) error {
	if _, err := os.Stat(path); err == nil {
		if err := m.RemoveWorktree(ctx, path); err != nil {
			return fmt.Errorf("%w: path %s: %s", ErrWorktreeExists, path, err.Error())
		}
	}

	_, stderr, err := m.executor.Execute(ctx, m.workDir,
		"git", "worktree", "add", "--detach", path, fmt.Sprintf("origin/%s", branch),
	)
	if err != nil {
		return fmt.Errorf("%w: path %s, branch %s: %s", ErrWorktreeCreateFailed, path, branch, string(stderr))
	}

	return nil
}

// RemoveWorktree removes a worktree at the given path.
func (m *WorktreeManager) RemoveWorktree(ctx context.Context, path string) error {
	_, _, err := m.executor.Execute(ctx, m.workDir,
		"git", "worktree", "remove", path, "--force",
	)
	if err == nil {
		return nil
	}

	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("%w: remove worktree %s: %s", ErrGitOperationFailed, path, err.Error())
	}

	_, _, _ = m.executor.Execute(ctx, m.workDir, "git", "worktree", "prune")

	return nil
}

// GetChangedFiles returns a list of changed files with stats.
func (m *WorktreeManager) GetChangedFiles(ctx context.Context, base string) (*FilesResult, error) {
	if base == "" {
		base = "origin/main"
	}

	stdout, stderr, err := m.executor.Execute(ctx, m.workDir,
		"git", "diff", "--numstat", fmt.Sprintf("%s...HEAD", base),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: diff --numstat %s: %s", ErrGitOperationFailed, base, string(stderr))
	}

	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	files := make([]FileInfo, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		var additions, deletions int
		if parts[0] != "-" {
			fmt.Sscanf(parts[0], "%d", &additions)
		}
		if parts[1] != "-" {
			fmt.Sscanf(parts[1], "%d", &deletions)
		}

		files = append(files, FileInfo{
			Path:      parts[2],
			Additions: additions,
			Deletions: deletions,
		})
	}

	return &FilesResult{Files: files}, nil
}

// GetCommits returns a list of commits with messages.
func (m *WorktreeManager) GetCommits(ctx context.Context, base string) (*CommitsResult, error) {
	if base == "" {
		base = "origin/main"
	}

	stdout, stderr, err := m.executor.Execute(ctx, m.workDir,
		"git", "log", fmt.Sprintf("%s...HEAD", base),
		"--format=%H%x00%s%x00%b%x00%an <%ae>%x00",
	)
	if err != nil {
		return nil, fmt.Errorf("%w: log %s: %s", ErrGitOperationFailed, base, string(stderr))
	}

	output := strings.TrimSpace(string(stdout))
	if output == "" {
		return &CommitsResult{Commits: []CommitInfo{}}, nil
	}

	commitStrings := strings.Split(output, "\x00\x00")
	commits := make([]CommitInfo, 0, len(commitStrings))

	for _, commitStr := range commitStrings {
		if commitStr == "" {
			continue
		}

		parts := strings.Split(commitStr, "\x00")
		if len(parts) < 4 {
			continue
		}

		commits = append(commits, CommitInfo{
			SHA:     parts[0],
			Subject: parts[1],
			Body:    strings.TrimSpace(parts[2]),
			Author:  parts[3],
		})
	}

	return &CommitsResult{Commits: commits}, nil
}

// WorktreePath returns the standard worktree path for a PR.
func WorktreePath(prNumber int) string {
	return fmt.Sprintf("/tmp/pr-review-%d", prNumber)
}
