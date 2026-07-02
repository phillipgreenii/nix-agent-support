package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
)

// migrateStoreOpen is the store-open function used by the migrate command.
// Tests replace this variable to inject a store backed by a temp file.
var migrateStoreOpen = store.Open

// migrateCmdFlags holds parsed CLI flags for `pg-pr migrate`.
type migrateCmdFlags struct {
	dbPath string
	dryRun bool
}

var mcF migrateCmdFlags

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply pending data migrations to the local store",
	Long: `migrate brings the pg-pr local store up to the current data version.

It is a first-class, versioned command that tracks which one-shot data
migrations have been applied and runs only those that are still pending.
Running it against an already-current store is a no-op.

Data migrations are separate from schema migrations (which run automatically
on Open). Data migrations perform destructive data transformations — dedup,
backfill, etc. — that must run explicitly.

First migration (0001): dedup legacy PRRC-keyed code-comment-thread feedback
rows whose thread now exists under a PRRT-keyed row (created by the GraphQL
path after pg2-re7e), and backfill code_comment_message.posted_at for rows
that predate the GraphQL createdAt fix.

Use --dry-run to see which migrations are pending without applying them.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()

		dbPath := mcF.dbPath
		if dbPath == "" {
			dbPath = store.DefaultPath()
		}

		db, err := migrateStoreOpen(dbPath)
		if err != nil {
			return fmt.Errorf("migrate: open store: %w", err)
		}
		defer func() { _ = db.Close() }()

		steps := store.RegisteredDataMigrations

		if mcF.dryRun {
			pending, err := store.PendingDataMigrations(ctx, db, steps)
			if err != nil {
				return fmt.Errorf("migrate: check pending: %w", err)
			}
			if len(pending) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "migrate: dry-run — store is up-to-date, nothing to apply")
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"migrate: dry-run — %d migration(s) pending:\n", len(pending))
			if err != nil {
				return err
			}
			for _, s := range pending {
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", s.ID); err != nil {
					return err
				}
			}
			return nil
		}

		pending, err := store.PendingDataMigrations(ctx, db, steps)
		if err != nil {
			return fmt.Errorf("migrate: check pending: %w", err)
		}
		if len(pending) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "migrate: store is up-to-date (0 migrations applied)")
			return err
		}

		if err := store.RunDataMigrations(ctx, db, pending); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		_, err = fmt.Fprintf(cmd.OutOrStdout(),
			"migrate: ok — applied %d migration(s)\n", len(pending))
		return err
	},
}

func init() {
	migrateCmd.Flags().StringVar(&mcF.dbPath, "db", "",
		"Path to the store.db file (defaults to the XDG state-home path)")
	migrateCmd.Flags().BoolVar(&mcF.dryRun, "dry-run", false,
		"List pending migrations without applying them")
	rootCmd.AddCommand(migrateCmd)
}
