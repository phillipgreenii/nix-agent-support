package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/phillipgreenii/my-code-review-support-cli/internal/prreview"
	"github.com/spf13/cobra"
)

var (
	Version = "dev"
	workDir string
	baseRef string
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "my-code-review-support-cli",
	Short: "Code review support utilities for AI agents",
	Long: `my-code-review-support-cli provides utilities for AI agents to perform
automated PR reviews on GitHub.

Commands:
  setup    - Identify PR, fetch branch, create worktree
  files    - List changed files with stats
  commits  - List commits with messages
  pr-info  - Get full PR metadata
  post     - Post review comments (reads from stdin)
  cleanup  - Remove worktree`,
	Version: Version,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("my-code-review-support-cli %s\n", Version)
	},
}

var setupCmd = &cobra.Command{
	Use:   "setup <PR_URL|PR_NUMBER|BRANCH_NAME>",
	Short: "Identify PR, fetch branch, verify commit, create worktree",
	Long: `Setup identifies a PR from various input formats, fetches the branch,
verifies the commit SHA matches the PR, and creates a temporary worktree.

Input formats:
  - PR number: 12345 or #12345
  - PR URL: https://github.com/org/repo/pull/12345
  - Branch name: user.TICKET.feature
  - Title search: any text to search PR titles

Output: JSON with PR metadata and worktree path`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := prreview.NewClient(workDir)
		result, err := client.Setup(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		return outputJSON(result)
	},
}

var postCmd = &cobra.Command{
	Use:   "post <PR_NUMBER>",
	Short: "Post review comments from stdin",
	Long: `Post a pending code review to a GitHub PR.

Reads a JSON review payload from stdin, deduplicates comments, and creates
a pending review on the specified PR. Never auto-submits — always pending.

A robot emoji (🤖) is appended to each comment automatically; do not include
it in your input.

Input Format (JSON on stdin):
  {
    "comments": [
      {
        "path": "src/file.ts",        // File path relative to repo root
                                       // Use null for PR-level comments
        "lines": [42],                 // Line number(s) in the file
                                       // Use null for file-level or PR-level comments
                                       // Single line: [42], range: [10, 20]
        "message": "Description...",   // The review comment text (required)
        "severity": "warning"          // One of: "error", "warning", "suggestion"
      }
    ]
  }

Comment Types:
  PR-level:   path=null, lines=null    → appears as a top-level PR comment
  File-level: path="src/foo.ts", lines=null → appears on the file
  Line-level: path="src/foo.ts", lines=[42] → appears on line 42
  Range:      path="src/foo.ts", lines=[10,20] → appears on lines 10-20

Severity Levels:
  error       → Must be fixed before merge
  warning     → Should be addressed, but not blocking
  suggestion  → Optional improvement

Examples:
  # Post a simple review
  echo '{"comments":[{"path":"main.go","lines":[10],"message":"Bug here","severity":"error"}]}' | my-code-review-support-cli post 12345

  # Post from a file
  cat review.json | my-code-review-support-cli post 12345

  # PR-level comment only
  echo '{"comments":[{"path":null,"lines":null,"message":"Add tests","severity":"suggestion"}]}' | my-code-review-support-cli post 12345`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var prNumber int
		if _, err := fmt.Sscanf(args[0], "%d", &prNumber); err != nil || prNumber == 0 {
			return fmt.Errorf("invalid PR number: %s", args[0])
		}

		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}

		if len(input) == 0 {
			return fmt.Errorf("no input received on stdin")
		}

		client := prreview.NewClient(workDir)
		result, err := client.Post(cmd.Context(), prNumber, string(input))
		if err != nil {
			return err
		}
		return outputJSON(result)
	},
}

var filesCmd = &cobra.Command{
	Use:   "files",
	Short: "List changed files with stats",
	Long: `Files lists all changed files with addition/deletion stats.

The --base flag specifies the base reference for comparison (default: origin/main).
This command should be run in the worktree created by setup.

Output: JSON with file paths and stats`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := prreview.NewClient(workDir)
		result, err := client.Files(cmd.Context(), baseRef)
		if err != nil {
			return err
		}
		return outputJSON(result)
	},
}

var commitsCmd = &cobra.Command{
	Use:   "commits",
	Short: "List commits with messages",
	Long: `Commits lists all commits with their SHA, subject, body, and author.

The --base flag specifies the base reference for comparison (default: origin/main).
This command should be run in the worktree created by setup.

Output: JSON with commit information`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := prreview.NewClient(workDir)
		result, err := client.Commits(cmd.Context(), baseRef)
		if err != nil {
			return err
		}
		return outputJSON(result)
	},
}

var prInfoCmd = &cobra.Command{
	Use:   "pr-info <PR_NUMBER>",
	Short: "Get full PR metadata",
	Long: `PR-info retrieves full PR metadata including description, labels, reviewers, and checks.

Output: JSON with PR metadata`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var prNumber int
		if _, err := fmt.Sscanf(args[0], "%d", &prNumber); err != nil || prNumber == 0 {
			return fmt.Errorf("invalid PR number: %s", args[0])
		}

		client := prreview.NewClient(workDir)
		result, err := client.PRInfo(cmd.Context(), prNumber)
		if err != nil {
			return err
		}
		return outputJSON(result)
	},
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup <WORKTREE_PATH>",
	Short: "Remove a worktree",
	Long: `Cleanup removes a worktree at the given path.

Always run this after a review, even if previous steps failed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := prreview.NewClient(workDir)
		result, err := client.Cleanup(cmd.Context(), args[0])
		if err != nil {
			_ = outputJSON(result)
			return err
		}
		return outputJSON(result)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&workDir, "workdir", ".", "Working directory (repo root)")

	filesCmd.Flags().StringVar(&baseRef, "base", "origin/main", "Base reference for comparison")
	commitsCmd.Flags().StringVar(&baseRef, "base", "origin/main", "Base reference for comparison")

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(filesCmd)
	rootCmd.AddCommand(commitsCmd)
	rootCmd.AddCommand(prInfoCmd)
	rootCmd.AddCommand(postCmd)
	rootCmd.AddCommand(cleanupCmd)
}

func outputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
