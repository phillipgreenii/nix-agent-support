package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// runFlags holds parsed CLI flags for `pg-desk run`.
type runFlags struct {
	change  string
	verbose bool
}

var runF runFlags

// runConfigLoad and runStoreOpen are package-level vars — mirroring
// import_pg_pr_annotations.go's importPgPrDeskStoreOpen convention — so
// tests can inject a fixture config/store without touching the real
// $PG_DESK_CONFIG / $XDG_STATE_HOME/pg-desk/store.db.
var runConfigLoad = func(ctx context.Context) (*config.Config, error) { return config.Load(ctx) }

var runStoreOpen = func() (*store.Store, error) { return store.Open(store.DefaultPath()) }

// runCmd implements `pg-desk run <type> <id> --change
// added|changed|removed|sweep`: the three-stage gather/interpret/store
// pipeline for one entity (internal/pipeline), for <type>=pr only in this
// phase. run issue/run thread are explicitly stubbed here [Binding
// decisions]: an issue event's real behavior (re-running stage 2 only for
// every linked PR, design section 7.2) is Phase 10 for the beads backend
// and Phase 13 for Jira/threads — internal/pipeline is never invoked for
// those types.
var runCmd = &cobra.Command{
	Use:   "run <type> <id>",
	Short: "Run the gather/interpret/store pipeline once for one entity",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		entityType, entityID := args[0], args[1]
		switch entityType {
		case "issue":
			return fmt.Errorf("run issue: not implemented, see Phase 10")
		case "thread":
			return fmt.Errorf("run thread: not implemented, see Phase 13")
		case "pr":
			// implemented below
		default:
			return fmt.Errorf("run: unknown entity type %q (want pr, issue, or thread)", entityType)
		}

		cfg, err := runConfigLoad(cmd.Context())
		if err != nil {
			return fmt.Errorf("run: load config: %w", err)
		}
		st, err := runStoreOpen()
		if err != nil {
			return fmt.Errorf("run: open store: %w", err)
		}
		defer func() { _ = st.Close() }()

		p := pipeline.New(cfg, st, pipeline.WithVerbose(runF.verbose), pipeline.WithLogWriter(cmd.ErrOrStderr()))
		return p.Run(cmd.Context(), entityType, entityID, gather.ChangeKind(runF.change))
	},
}

func init() {
	// An absent --change means sweep [design 7.2].
	runCmd.Flags().StringVar(&runF.change, "change", "sweep", "Change kind: added|changed|removed|sweep")
	runCmd.Flags().BoolVar(&runF.verbose, "verbose", false, "Print the three-stage timeline")
	rootCmd.AddCommand(runCmd)
}
