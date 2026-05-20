package main

import (
	"fmt"
	"io"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/branch"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/spf13/cobra"
)

// branchFlags holds flags shared by the branch subcommands.
type branchFlags struct {
	jsonOutput bool
}

var brFlags branchFlags

// ----------------------------------------------------------------------
// Cobra wiring
// ----------------------------------------------------------------------

var branchCmd = &cobra.Command{
	Use:   "branch",
	Short: "Inspect branch / PR context",
	Long: `Inspect the git branch and pull-request context of the current
working directory. Useful for agents that need to discover what PR (if any)
is being worked on.`,
}

var branchDetectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Detect the branch / PR context for the current directory",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
		info, err := branch.Detect(cmd.Context(), cwd, branch.Options{})
		if err != nil {
			return err
		}
		if output.Resolve(brFlags.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), info)
		}
		return renderBranchInfo(cmd.OutOrStdout(), info)
	},
}

// renderBranchInfo prints the human-readable view of BranchInfo: one
// `key: value` line per field. PRNumber prints as `null` when nil.
func renderBranchInfo(w io.Writer, b *api.BranchInfo) error {
	pr := "null"
	if b.PRNumber != nil {
		pr = fmt.Sprintf("%d", *b.PRNumber)
	}
	_, err := fmt.Fprintf(w,
		"repo: %s\nbranch: %s\nbase: %s\nworktree_root: %s\npr_id: %s\n",
		b.Repo, b.Branch, b.Base, b.WorktreeRoot, pr)
	return err
}

func init() {
	branchDetectCmd.Flags().BoolVar(&brFlags.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")

	branchCmd.AddCommand(branchDetectCmd)
	rootCmd.AddCommand(branchCmd)
}
