package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/beadref"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/ticketkey"
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
// <type>=issue, a resolve-then-interpret-ONLY re-run
// (pipeline.Pipeline.RunInterpretOnly) — never a second gather, and never
// the sync stage [design: 7.2, 6.1; Binding decisions] — split two ways by
// the incoming id's own SHAPE (checked first, via ticketkey.MatchesShape
// against config.Config.TicketPatterns; never try one resolution and fall
// back to the other [Binding decisions]):
//
//   - A beads id (Phase 10, the beads backend): resolved to its linked PR
//     via internal/beadref (bead-to-PR resolution).
//   - A Jira ticket key (Phase 13, docket pg2-2j5ac.40): every PR already
//     cross-referenced to it (internal/store's xref table, populated by
//     internal/gather's own ticket-key scan) is re-interpreted — see
//     runJiraIssue below. An id matching NEITHER shape is this dispatch's
//     own well-formed error, never a silent no-op [Binding decisions].
//
// run thread remains stubbed [Binding decisions]: a thread-triggered
// re-run (design section 7.2) is Phase 13's own Slack-half sibling packet
// — internal/pipeline is never invoked for that type.
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
		change := gather.ChangeKind(runF.change)

		if entityType == "issue" {
			if ticketkey.MatchesShape(entityID, cfg.TicketPatterns) {
				return runJiraIssue(cmd.Context(), p, cfg, st, entityID, change)
			}
			_, prEntityID, err := runResolveBeadPR(cmd.Context(), cfg, entityID)
			if err != nil {
				return fmt.Errorf("run issue: resolve bead %s to PR: %w", entityID, err)
			}
			return p.RunInterpretOnly(cmd.Context(), entityTypePR, prEntityID, change)
		}

		return p.Run(cmd.Context(), entityType, entityID, change)
	},
}

// runJiraIssue implements run issue's Jira half (docket pg2-2j5ac.40,
// Phase 13, [design: 7.2, 8]): resolve every PR xref'd to ticketKey via the
// store's own reverse xref lookup (Store.ListXrefsByTo, to_type="issue"),
// and re-run interpret-only for each — never gather, never sync, never a
// bead write [Binding decisions: "added/changed/removed/sweep all resolve
// the SAME triggering bead's linked PR the same way ... re-run interpret
// ONLY"]. A ticket key with no currently xref'd PR is a no-op (exit 0):
// there is nothing to re-interpret yet — mirrors pipeline.Run's own "an id
// the store does not know is a no-op" convention for --change removed. If
// re-interpreting more than one linked PR, every one is attempted (a
// failure on one does not skip the rest); any failures are joined into a
// single returned error.
func runJiraIssue(ctx context.Context, p *pipeline.Pipeline, cfg *config.Config, st *store.Store, ticketKey string, change gather.ChangeKind) error {
	repo := runRepo(cfg)
	xrefs, err := st.ListXrefsByTo(repo, "issue", ticketKey)
	if err != nil {
		return fmt.Errorf("run issue %s: list xrefs: %w", ticketKey, err)
	}

	var errs []error
	for _, x := range xrefs {
		if runErr := p.RunInterpretOnly(ctx, entityTypePR, x.FromID, change); runErr != nil {
			errs = append(errs, fmt.Errorf("re-interpret %s: %w", x.FromID, runErr))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("run issue %s: %w", ticketKey, errors.Join(errs...))
	}
	return nil
}

// runRepo returns the single Phase-9 configured repository's remote, or ""
// if none is configured — mirrors internal/pipeline's own identical
// p.repo() helper and internal/gather's own identical g.repo() helper
// (each package keeps its own private copy rather than a shared one, per
// this codebase's established precedent), so runJiraIssue's own
// ListXrefsByTo call is keyed by the SAME repo value the entity/
// interpretation/xref tables use.
func runRepo(cfg *config.Config) string {
	if cfg == nil || len(cfg.Repos) == 0 {
		return ""
	}
	return cfg.Repos[0].Remote
}

func init() {
	// An absent --change means sweep [design 7.2].
	runCmd.Flags().StringVar(&runF.change, "change", "sweep", "Change kind: added|changed|removed|sweep")
	runCmd.Flags().BoolVar(&runF.verbose, "verbose", false, "Print the three-stage timeline")
	rootCmd.AddCommand(runCmd)
}
