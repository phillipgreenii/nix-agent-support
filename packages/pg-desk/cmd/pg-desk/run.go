package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/beadref"
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

// runResolveBeadPR is the "issue" case's own injectable seam — mirroring
// runConfigLoad/runStoreOpen's identical convention — so tests can inject
// a fixture bead-to-PR resolution without a real pg-connector subprocess
// on $PATH. Production wraps beadref.Resolver.ResolvePR (Phase 10, docket
// pg2-2j5ac.34): given a beads-tracker issue-type entity's id, resolve the
// (repo, entityID) of the PR it is about, by the SAME title/metadata keys
// the sync stage's forward direction uses [design: 7.2, 7.5].
var runResolveBeadPR = func(ctx context.Context, cfg *config.Config, beadID string) (repo, entityID string, err error) {
	return beadref.NewResolver(cfg).ResolvePR(ctx, beadID)
}

// runCmd implements `pg-desk run <type> <id> --change
// added|changed|removed|sweep`: the three-stage gather/interpret/store
// pipeline for one entity (internal/pipeline), for <type>=pr; and, for
// <type>=issue (Phase 10, the beads backend), a bead-to-PR resolution
// (internal/beadref) followed by an interpret-ONLY re-run
// (pipeline.Pipeline.RunInterpretOnly) for the resolved PR — never a
// second gather, and never the sync stage [design: 7.2, 6.1; Binding
// decisions]. run thread remains stubbed [Binding decisions]: a
// thread-triggered re-run (design section 7.2) is Phase 13 for
// Jira/threads — internal/pipeline is never invoked for that type.
var runCmd = &cobra.Command{
	Use:   "run <type> <id>",
	Short: "Run the gather/interpret/store pipeline once for one entity",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		entityType, entityID := args[0], args[1]
		switch entityType {
		case "issue", "pr":
			// implemented below
		case "thread":
			return fmt.Errorf("run thread: not implemented, see Phase 13")
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

		if entityType == "issue" {
			_, prEntityID, err := runResolveBeadPR(cmd.Context(), cfg, entityID)
			if err != nil {
				return fmt.Errorf("run issue: resolve bead %s to PR: %w", entityID, err)
			}
			return p.RunInterpretOnly(cmd.Context(), entityTypePR, prEntityID, gather.ChangeKind(runF.change))
		}

		return p.Run(cmd.Context(), entityType, entityID, gather.ChangeKind(runF.change))
	},
}

func init() {
	// An absent --change means sweep [design 7.2].
	runCmd.Flags().StringVar(&runF.change, "change", "sweep", "Change kind: added|changed|removed|sweep")
	runCmd.Flags().BoolVar(&runF.verbose, "verbose", false, "Print the three-stage timeline")
	rootCmd.AddCommand(runCmd)
}
