package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
)

// reconcileCmd implements `pg-desk reconcile` (bead pg2-kftf9.2): an
// event-independent repair pass that re-drives closure (and any recorded
// sync_error) for PRs that left the open set. See pipeline.Reconcile.
var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Re-drive closure for PRs whose anchor is still open but which merged/closed, and any recorded sync_error",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := runConfigLoad(cmd.Context())
		if err != nil {
			return fmt.Errorf("reconcile: load config: %w", err)
		}
		st, err := runStoreOpen()
		if err != nil {
			return fmt.Errorf("reconcile: open store: %w", err)
		}
		defer func() { _ = st.Close() }()

		p := pipeline.New(cfg, st, pipeline.WithLogWriter(cmd.ErrOrStderr()))
		if err := p.Reconcile(cmd.Context()); err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(reconcileCmd)
}
