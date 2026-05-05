package prreview

import (
	"context"
	"fmt"
	"time"
)

// Client coordinates the PR review workflow.
type Client struct {
	github   *GitHubClient
	worktree *WorktreeManager
	workDir  string
}

// NewClient creates a new Client with the given work directory.
func NewClient(workDir string) *Client {
	executor := NewRealExecutor(2 * time.Minute)
	return &Client{
		github:   NewGitHubClient(workDir, executor),
		worktree: NewWorktreeManager(workDir, executor),
		workDir:  workDir,
	}
}

// NewClientWithExecutor creates a new Client with a custom executor (for testing).
func NewClientWithExecutor(workDir string, executor CommandExecutor) *Client {
	return &Client{
		github:   NewGitHubClient(workDir, executor),
		worktree: NewWorktreeManager(workDir, executor),
		workDir:  workDir,
	}
}

// Setup identifies a PR, fetches and verifies the commit, and creates a worktree.
func (c *Client) Setup(ctx context.Context, arg string) (*SetupResult, error) {
	pr, err := c.github.IdentifyPR(ctx, arg)
	if err != nil {
		return nil, fmt.Errorf("identify PR: %w", err)
	}

	if err := c.worktree.FetchAndVerify(ctx, pr.HeadRefName, pr.HeadRefOid); err != nil {
		return nil, fmt.Errorf("fetch and verify: %w", err)
	}

	worktreePath := WorktreePath(pr.Number)
	if err := c.worktree.CreateWorktree(ctx, pr.HeadRefName, worktreePath); err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	return &SetupResult{
		PRNumber:     pr.Number,
		Title:        pr.Title,
		HeadBranch:   pr.HeadRefName,
		BaseBranch:   pr.BaseRefName,
		URL:          pr.URL,
		CommitSHA:    pr.HeadRefOid,
		WorktreePath: worktreePath,
	}, nil
}

// Post parses review output, deduplicates comments, and posts them to GitHub.
func (c *Client) Post(ctx context.Context, prNumber int, rawInput string) (*PostResult, error) {
	jsonStr, err := ExtractJSON(rawInput)
	if err != nil {
		return nil, fmt.Errorf("extract JSON: %w", err)
	}

	reviewOutput, err := ParseReviewOutput(jsonStr)
	if err != nil {
		return nil, fmt.Errorf("parse review output: %w", err)
	}

	fileComments, prLevelComments := TransformToGitHubComments(reviewOutput.Comments)

	if len(fileComments) == 0 && len(prLevelComments) == 0 {
		return &PostResult{
			CommentsPosted:    0,
			DuplicatesSkipped: 0,
			PRLevelComments:   0,
			Mode:              "none",
		}, nil
	}

	existingReview, err := c.github.GetPendingReview(ctx, prNumber)
	if err != nil {
		return nil, fmt.Errorf("get pending review: %w", err)
	}

	var existingComments []ExistingComment
	if existingReview != nil {
		existingComments, _ = c.github.GetReviewComments(ctx, prNumber, existingReview.ID)
	}

	uniqueComments, skipped := DeduplicateComments(fileComments, existingComments)

	reviewBody := BuildReviewBody(prLevelComments)

	var mode string
	var posted int

	if existingReview != nil {
		mode = "appended_to_existing"
		for _, comment := range uniqueComments {
			if err := c.github.AddCommentToReview(ctx, prNumber, existingReview.ID, comment); err == nil {
				posted++
			}
		}

		if reviewBody != "" {
			newBody := existingReview.Body
			if newBody != "" {
				newBody += "\n\n---\n\n"
			}
			newBody += reviewBody
			_ = c.github.UpdateReviewBody(ctx, prNumber, existingReview.ID, newBody)
		}
	} else {
		mode = "created_new"
		if err := c.github.CreateReview(ctx, prNumber, reviewBody, uniqueComments); err != nil {
			return nil, fmt.Errorf("create review: %w", err)
		}
		posted = len(uniqueComments)
	}

	return &PostResult{
		CommentsPosted:    posted,
		DuplicatesSkipped: skipped,
		PRLevelComments:   len(prLevelComments),
		Mode:              mode,
	}, nil
}

// Files lists changed files with stats.
func (c *Client) Files(ctx context.Context, base string) (*FilesResult, error) {
	return c.worktree.GetChangedFiles(ctx, base)
}

// Commits lists commits with messages.
func (c *Client) Commits(ctx context.Context, base string) (*CommitsResult, error) {
	return c.worktree.GetCommits(ctx, base)
}

// PRInfo gets full PR metadata.
func (c *Client) PRInfo(ctx context.Context, prNumber int) (*PRInfoResult, error) {
	return c.github.GetPRInfo(ctx, prNumber)
}

// Cleanup removes a worktree at the given path.
func (c *Client) Cleanup(ctx context.Context, path string) (*CleanupResult, error) {
	if err := c.worktree.RemoveWorktree(ctx, path); err != nil {
		return &CleanupResult{Status: "error"}, err
	}
	return &CleanupResult{Status: "ok"}, nil
}
