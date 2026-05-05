package prreview

import "errors"

var (
	// ErrPRNotFound is returned when the PR cannot be found.
	ErrPRNotFound = errors.New("pull request not found")

	// ErrMultiplePRsMatch is returned when multiple PRs match the query.
	ErrMultiplePRsMatch = errors.New("multiple pull requests match")

	// ErrCommitMismatch is returned when the fetched commit doesn't match the PR.
	ErrCommitMismatch = errors.New("commit SHA mismatch - PR may have been updated")

	// ErrWorktreeExists is returned when the worktree already exists and cleanup failed.
	ErrWorktreeExists = errors.New("worktree already exists")

	// ErrWorktreeCreateFailed is returned when worktree creation fails.
	ErrWorktreeCreateFailed = errors.New("failed to create worktree")

	// ErrNoJSONFound is returned when no JSON object is found in input.
	ErrNoJSONFound = errors.New("no JSON object found in input")

	// ErrInvalidJSON is returned when extracted JSON is not parseable.
	ErrInvalidJSON = errors.New("invalid JSON in input")

	// ErrGitHubAPIFailed is returned when a GitHub API call fails.
	ErrGitHubAPIFailed = errors.New("GitHub API call failed")

	// ErrGitOperationFailed is returned when a git operation fails.
	ErrGitOperationFailed = errors.New("git operation failed")
)
