package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/pipeline"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// refreshChangeKind is the gather.ChangeKind a targeted refresh passes to
// RunEntityChange. It MUST NOT be ChangeRemoved (that would turn a genuinely
// not-found entity into a quiet NotFound result instead of the loud failure
// D-G8 requires) and SHOULD NOT be ChangeSweep (a sweep skips re-fetching an
// unchanged PR head, which would defeat an explicit refresh). "changed" is
// chosen over "added": a refresh targets an entity the operator already
// knows, not a newly watched one. See docs/behavior/pg-desk/refresh.md.
const refreshChangeKind = gather.ChangeChanged

// refreshOrigin labels the change_log row an operator-invoked refresh appends.
const refreshOrigin = "pg-desk"

// newRefreshHydrator builds the hydrator refreshEntity uses; tests replace it
// to observe the call.
var newRefreshHydrator = func(cfg *config.Config, st *store.Store) changes.Hydrator {
	return pipeline.New(cfg, st)
}

func init() {
	for _, t := range typedEntityTypes {
		typeGroup(t).AddCommand(newRefreshCmd(t))
	}
}

// newRefreshCmd builds `pg-desk <type> refresh <id>`: hydrate exactly one
// entity through RunEntityChange [design 6.9, 9.12]. It is a typed verb with
// no old-schema counterpart, so it refuses an old-schema store.
func newRefreshCmd(entityType string) *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "refresh <id>",
		Short: fmt.Sprintf("Re-hydrate one %s from pg-connector", entityType),
		Long: fmt.Sprintf(`Hydrate exactly one %[1]s through the entity-change pipeline: read it from
pg-connector, classify the change against the previous snapshot, and log it.

For an issue the read is pinned to its backend (the stored backend column, else
the configured watched queries, else the single owning backend for an id that
matches ticket_patterns); an id served by queries of two different backends is
refused with exit 1 before any read. --watch (issue only) also asks that
backend to watch the one entity and marks it watched in the same write, so its
first row is delivered (a backend that keeps no store, such as beads, is not
asked: the hydration write alone marks the entity watched and appends one
added row); without it an unwatched issue is hydrated and appends no row.

Exit codes: 0 hydrated; 2 hydration degraded (the previous snapshot is kept
and the entity stays due); 3 hydration failed entirely (previous snapshot
kept, entity stays due), including an entity that is not found.

On exit 2 and 3 a line "class=<class>" is printed on standard output, so a
caller can tell the failures apart without reading the message text. The class
is one of: not_found (the source says the entity is absent), transient (the
source or the connector failed in a way a later attempt may clear),
unauthenticated (the source refused the credentials), degraded (exit 2: part
of the read failed) or hydrate_error (any other failure).`, entityType),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRefresh(cmd, entityType, args[0], watch)
		},
	}
	if entityType == entityTypeIssue {
		cmd.Flags().BoolVar(&watch, "watch", false, "also ask the issue's backend to watch this one entity (a backend that keeps no store is not asked) and mark it watched in the same write, so its first row is delivered")
	}
	return cmd
}

func runRefresh(cmd *cobra.Command, entityType, ref string, watch bool) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("refresh: load config: %w", err)
	}
	_, id, err := resolveTypedRef(cfg, entityType, ref)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	st, err := openNewSchemaStore("refresh")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true

	res, err := refreshEntityWatch(cmd.Context(), cfg, st, entityType, id, watch)
	if errors.Is(err, changes.ErrAmbiguousBackend) {
		// Not a hydration outcome: the id cannot be pinned to one backend, so
		// nothing was read. A plain error (exit 1) names the id.
		return fmt.Errorf("refresh: %s %s: %w", entityType, id, err)
	}
	if class := changes.RefreshClass(err, res.Degraded); class != "" {
		// The machine-readable class goes to standard output, ahead of the
		// error text main prints to standard error.
		if _, perr := fmt.Fprintf(cmd.OutOrStdout(), "class=%s\n", class); perr != nil {
			return perr
		}
	}
	switch {
	case err != nil:
		return newExitError(exitTotal, fmt.Errorf("refresh: %s %s: %w", entityType, id, notFoundLoud(err)))
	case res.Degraded != "":
		return newExitError(exitPartial, fmt.Errorf("refresh: %s %s: hydration degraded (previous snapshot kept, entity stays due): %s", entityType, id, res.Degraded))
	}
	kinds := "no change"
	if len(res.Kinds) > 0 {
		kinds = strings.Join(res.Kinds, ", ")
	}
	_, perr := fmt.Fprintf(cmd.OutOrStdout(), "refreshed %s %s: %s (version %d)\n", entityType, id, kinds, res.Version)
	return perr
}

