package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sync"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
	"github.com/spf13/cobra"
)

// syncFlags holds the parsed CLI flags for `pg-pr sync`.
type syncFlags struct {
	jsonOutput bool
	pr         int
	repo       string
	daemon     bool
	interval   string
}

var syFlags syncFlags

// loadConfigForCLI is overridable for tests; production uses config.Load.
var loadConfigForCLI = func(ctx context.Context) (*config.Config, error) {
	return config.Load(ctx)
}

// newSyncEngineForCLI builds the engine from loaded config. Production
// callers use this; tests can substitute by reassigning.
var newSyncEngineForCLI = func(cfg *config.Config) (*sync.Engine, error) {
	return sync.New(sync.Deps{
		Cfg:   cfg,
		VCS:   map[string]sync.VCSProvider{"github": github.New()},
		Beads: beads.NewClient(),
	})
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync PR state from upstream into bd merge-request beads",
	Long: `Sync enumerates PRs of interest (configured self + team members)
across all configured repos and updates the corresponding merge-request
beads in bd. Re-running is idempotent. Beads whose upstream PR is no
longer in the watched set are closed automatically.

With --pr <number>, sync refreshes a single PR. --repo owner/name is
required when --pr is passed and the working directory is outside a
configured repo.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if syFlags.daemon {
			return errors.New("sync --daemon: lands in Phase 3 (the spec earmarks this for the daemon work-stream)")
		}
		ctx := cmd.Context()
		cfg, err := loadConfigForCLI(ctx)
		if err != nil {
			return err
		}
		engine, err := newSyncEngineForCLI(cfg)
		if err != nil {
			return err
		}

		var summary *sync.Summary
		if syFlags.pr > 0 {
			if syFlags.repo == "" {
				return errors.New("sync --pr requires --repo owner/name")
			}
			summary, err = engine.SyncPR(ctx, syFlags.repo, syFlags.pr)
		} else {
			summary, err = engine.Sync(ctx)
		}

		// Even on err, render the summary so callers see partial progress.
		if output.Resolve(syFlags.jsonOutput) {
			_ = writeJSON(cmd.OutOrStdout(), summary)
		} else {
			renderSyncSummary(cmd.OutOrStdout(), summary)
		}
		return err
	},
}

// renderSyncSummary prints the human-readable view of a sync.Summary.
// Write errors are non-fatal — the caller may have closed the writer.
func renderSyncSummary(w io.Writer, s *sync.Summary) {
	if s == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "Sync completed: %d PR(s) observed across %d repo(s)\n",
		s.TotalPRs, len(s.Repos))
	_, _ = fmt.Fprintf(w, "  created: %d  updated: %d  closed: %d\n",
		s.BeadsCreated, s.BeadsUpdated, s.BeadsClosed)
	for _, r := range s.Repos {
		if r.Error != "" {
			_, _ = fmt.Fprintf(w, "  ! %s: %s\n", r.Repo, r.Error)
		} else {
			_, _ = fmt.Fprintf(w, "  ok %s (%d PR%s)\n", r.Repo, r.PRs, plural(r.PRs))
		}
	}
	if len(s.Errors) > 0 {
		_, _ = fmt.Fprintln(w, "Errors:")
		for _, e := range s.Errors {
			_, _ = fmt.Fprintf(w, "  ! [%s] %s\n", e.Repo, e.Message)
		}
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func init() {
	syncCmd.Flags().BoolVar(&syFlags.jsonOutput, "json", false,
		"Emit machine-readable JSON instead of human-readable output")
	syncCmd.Flags().IntVar(&syFlags.pr, "pr", 0,
		"Sync a single PR by number (requires --repo)")
	syncCmd.Flags().StringVar(&syFlags.repo, "repo", "",
		"Repository in owner/name form (required with --pr)")
	syncCmd.Flags().BoolVar(&syFlags.daemon, "daemon", false,
		"Run as a daemon (lands in Phase 3)")
	syncCmd.Flags().StringVar(&syFlags.interval, "interval", "10m",
		"Daemon poll interval (effective only with --daemon)")
	rootCmd.AddCommand(syncCmd)
}
