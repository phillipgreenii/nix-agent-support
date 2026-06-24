package main

import (
	"context"
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/spf13/cobra"
)

// feedbackCloser is the minimal interface the migrate-feedback command needs
// from a beads.Client. The seam lets tests inject a fake without requiring a
// real bd workspace.
type feedbackCloser interface {
	ListFeedbackBeadIDs(ctx context.Context) ([]string, error)
	CloseFeedback(ctx context.Context, id, reason string) error
}

// migrateBeadsClient constructs the beads client used by migrate-feedback.
// Tests replace this variable to inject a fake feedbackCloser.
var migrateBeadsClient = func(repo string) feedbackCloser {
	if repo != "" {
		return beads.NewClientForRepo(repo)
	}
	return beads.NewClient()
}

// migrateFlags holds parsed CLI flags for `pg-pr migrate-feedback`.
type migrateFlags struct {
	repo   string
	dryRun bool
}

var mgF migrateFlags

var migrateFeedbackCmd = &cobra.Command{
	Use:   "migrate-feedback",
	Short: "Close legacy feedback beads (one-shot migration to the store)",
	Long: `migrate-feedback is a one-shot migration command.

Before pg-pr stored feedback in its SQLite store (internal/store), it
created 'feedback' beads in bd for every upstream event. Those legacy
beads are now stale — the store is the source of truth and is
repopulated automatically on the next 'pg-pr sync' or daemon cycle.

migrate-feedback closes all open feedback beads (type=feedback) in the
bd workspace so they stop cluttering 'bd ready' and 'bd list'. It does
NOT backfill their data into the store; 'pg-pr sync' will re-ingest
current upstream feedback from GitHub on its next run.

Use --dry-run to preview which beads would be closed without actually
closing them.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		client := migrateBeadsClient(mgF.repo)

		ids, err := client.ListFeedbackBeadIDs(ctx)
		if err != nil {
			return fmt.Errorf("migrate-feedback: list: %w", err)
		}

		if len(ids) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "migrate-feedback: no legacy feedback beads found")
			return err
		}

		if mgF.dryRun {
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"migrate-feedback: dry-run — would close %d feedback bead(s):\n", len(ids))
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", id); err != nil {
					return err
				}
			}
			return nil
		}

		var closed, failed int
		for _, id := range ids {
			if err := client.CloseFeedback(ctx, id, "migrated-to-store"); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "migrate-feedback: close %s: %v\n", id, err)
				failed++
				continue
			}
			closed++
		}

		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"migrate-feedback: closed %d feedback bead(s)", closed)
		if err != nil {
			return err
		}
		if failed > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), " (%d failed — see stderr)", failed)
			if err != nil {
				return err
			}
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return err
		}
		if failed > 0 {
			return fmt.Errorf("migrate-feedback: %d bead(s) could not be closed", failed)
		}
		return nil
	},
}

func init() {
	migrateFeedbackCmd.Flags().StringVar(&mgF.repo, "repo", "",
		"Path to the monorepo whose .beads/ workspace to operate on (defaults to cwd)")
	migrateFeedbackCmd.Flags().BoolVar(&mgF.dryRun, "dry-run", false,
		"List feedback beads that would be closed without actually closing them")
	rootCmd.AddCommand(migrateFeedbackCmd)
}