// notFoundLoud makes sure a not-found hydration failure reads "not found".
// RunEntityChange says "not found" for the issue and thread types, but the PR
// gatherer reports pg-connector's "not_found" verbatim.
func notFoundLoud(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "not found") || !strings.Contains(msg, "not_found") {
		return err
	}
	return fmt.Errorf("not found: %w", err)
}

// refreshEntity hydrates one entity through RunEntityChange with
// refreshChangeKind and reports the outcome through changes.RecordHydration.
// It is the one hydration path a targeted refresh uses (show --refresh reuses
// it). A failed or degraded read writes nothing: the previous snapshot stands
// and the entity stays active and due.
func refreshEntity(ctx context.Context, cfg *config.Config, st *store.Store, entityType, entityID string) (pipeline.EntityChangeResult, error) {
	return refreshEntityWatch(ctx, cfg, st, entityType, entityID, false)
}

// refreshEntityWatch is refreshEntity with the one-off watch of `issue
// refresh --watch`. For an issue it first resolves the backend (the entity's
// backend column, else the configured watched queries, else the single owning
// instance for an id matching ticket_patterns) and pins the read to it, so a
// Jira id never costs a call to another backend first. An ambiguous id fails
// with changes.ErrAmbiguousBackend before any read. The pinned instance is
// also written to the backend column in the hydration write.
//
// watch asks the backend to watch the entity and marks it watched in the SAME
// write. With no resolved backend, the owning instances are probed for the
// id; an id none holds is read unpinned and watched locally, as is any id of a
// backend that keeps no store (no backend is asked to watch it). A daemon that answers unavailable while
// probing or reading is returned as an error wrapping gather.ErrUnavailable
// (class transient): nothing is written and the next run retries.
func refreshEntityWatch(ctx context.Context, cfg *config.Config, st *store.Store, entityType, entityID string, watch bool) (pipeline.EntityChangeResult, error) {
	if cfg == nil {
		return pipeline.EntityChangeResult{}, errors.New("refresh: no config")
	}
	opts := pipeline.EntityChangeOptions{
		Origin:             refreshOrigin,
		ThreadActiveWindow: cfg.ThreadActiveWindow(),
	}
	if entityType == entityTypeIssue {
		instance, owning, err := changes.ResolveIssueBackend(cfg, st, entityID)
		if err != nil {
			return pipeline.EntityChangeResult{}, err
		}
		if instance == "" && watch {
			// Nothing in the store or config names a backend: ask the owning
			// instances (a daemon read, never a bd call) so --watch can reach
			// the one that holds the id.
			var held bool
			instance, held, err = changes.ProbeOwning(ctx, gather.NewGatherer(cfg, st), cfg, entityID)
			if err != nil {
				res := pipeline.EntityChangeResult{}
				changes.RecordHydration(st, entityType, entityID, res, err)
				return res, err
			}
			owning = held
		}
		opts.Backend = instance
		// An owning backend is asked to watch the entity; a backend that keeps no
		// store (beads) has nothing to ask, so the hydration write alone marks it
		// watched (WatchLocal) and appends its one added row.
		opts.Watch = watch && owning
		opts.WatchLocal = watch && !owning
	}
	res, err := newRefreshHydrator(cfg, st).RunEntityChange(ctx, entityType, entityID, refreshChangeKind, opts)
	changes.RecordHydration(st, entityType, entityID, res, err)
	return res, err
}
