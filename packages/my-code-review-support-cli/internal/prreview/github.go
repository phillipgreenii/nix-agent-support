package prreview

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// GitHubClient handles GitHub API interactions via gh CLI.
type GitHubClient struct {
	executor CommandExecutor
	workDir  string
}

// NewGitHubClient creates a new GitHubClient.
func NewGitHubClient(workDir string, executor CommandExecutor) *GitHubClient {
	return &GitHubClient{
		executor: executor,
		workDir:  workDir,
	}
}

// IdentifyPR identifies a PR from various input formats.
// Accepts: PR number, PR URL, branch name, or title search.
func (c *GitHubClient) IdentifyPR(ctx context.Context, arg string) (*PRInfo, error) {
	if num := parsePRNumber(arg); num > 0 {
		return c.getPRByNumber(ctx, num)
	}

	if num := parsePRURL(arg); num > 0 {
		return c.getPRByNumber(ctx, num)
	}

	pr, err := c.getPRByBranch(ctx, arg)
	if err == nil && pr != nil {
		return pr, nil
	}

	pr, err = c.searchPRByTitle(ctx, arg)
	if err == nil && pr != nil {
		return pr, nil
	}

	return nil, fmt.Errorf("%w: %s", ErrPRNotFound, arg)
}

func (c *GitHubClient) getPRByNumber(ctx context.Context, num int) (*PRInfo, error) {
	stdout, stderr, err := c.executor.Execute(ctx, c.workDir,
		"gh", "pr", "view", strconv.Itoa(num),
		"--json", "number,title,headRefName,baseRefName,url,state,headRefOid",
	)
	if err != nil {
		return nil, fmt.Errorf("%w: PR #%d: %s", ErrPRNotFound, num, string(stderr))
	}

	var pr PRInfo
	if err := json.Unmarshal(stdout, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal PR info: %w", err)
	}

	return &pr, nil
}

func (c *GitHubClient) getPRByBranch(ctx context.Context, branch string) (*PRInfo, error) {
	stdout, stderr, err := c.executor.Execute(ctx, c.workDir,
		"gh", "pr", "list",
		"--head", branch,
		"--json", "number,title,headRefName,baseRefName,url,state,headRefOid",
		"--limit", "2",
	)
	if err != nil {
		return nil, fmt.Errorf("%w: list by branch: %s", ErrGitHubAPIFailed, string(stderr))
	}

	var prs []PRInfo
	if err := json.Unmarshal(stdout, &prs); err != nil {
		return nil, fmt.Errorf("unmarshal PR list: %w", err)
	}

	if len(prs) == 0 {
		return nil, nil
	}
	if len(prs) > 1 {
		return nil, fmt.Errorf("%w: branch %s matched %d PRs", ErrMultiplePRsMatch, branch, len(prs))
	}

	return &prs[0], nil
}

func (c *GitHubClient) searchPRByTitle(ctx context.Context, title string) (*PRInfo, error) {
	stdout, stderr, err := c.executor.Execute(ctx, c.workDir,
		"gh", "pr", "list",
		"--search", title,
		"--json", "number,title,headRefName,baseRefName,url,state,headRefOid",
		"--limit", "2",
	)
	if err != nil {
		return nil, fmt.Errorf("%w: search by title: %s", ErrGitHubAPIFailed, string(stderr))
	}

	var prs []PRInfo
	if err := json.Unmarshal(stdout, &prs); err != nil {
		return nil, fmt.Errorf("unmarshal PR list: %w", err)
	}

	if len(prs) == 0 {
		return nil, nil
	}
	if len(prs) > 1 {
		return nil, fmt.Errorf("%w: title %q matched %d PRs", ErrMultiplePRsMatch, title, len(prs))
	}

	return &prs[0], nil
}

// GetPendingReview returns the existing pending review for the current user, if any.
func (c *GitHubClient) GetPendingReview(ctx context.Context, prNumber int) (*ExistingReview, error) {
	stdout, _, err := c.executor.Execute(ctx, c.workDir,
		"gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews", prNumber),
		"--jq", `[.[] | select(.state == "PENDING")] | .[0] // empty`,
	)
	if err != nil {
		return nil, nil
	}

	output := strings.TrimSpace(string(stdout))
	if output == "" || output == "null" {
		return nil, nil
	}

	var review ExistingReview
	if err := json.Unmarshal(stdout, &review); err != nil {
		return nil, fmt.Errorf("unmarshal pending review: %w", err)
	}

	return &review, nil
}

// GetReviewComments returns existing comments from a pending review.
func (c *GitHubClient) GetReviewComments(ctx context.Context, prNumber, reviewID int) ([]ExistingComment, error) {
	stdout, _, err := c.executor.Execute(ctx, c.workDir,
		"gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews/%d/comments", prNumber, reviewID),
		"--jq", `[.[] | {path, line, start_line, body}]`,
	)
	if err != nil {
		return nil, nil
	}

	var comments []ExistingComment
	if err := json.Unmarshal(stdout, &comments); err != nil {
		return nil, fmt.Errorf("unmarshal review comments: %w", err)
	}

	return comments, nil
}

