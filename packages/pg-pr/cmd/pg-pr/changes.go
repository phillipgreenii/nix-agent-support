package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
)

// changesFlags holds the parsed CLI flags for `pg-pr changes`.
type changesFlags struct {
	since      string
	jsonOutput bool
	store      string
}

var chFlags changesFlags

// newChangesRunner returns a Runner targeting bd's auto-discovered workspace
// (cwd-rooted). Tests override this; production fans out per-repo via
// newChangesRunnerForRepo below.
var newChangesRunner = func() beads.Runner { return beads.NewCLIRunner() }

// newChangesRunnerForRepo returns a Runner whose Dir is the given monorepo
// path so bd discovers that monorepo's `.beads/` workspace. Exposed as a
// var for tests.
var newChangesRunnerForRepo = func(dir string) beads.Runner {
	return beads.NewCLIRunnerForRepo(dir)
}

var changesCmd = &cobra.Command{
	Use:   "changes",
	Short: "List pg-pr-managed bead changes since a timestamp",
	Long: `Reports merge-request / feedback / action beads that were created,
updated, or closed since the given timestamp, plus any per-repo sync errors
recorded in the local pg-pr SQLite store.

Used by integrations (the pg-pr skill and other polling agents) to drive
incremental updates without re-scanning the whole bd workspace.

When pg-pr is configured with multiple repos (potentially across multiple
monorepos with distinct bd workspaces), this command fans out one bd query
per repo and merges the results. If no config is loadable, it falls back
to a single cwd-rooted bd query.

Example:

  pg-pr changes --since 2026-05-20T00:00:00Z
  pg-pr changes --since 2026-05-20T00:00:00Z --json`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if chFlags.since == "" {
			return errors.New("changes: --since <RFC3339 timestamp> is required")
		}
		since, err := time.Parse(time.RFC3339, chFlags.since)
		if err != nil {
			return fmt.Errorf("changes: --since %q is not RFC3339: %w", chFlags.since, err)
		}

		// The sync engine's store is process-global (one store.db, not one
		// per bd workspace), so this is read exactly once regardless of how
		// many repos are fanned out below.
		repoErrors, err := changes.LoadRepoErrors(cmd.Context(), chFlags.store)
		if err != nil {
			return err
		}

		// Try to load config so we can fan out per repo. A missing config
		// is not fatal — fall back to the single-runner behavior so
		// ad-hoc invocations from inside a repo still work.
		cfg, _ := loadConfigForCLI(cmd.Context())
		var paths []string
		if cfg != nil {
			seen := map[string]bool{}
			for _, r := range cfg.Repos {
				if r.Path == "" || seen[r.Path] {
					continue
				}
				seen[r.Path] = true
				paths = append(paths, r.Path)
			}
		}

		var merged *changes.ChangeSet
		if len(paths) == 0 {
			runner := newChangesRunner()
			merged, err = changes.Since(cmd.Context(), since, runner)
			if err != nil {
				return err
			}
		} else {
			merged = &changes.ChangeSet{Since: since.UTC()}
			for _, p := range paths {
				runner := newChangesRunnerForRepo(p)
				cs, qerr := changes.Since(cmd.Context(), since, runner)
				if qerr != nil {
					return qerr
				}
				if cs == nil {
					continue
				}
				merged.Created = append(merged.Created, cs.Created...)
				merged.Updated = append(merged.Updated, cs.Updated...)
				merged.Closed = append(merged.Closed, cs.Closed...)
			}
		}
		merged.Errors = repoErrors

		if output.Resolve(chFlags.jsonOutput) {
			return writeJSON(cmd.OutOrStdout(), merged)
		}
		return renderChanges(cmd.OutOrStdout(), merged)
	},
}

// renderChanges prints the human-readable view of a ChangeSet.
func renderChanges(w io.Writer, cs *changes.ChangeSet) error {
	if cs == nil {
		return nil
	}
	total := len(cs.Created) + len(cs.Updated) + len(cs.Closed)
	if _, err := fmt.Fprintf(w, "Changes since %s: %d bead(s)\n",
		cs.Since.Format(time.RFC3339), total); err != nil {
		return err
	}
	if total > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(tw, "STATE\tTYPE\tID\tTITLE"); err != nil {
			return err
		}
		for _, b := range cs.Created {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", "created", b.Type, b.ID, b.Title)
		}
		for _, b := range cs.Updated {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", "updated", b.Type, b.ID, b.Title)
		}
		for _, b := range cs.Closed {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", "closed", b.Type, b.ID, b.Title)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if len(cs.Errors) > 0 {
		if _, err := fmt.Fprintln(w, "Sync errors:"); err != nil {
			return err
		}
		for _, e := range cs.Errors {
			_, _ = fmt.Fprintf(w, "  ! [%s] %s\n", e.Repo, e.Message)
		}
	}
	return nil
}

func init() {
	changesCmd.Flags().StringVar(&chFlags.since, "since", "",
		"RFC3339 timestamp: report changes at or after this moment (required)")
	changesCmd.Flags().BoolVar(&chFlags.jsonOutput, "json", false,
		"Emit machine-readable JSON (PGPR_OUTPUT=json env also honored)")

	// --store / PG_PR_STORE env override, matching `pg-pr feedback`'s
	// convention (cmd/pg-pr/feedback.go) — resolved at flag-parse time.
	defaultStore := os.Getenv("PG_PR_STORE")
	if defaultStore == "" {
		defaultStore = store.DefaultPath()
	}
	changesCmd.Flags().StringVar(&chFlags.store, "store", defaultStore,
		"Path to the pg-pr SQLite store (env: PG_PR_STORE)")

	rootCmd.AddCommand(changesCmd)
}
