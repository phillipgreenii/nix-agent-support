package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

func init() {
	typeGroup(entityTypeIssue).AddCommand(newBackfillBackendCmd())
}

// newBackfillBackendCmd builds `pg-desk issue backfill-backend`, the one-shot
// cutover backfill of the backend and watched columns (see
// docs/behavior/pg-desk/operator-commands.md, "issue backfill-backend"). It is
// issue-only, so it registers under the issue group alone.
func newBackfillBackendCmd() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "backfill-backend",
		Short: "One-shot backfill of the backend and watched columns of every held issue",
		Long: `Set the backend column (and, where the backend states it, the watched mark) of
every issue pg-desk holds whose backend is still unset. It runs once, at the
cutover of the issue flow to the per-backend watched-only flow.

For each watched query served by a backend that owns its changes, the query's
members are read from that backend once; every returned id that pg-desk holds
gets the backend and, only when the backend states it, the watched mark (an
unstated mark leaves the column untouched). An id pg-desk does not hold is not
created. Every other held issue with no backend is probed against the
configured owning backend instances in turn (a daemon read, never a bd call):
the first instance that holds it is written, and an id none holds gets the
configured agent tracker backend.

The command appends no change row and bumps no version, and it never changes a
beads issue's watched mark. It is safe to re-run.

Exit codes: 0 when it completes; 1 when the config or store cannot be opened,
or when a list read or a probe failed (the report names which), so a partial
backfill is never reported as complete.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackfillBackend(cmd, resolveJSONOutput(jsonOut))
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, `Print {"updated": n, "failed": [...]} instead of one line per query and id`)
	return c
}

// backfillResult is the --json payload.
type backfillResult struct {
	Updated int      `json:"updated"`
	Failed  []string `json:"failed"`
}

func runBackfillBackend(cmd *cobra.Command, jsonOut bool) error {
	cfg, err := deskConfigLoad(cmd.Context())
	if err != nil {
		return fmt.Errorf("backfill-backend: load config: %w", err)
	}
	st, err := openNewSchemaStore("backfill-backend")
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	cmd.SilenceUsage = true

	lines := io.Discard
	if !jsonOut {
		lines = cmd.OutOrStdout()
	}
	res, err := backfillBackend(cmd.Context(), cfg, st, gather.NewGatherer(cfg, st), lines, cmd.ErrOrStderr())
	if err != nil {
		return fmt.Errorf("backfill-backend: %w", err)
	}
	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		if err := enc.Encode(res); err != nil {
			return err
		}
	}
	if len(res.Failed) > 0 {
		return fmt.Errorf("backfill-backend: %d failure(s), the backfill is incomplete (re-run it): %s",
			len(res.Failed), strings.Join(res.Failed, "; "))
	}
	return nil
}

// backfillBackend does the backfill. Each failed list read or probe is
// written to errw and recorded in the result; every query and id is still
// tried. The returned error is only a store fault.
func backfillBackend(ctx context.Context, cfg *config.Config, st *store.Store, g *gather.Gatherer, out, errw io.Writer) (backfillResult, error) {
	res := backfillResult{Failed: []string{}}
	fail := func(what string, err error) {
		msg := fmt.Sprintf("%s: %v", what, err)
		res.Failed = append(res.Failed, msg)
		_, _ = fmt.Fprintf(errw, "backfill-backend: %s\n", msg)
	}

	all, err := st.ListEntities()
	if err != nil {
		return res, fmt.Errorf("list entities: %w", err)
	}
	held := map[string][]store.Entity{} // issue id -> held rows (one per repo)
	for _, e := range all {
		if e.EntityType == entityTypeIssue {
			held[e.EntityID] = append(held[e.EntityID], e)
		}
	}
	done := map[string]bool{} // ids resolved by the list pass

	// Pass 1: one list read per owning watched query, in config order.
	for _, q := range cfg.OwningWatchQueries(entityTypeIssue) {
		wb, ok := cfg.WatchQueryBackend(entityTypeIssue, q)
		if !ok || wb.Instance == "" {
			fail("query "+q, errors.New("no backend instance configured"))
			continue
		}
		list, err := g.ListQueryMembers(ctx, q, wb.Instance)
		if err != nil {
			fail("query "+q, err)
			continue
		}
		if list.Truncated {
			fail("query "+q, errors.New("the list read was truncated"))
			continue
		}
		n := 0
		for _, m := range list.Members {
			rows := held[m.ID]
			if len(rows) == 0 || done[m.ID] {
				continue // not held (created by its first feed row) or already taken by an earlier query
			}
			done[m.ID] = true
			for _, e := range rows {
				backend := wb.Instance
				if err := st.SetEntityColumns(e.Repo, e.EntityType, e.EntityID, &backend, m.Watched); err != nil {
					return res, fmt.Errorf("set columns of %s: %w", m.ID, err)
				}
				res.Updated++
			}
			n++
		}
		_, _ = fmt.Fprintf(out, "query %s (%s): %d held member(s) set\n", q, wb.Instance, n)
	}

	// Pass 2: every other held issue with no backend, in a stable order.
	for _, e := range all {
		if e.EntityType != entityTypeIssue || e.Backend != "" || done[e.EntityID] {
			continue
		}
		inst, held, err := changes.ProbeOwning(ctx, g, cfg, e.EntityID)
		if err != nil {
			fail("issue "+e.EntityID, err)
			continue
		}
		if !held {
			inst = cfg.AgentTrackerBackend
			if inst == "" {
				fail("issue "+e.EntityID, errors.New("held by no owning backend and no agent_tracker_backend is configured"))
				continue
			}
		}
		backend := inst
		if err := st.SetEntityColumns(e.Repo, e.EntityType, e.EntityID, &backend, nil); err != nil {
			return res, fmt.Errorf("set backend of %s: %w", e.EntityID, err)
		}
		res.Updated++
		_, _ = fmt.Fprintf(out, "issue %s: backend %s\n", e.EntityID, inst)
	}
	return res, nil
}
