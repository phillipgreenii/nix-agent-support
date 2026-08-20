package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/worktree"
	"github.com/spf13/cobra"
)

// worktreeFlags holds flags shared by the worktree subcommands.
type worktreeFlags struct {
	worktreeRoot string
	jsonOutput   bool
	force        bool
}

var wtFlags worktreeFlags

// defaultWorktreeRoot resolves the worktree root using the following
// priority (highest first):
//
//  1. --worktree-root flag (handled by cobra, not here).
//  2. $PG_PR_WORKTREE_ROOT env var.
//  3. ~/Code/reviews.
//
// Real config-file resolution lands in a later phase; this default
// matches the Phase 1 contract documented in the spec.
func defaultWorktreeRoot() string {
	if v := os.Getenv("PG_PR_WORKTREE_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "Code", "reviews")
}

// resolveWorktreeRoot returns the root to use given the parsed flag value.
func resolveWorktreeRoot(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	root := defaultWorktreeRoot()
	if root == "" {
		return "", fmt.Errorf("could not determine worktree root: pass --worktree-root or set PG_PR_WORKTREE_ROOT")
	}
	return root, nil
}

// parsePR converts a CLI argument to a PR number. For Phase 1 we accept
// only a plain integer; URL/branch parsing is deferred.
func parsePR(arg string) (int, error) {
	s := strings.TrimPrefix(arg, "#")
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid PR identifier %q: expected a positive integer", arg)
	}
	return n, nil
}

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

var worktreeCmd = &cobra.Command{
	Use:   "worktree",
	Short: "Manage git worktrees for PRs",
	Long: `Manage local git worktrees for pull requests under review.

Each worktree lives at <worktree-root>/pr-<number> on a branch named
review/pr-<number>. The worktree root defaults to ~/Code/reviews and can
be overridden via --worktree-root or $PG_PR_WORKTREE_ROOT.`,
}

var worktreeAddCmd = &cobra.Command{
	Use:   "add <pr>",
	Short: "Create a worktree for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pr, err := parsePR(args[0])
		if err != nil {
			return err
		}
		root, err := resolveWorktreeRoot(wtFlags.worktreeRoot)
		if err != nil {
			return err
		}

		res, err := worktree.Add(cmd.Context(), pr, worktree.Options{
			WorktreeRoot: root,
		})
		if err != nil {
			return err
		}

		if output.Resolve(wtFlags.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		if res.AlreadyExists {
			_, err = fmt.Fprintf(w, "! Worktree already exists for PR #%d at %s\n", res.PRNumber, res.Path)
			return err
		}
		if _, err = fmt.Fprintf(w, "ok Created worktree for PR #%d at %s (branch %s)\n",
			res.PRNumber, res.Path, res.Branch); err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "    cd %s\n", res.Path)
		return err
	},
}

var worktreeRemoveCmd = &cobra.Command{
	Use:   "remove <pr>",
	Short: "Remove the worktree for a PR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pr, err := parsePR(args[0])
		if err != nil {
			return err
		}
		root, err := resolveWorktreeRoot(wtFlags.worktreeRoot)
		if err != nil {
			return err
		}

		res, err := worktree.Remove(cmd.Context(), pr, worktree.Options{
			WorktreeRoot: root,
			Force:        wtFlags.force,
		})
		if err != nil {
			return err
		}

		if output.Resolve(wtFlags.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), res)
		}
		w := cmd.OutOrStdout()
		switch {
		case res.Removed:
			if _, err = fmt.Fprintf(w, "ok Removed worktree for PR #%d (%s)\n", res.PRNumber, res.Path); err != nil {
				return err
			}
			if res.Warning != "" {
				if _, err = fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: %s\n", res.Warning); err != nil {
					return err
				}
			}
		case res.Skipped:
			if _, err = fmt.Fprintf(w, "! Skipped PR #%d: %s\n", res.PRNumber, res.SkipReason); err != nil {
				return err
			}
			// Non-zero exit when skipped due to dirty worktree, so callers can
			// detect it. "no worktree found" is a success-ish no-op.
			if !strings.Contains(res.SkipReason, "no worktree found") {
				os.Exit(2)
			}
		}
		return nil
	},
}

var worktreeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List current PR worktrees",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := resolveWorktreeRoot(wtFlags.worktreeRoot)
		if err != nil {
			return err
		}

		entries, err := worktree.List(cmd.Context(), worktree.Options{
			WorktreeRoot: root,
		})
		if err != nil {
			return err
		}

		if output.Resolve(wtFlags.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), entries)
		}
		return renderWorktreeTable(cmd.OutOrStdout(), root, entries)
	},
}

// writeJSON serializes v as indented JSON followed by a newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// renderWorktreeTable prints the human-readable view of `list`.
func renderWorktreeTable(w io.Writer, root string, entries []worktree.Worktree) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintf(w, "No PR worktrees under %s\n", root)
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "PR\tBRANCH\tSTATUS\tPATH"); err != nil {
		return err
	}
	for _, e := range entries {
		status := "clean"
		var parts []string
		if e.HasUncommittedChange {
			parts = append(parts, "uncommitted")
		}
		if e.UnpushedCommits > 0 {
			parts = append(parts, fmt.Sprintf("+%d commits", e.UnpushedCommits))
		}
		if len(parts) > 0 {
			status = strings.Join(parts, ", ")
		}
		if _, err := fmt.Fprintf(tw, "#%d\t%s\t%s\t%s\n", e.PRNumber, e.Branch, status, e.Path); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func init() {
	for _, c := range []*cobra.Command{worktreeAddCmd, worktreeRemoveCmd, worktreeListCmd} {
		c.PersistentFlags().StringVar(&wtFlags.worktreeRoot, "worktree-root", "",
			"Worktree root directory (overrides $PG_PR_WORKTREE_ROOT and the default)")
		c.PersistentFlags().BoolVar(&wtFlags.jsonOutput, "json", false,
			"Emit machine-readable JSON instead of human-readable output")
	}
	worktreeRemoveCmd.Flags().BoolVarP(&wtFlags.force, "force", "f", false,
		"Force removal even with uncommitted changes")

	worktreeCmd.AddCommand(worktreeAddCmd, worktreeRemoveCmd, worktreeListCmd)
	rootCmd.AddCommand(worktreeCmd)
}