// CreateReview creates a new pending review with comments.
func (c *GitHubClient) CreateReview(ctx context.Context, prNumber int, body string, comments []GitHubComment) error {
	payload := map[string]interface{}{
		"comments": comments,
	}
	if body != "" {
		payload["body"] = body
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal review payload: %w", err)
	}

	_, stderr, err := c.executor.ExecuteWithStdin(ctx, c.workDir, payloadBytes,
		"gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews", prNumber),
		"-X", "POST",
		"-H", "Accept: application/vnd.github+json",
		"--input", "-",
	)
	if err != nil {
		return fmt.Errorf("%w: create review: %s", ErrGitHubAPIFailed, string(stderr))
	}

	return nil
}

// AddCommentToReview adds a single comment to an existing pending review.
func (c *GitHubClient) AddCommentToReview(ctx context.Context, prNumber, reviewID int, comment GitHubComment) error {
	args := []string{
		"api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews/%d/comments", prNumber, reviewID),
		"-X", "POST",
		"-H", "Accept: application/vnd.github+json",
		"-f", fmt.Sprintf("path=%s", comment.Path),
		"-f", fmt.Sprintf("body=%s", comment.Body),
	}

	if comment.SubjectType != "" {
		args = append(args, "-f", fmt.Sprintf("subject_type=%s", comment.SubjectType))
	} else {
		args = append(args, "-f", "side=RIGHT")
		if comment.Line != nil {
			args = append(args, "-f", fmt.Sprintf("line=%d", *comment.Line))
		}
		if comment.StartLine != nil {
			args = append(args, "-f", fmt.Sprintf("start_line=%d", *comment.StartLine))
		}
	}

	_, stderr, err := c.executor.Execute(ctx, c.workDir, "gh", args...)
	if err != nil {
		return fmt.Errorf("%w: add comment to %s: %s", ErrGitHubAPIFailed, comment.Path, string(stderr))
	}

	return nil
}

// UpdateReviewBody updates the body of an existing review.
func (c *GitHubClient) UpdateReviewBody(ctx context.Context, prNumber, reviewID int, body string) error {
	_, stderr, err := c.executor.Execute(ctx, c.workDir,
		"gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews/%d", prNumber, reviewID),
		"-X", "PUT",
		"-H", "Accept: application/vnd.github+json",
		"-f", fmt.Sprintf("body=%s", body),
	)
	if err != nil {
		return fmt.Errorf("%w: update review body: %s", ErrGitHubAPIFailed, string(stderr))
	}

	return nil
}

// GetPRInfo returns full PR metadata including description, labels, reviewers, and checks.
func (c *GitHubClient) GetPRInfo(ctx context.Context, prNumber int) (*PRInfoResult, error) {
	stdout, _, err := c.executor.Execute(ctx, c.workDir,
		"gh", "pr", "view", strconv.Itoa(prNumber),
		"--json", "body,labels,reviewRequests,statusCheckRollup",
	)
	if err != nil {
		return nil, fmt.Errorf("%w: get PR info for #%d", ErrGitHubAPIFailed, prNumber)
	}

	var prData struct {
		Body           string `json:"body"`
		Labels         []struct {
			Name string `json:"name"`
		} `json:"labels"`
		ReviewRequests []struct {
			Login string `json:"login"`
		} `json:"reviewRequests"`
		StatusCheckRollup []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"statusCheckRollup"`
	}

	if err := json.Unmarshal(stdout, &prData); err != nil {
		return nil, fmt.Errorf("unmarshal PR info: %w", err)
	}

	labels := make([]string, 0, len(prData.Labels))
	for _, label := range prData.Labels {
		labels = append(labels, label.Name)
	}

	reviewers := make([]string, 0, len(prData.ReviewRequests))
	for _, reviewer := range prData.ReviewRequests {
		reviewers = append(reviewers, reviewer.Login)
	}

	checks := make([]CheckInfo, 0, len(prData.StatusCheckRollup))
	for _, check := range prData.StatusCheckRollup {
		checks = append(checks, CheckInfo{
			Name:       check.Name,
			Status:     check.Status,
			Conclusion: check.Conclusion,
		})
	}

	return &PRInfoResult{
		Description: prData.Body,
		Labels:      labels,
		Reviewers:   reviewers,
		Checks:      checks,
	}, nil
}

// parsePRNumber parses a PR number from a string like "12345" or "#12345".
func parsePRNumber(s string) int {
	s = strings.TrimPrefix(s, "#")
	num, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return num
}

// parsePRURL extracts the PR number from a GitHub PR URL.
func parsePRURL(s string) int {
	re := regexp.MustCompile(`github\.com/.+/pull/(\d+)`)
	matches := re.FindStringSubmatch(s)
	if len(matches) < 2 {
		return 0
	}
	num, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0
	}
	return num
}
