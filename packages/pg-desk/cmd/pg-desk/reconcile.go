package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
)

// reconcileCmd implements `pg-desk reconcile` (bead pg2-kftf9.2): an
// event-independent repair pass that re-drives closure for PRs that left the
// open set, and any recorded sync_error whose automatic retry is due (bead
// pg2-xb6fs). See pipeline.Reconcile.
// reconcileBudget bounds one run (bead pg2-a5z69). The default sits under
// pg-router's handler timeouts; 0 disables the bound.
var reconcileBudget time.Duration

// reconcileRetryAll lifts the sync_error automatic-retry policy for one
// manual run (bead pg2-xb6fs): every recorded sync_error is re-driven now,
// including exhausted, non-transient and still-backing-off rows.
var reconcileRetryAll bool

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Re-drive closure for PRs whose anchor is still open but which merged/closed, and any sync_error whose automatic retry is due",
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

		p := pipeline.New(cfg, st,
			pipeline.WithLogWriter(cmd.ErrOrStderr()),
			pipeline.WithReconcileBudget(reconcileBudget),
			pipeline.WithReconcileRetryAll(reconcileRetryAll))
		if err := p.Reconcile(cmd.Context()); err != nil {
			return fmt.Errorf("reconcile: %w", err)
		}
		return nil
	},
}

func init() {
	reconcileCmd.Flags().DurationVar(&reconcileBudget, "budget", 4*time.Minute,
		"stop starting new candidates after this much wall-clock time (0 = unbounded); a budget stop exits 0 and logs reconcile_budget_exhausted, the next run resumes with the oldest-checked candidates")
	reconcileCmd.Flags().BoolVar(&reconcileRetryAll, "retry-all", false,
		"re-drive every recorded sync_error now, ignoring the automatic-retry policy (backoff, the retry bound, and non-transient classification) — the manual repair once the cause is fixed")
	rootCmd.AddCommand(reconcileCmd)
}
