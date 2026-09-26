package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
)

// sweepF holds parsed CLI flags for `pg-desk sweep` — a private copy
// rather than reusing run.go's own runF, mirroring this codebase's
// established "each command owns its own flag struct" convention (see
// e.g. run.go's runF versus desk.go's shared deskConfigLoad/deskStoreOpen
// seams, which ARE shared: seams are shared, flag structs are not).
var sweepF struct {
	verbose bool
}

// sweepCmd implements `pg-desk sweep` (bead pg2-gznpe): the operator's
// bulk backfill command. Each tracked entity otherwise only gets
// recomputed via its own next webhook-triggered `pg-desk run pr <id>`
// (run.go) — a schema/enrichment change (e.g. a new Enrichment field)
// silently leaves every entity that has not since had a real event stale,
// with no way to force a full recompute short of enumerating and
// re-running each one by hand. `sweep` does that enumeration itself: list
// every entity currently in the store (internal/store's ListEntities) and
// re-run the gather/interpret/store/sync pipeline for each with
// --change=sweep (internal/pipeline's Sweep — see its own doc comment for
// the per-entity error-tolerance and meta.last_sweep contract).
var sweepCmd = &cobra.Command{
	Use:   "sweep",
	Short: "Recompute every entity currently in the store (gather/interpret/store/sync, --change=sweep)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := runConfigLoad(cmd.Context())
		if err != nil {
			return fmt.Errorf("sweep: load config: %w", err)
		}
		st, err := runStoreOpen()
		if err != nil {
			return fmt.Errorf("sweep: open store: %w", err)
		}
		defer func() { _ = st.Close() }()

		entities, err := st.ListEntities()
		if err != nil {
			return fmt.Errorf("sweep: list entities: %w", err)
		}

		p := pipeline.New(cfg, st, pipeline.WithVerbose(sweepF.verbose), pipeline.WithLogWriter(cmd.ErrOrStderr()))
		if err := p.Sweep(cmd.Context(), entities); err != nil {
			return fmt.Errorf("sweep: %w", err)
		}
		return nil
	},
}

func init() {
	sweepCmd.Flags().BoolVar(&sweepF.verbose, "verbose", false, "Print the three-stage timeline for each entity")
	rootCmd.AddCommand(sweepCmd)
}
