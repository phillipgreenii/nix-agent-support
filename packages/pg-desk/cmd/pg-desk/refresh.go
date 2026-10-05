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
	return &cobra.Command{
		Use:   "refresh <id>",
		Short: fmt.Sprintf("Re-hydrate one %s from pg-connector", entityType),
		Long: fmt.Sprintf(`Hydrate exactly one %[1]s through the entity-change pipeline: read it from
pg-connector, classify the change against the previous snapshot, and log it.

Exit codes: 0 hydrated; 2 hydration degraded (the previous snapshot is kept
and the entity stays due); 3 hydration failed entirely (previous snapshot
kept, entity stays due), including an entity that is not found.`, entityType),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRefresh(cmd, entityType, args[0])
		},
	}
}

func runRefresh(cmd *cobra.Command, entityType, ref string) error {
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

	res, err := refreshEntity(cmd.Context(), cfg, st, entityType, id)
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
	if cfg == nil {
		return pipeline.EntityChangeResult{}, errors.New("refresh: no config")
	}
	res, err := newRefreshHydrator(cfg, st).RunEntityChange(ctx, entityType, entityID, refreshChangeKind, pipeline.EntityChangeOptions{
		Origin:             refreshOrigin,
		ThreadActiveWindow: cfg.ThreadActiveWindow(),
	})
	changes.RecordHydration(st, entityType, entityID, res, err)
	return res, err
}
