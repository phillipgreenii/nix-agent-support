package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/agentregistry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/beadsbridge"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/event"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/output"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/replyposter"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/reviewsink"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/sync"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/beads"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/cicd/ghactions"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs/github"
	"github.com/spf13/cobra"
)

// Compile-time check: the github VCS provider must satisfy the reply-poster's
// Replier interface. The sync engine type-asserts e.deps.VCS["github"] to
// replyposter.Replier at runtime (see Engine.reconcileReplies); this guards the
// assertion at build time so a provider regression fails the build, not a tick.
var _ replyposter.Replier = (*github.Provider)(nil)

// Compile-time check: the github VCS provider must satisfy the team-PR sink's
// narrow review-write surface (pg2-4c5i.35). The default team sink type-asserts
// the configured provider to reviewsink.VCSReviewer at runtime; this guards the
// assertion at build time so a provider regression fails the build, not a tick.
var _ reviewsink.VCSReviewer = (*github.Provider)(nil)

// syncFlags holds the parsed CLI flags for `pg-pr sync`.
type syncFlags struct {
	jsonOutput  bool
	pr          int
	repo        string
	daemon      bool
	interval    string
	logJSON     bool
	metricsAddr string
}

var syFlags syncFlags

// loadConfigForCLI is overridable for tests; production uses config.Load.
var loadConfigForCLI = func(ctx context.Context) (*config.Config, error) {
	return config.Load(ctx)
}

// newSyncEngineForCLI builds the engine from loaded config. Production
// callers use this; tests can substitute by reassigning.
//
// Deps.Beads is intentionally left nil so the engine constructs a per-repo
// bd Client (via beads.NewClientForRepo(rcfg.Path)) for each repo it
// processes. That way every bd command lands in the correct monorepo's
// .beads/ workspace regardless of where the pg-pr binary was invoked from.
var newSyncEngineForCLI = func(cfg *config.Config) (*sync.Engine, error) {
	gha := ghactions.New()
	// githubPRResolver lives in ci.go; same package. The github-actions
	// provider needs it to map PR # → head branch when listing runs.
	gha.SetPRResolver(githubPRResolver{})
	return sync.New(sync.Deps{
		Cfg:  cfg,
		VCS:  map[string]sync.VCSProvider{"github": github.New()},
		CICD: map[string]sync.CICDProvider{ghactions.ProviderName: gha},
	})
}

// newBeadsBridgeHandler returns an event.Handler that routes each event to the
// correct per-repo beads workspace. The beads client model is per-repo (each
// monorepo's .beads/ workspace is separate), so this wrapper resolves the repo
// path from the config and constructs a beads.NewClientForRepo on each call.
// beads.NewClientForRepo is cheap (no I/O until a bd command is issued), so
// constructing it per-dispatch is safe.
//
// Events whose repo is not in the config are silently ignored (the repo may
// have been removed from config between enqueueing and flushing).
func newBeadsBridgeHandler(cfg *config.Config) event.Handler {
	// Build a repo-remote → path index once.
	repoPaths := make(map[string]string, len(cfg.Repos))
	for _, r := range cfg.Repos {
		if r.Path != "" {
			repoPaths[r.Remote] = r.Path
		}
	}
	return func(ctx context.Context, e store.Event) error {
		// Extract the repo identifier from the event payload. All current event
		// types carry a "repo" field at the top level.
		var head struct {
			Repo string `json:"repo"`
		}
		if err := json.Unmarshal(e.Payload, &head); err != nil || head.Repo == "" {
			return nil // no repo → cannot route; skip silently
		}
		path, ok := repoPaths[head.Repo]
		if !ok {
			return nil // repo not in config; skip silently
		}
		client := beads.NewClientForRepo(path)
		return beadsbridge.New(client).Handle(ctx, e)
	}
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
		ctx := cmd.Context()
		cfg, err := loadConfigForCLI(ctx)
		if err != nil {
			return err
		}
		engine, err := newSyncEngineForCLI(cfg)
		if err != nil {
			return err
		}

		// Open the SQLite event store and wire up the outbox dispatcher.
		// The store path is resolved by the engine (honors StateDir / XDG).
		// Close is deferred so both the one-shot and daemon paths clean up.
		eventStore, err := store.Open(engine.StoreFile())
		if err != nil {
			return fmt.Errorf("open event store: %w", err)
		}
		defer func() { _ = eventStore.Close() }()
		disp := event.New()
		disp.Register(newBeadsBridgeHandler(cfg))
		engine.SetStoreAndDispatch(eventStore, disp.Dispatch)

		if syFlags.daemon {
			interval, err := time.ParseDuration(syFlags.interval)
			if err != nil {
				return fmt.Errorf("invalid --interval: %w", err)
			}
			var base = sync.NewTextHandler()
			if syFlags.logJSON {
				base = sync.NewJSONHandler()
			}
			// Fan out to stderr (preserves ~/Library/Logs/pg-pr-sync.err)
			// and the OTLP bridge (→ otelcol → Loki). The bridge is a
			// no-op when no OTLP endpoint is configured.
			logger := slog.New(telemetry.Fanout(base, telemetry.NewSlogHandler()))
			// Construct the dashboard store + agent registry and wire them
			// onto the engine. The registry is required for snapshot
			// approval classification; a config with no agents yields an
			// empty registry that treats every approver as human.
			reg, err := agentregistry.New(cfg.Agents)
			if err != nil {
				return fmt.Errorf("agent registry: %w", err)
			}
			snapStore := snapshot.NewStore()
			engine.SetAgentRegistry(reg)
			// Wire the draft-review consumption hook (pg2-4c5i.36): delegate
			// LLM production to a spawned `claude -p`, route by ownership to
			// the mine/team sink stubs (.34/.35 fill them). A per-repo bd
			// multiplexer scans every configured workspace's `bd ready`.
			engine.SetReviewHook(sync.ReviewHookDeps{
				Beads:   newMultiRepoReviewBeads(cfg),
				Spawner: newClaudeSpawner(cfg),
			})
			return engine.Daemon(ctx, sync.DaemonOpts{
				Interval:    interval,
				Logger:      logger,
				MetricsAddr: syFlags.metricsAddr,
				Dashboard:   snapStore,
			})
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
	if s.RepliesPosted > 0 {
		_, _ = fmt.Fprintf(w, "  replies posted: %d\n", s.RepliesPosted)
	}
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
		"Run as a daemon: fingerprint-poll at --interval until SIGINT/SIGTERM")
	syncCmd.Flags().StringVar(&syFlags.interval, "interval", "60s",
		"Daemon fingerprint poll interval (effective only with --daemon)")
	syncCmd.Flags().BoolVar(&syFlags.logJSON, "log-json", false,
		"Emit structured JSON logs to stderr (effective only with --daemon)")
	syncCmd.Flags().StringVar(&syFlags.metricsAddr, "metrics-addr", sync.DefaultMetricsAddr,
		"Daemon Prometheus scrape address (effective only with --daemon; empty disables)")
	rootCmd.AddCommand(syncCmd)
}
