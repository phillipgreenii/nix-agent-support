package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// statusCmd implements `pg-desk status`
// [docs/behavior/pg-desk/operator-commands.md].
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print the store path, schema version, and health summary",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatus(cmd)
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command) error {
	// status must work on a store that has not been initialized yet, so it
	// opens raw (no schema creation, no version gate) rather than creating the
	// file. A store left on the retired schema 1 is refused below.
	st, err := deskStoreOpenRaw()
	if err != nil {
		return fmt.Errorf("status: open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	w := cmd.OutOrStdout()

	// The raw open leaves an empty file empty. With no schema there is
	// nothing to count, and querying would fail on the missing tables, so
	// report the same shape with zeros.
	version, err := st.SchemaVersion()
	if err != nil {
		return fmt.Errorf("status: read schema version: %w", err)
	}
	if version > 0 && version < store.NewSchemaVersion {
		return fmt.Errorf("status: %w", st.RequireNewSchema())
	}
	initialized := version > 0

	var (
		schemaVersion, lastHeartbeat, lastRun, lastSweep string
		entityCount, degraded                            int
		interps                                          []store.Interpretation
	)
	if initialized {
		schemaVersion, _, err = st.GetMeta(store.MetaKeySchemaVersion)
		if err != nil {
			return fmt.Errorf("status: read meta.schema_version: %w", err)
		}
		entityCount, err = st.CountEntities()
		if err != nil {
			return fmt.Errorf("status: count entities: %w", err)
		}
		interps, err = st.ListInterpretations()
		if err != nil {
			return fmt.Errorf("status: list interpretations: %w", err)
		}
		for _, i := range interps {
			if i.Degraded {
				degraded++
			}
		}
		lastHeartbeat, _, err = st.GetMeta(store.MetaKeyLastHeartbeat)
		if err != nil {
			return fmt.Errorf("status: read meta.last_heartbeat: %w", err)
		}
		lastRun, _, err = st.GetMeta(store.MetaKeyLastRun)
		if err != nil {
			return fmt.Errorf("status: read meta.last_run: %w", err)
		}
		lastSweep, _, err = st.GetMeta(store.MetaKeyLastSweep)
		if err != nil {
			return fmt.Errorf("status: read meta.last_sweep: %w", err)
		}
	}

	fmt.Fprintf(w, "store: %s\n", store.DefaultPath())
	fmt.Fprintf(w, "schema_version: %s\n", orDash(schemaVersion))
	fmt.Fprintf(w, "entities: %d\n", entityCount)
	fmt.Fprintf(w, "interpretations: %d\n", len(interps))
	fmt.Fprintf(w, "degraded: %d\n", degraded)
	fmt.Fprintf(w, "last_heartbeat: %s\n", orDash(lastHeartbeat))
	fmt.Fprintf(w, "last_run: %s\n", orDash(lastRun))
	fmt.Fprintf(w, "last_sweep: %s\n", orDash(lastSweep))

	// The live pr lane line (design: status truthfulness in the interim),
	// read from the persisted store state. A config-load failure degrades to
	// "unknown", like the sections below.
	laneCfg, _ := deskConfigLoad(cmd.Context())
	fmt.Fprintln(w, prLaneLine(laneCfg, st, changesNow()))

	if err := printInterpretationCatchUp(w, st, version, laneCfg); err != nil {
		return fmt.Errorf("status: %w", err)
	}

	if err := printChangeFlow(cmd.Context(), w, st, version); err != nil {
		return fmt.Errorf("status: %w", err)
	}

	// The focus: block (gate and report lines of the shared health model). A
	// config-load failure degrades to defaults, like the change-flow section.
	focusCfg, focusCfgErr := deskConfigLoad(cmd.Context())
	if focusCfgErr != nil || focusCfg == nil {
		focusCfg = &config.Config{}
	}
	printFocusBlock(cmd.Context(), w, st, focusCfg, nil, nil)

	return nil
}

// printInterpretationCatchUp prints the interpretation catch-up state
// (docs/behavior/pg-desk/reinterpret.md, "Observability"): how many `pr`
// interpretations are behind the running stamp, how many are marked failed
// under it, and the counts and time of the last catch-up pass. Counts only, no
// verdict. It reads stored state only, so it works on the raw handle status
// opens; a store that cannot answer (not initialized, no stamp column yet, no
// configuration to name the repo and the stamp) reports "unknown", never 0.
func printInterpretationCatchUp(w io.Writer, st *store.Store, version int, cfg *config.Config) error {
	switch {
	case version < store.NewSchemaVersion:
		fmt.Fprintln(w, "interpretations behind: unknown (store not initialized)")
	case cfg == nil || len(cfg.Repos) == 0:
		fmt.Fprintln(w, "interpretations behind: unknown (configuration unavailable)")
	default:
		stamp := runningStamp(cfg)
		repo := cfg.Repos[0].Remote
		st.SetInterpStamp(stamp)
		behind, known, err := st.InterpretationsBehind(repo, 0)
		if err != nil {
			return fmt.Errorf("count interpretations behind: %w", err)
		}
		if !known {
			fmt.Fprintln(w, "interpretations behind: unknown (column not yet added)")
			break
		}
		failed, _, err := st.InterpretationsFailed(repo)
		if err != nil {
			return fmt.Errorf("count interpretations failed: %w", err)
		}
		fmt.Fprintf(w, "interpretations behind: %d (stamp differs from %s)\n", len(behind), stamp)
		fmt.Fprintf(w, "interpretations failed: %d\n", failed)
	}
	if version < store.NewSchemaVersion {
		return nil
	}
	run, found, err := st.LastReinterpretRun()
	if err != nil {
		return fmt.Errorf("read last catch-up: %w", err)
	}
	if !found {
		fmt.Fprintln(w, "last catch-up: never")
		return nil
	}
	fmt.Fprintf(w, "last catch-up: %s via %s: selected %d, changed %d, unchanged %d, stamped only %d, skipped %d, failed %d, remaining %d\n",
		orDash(run.At), orDash(run.Source), run.Selected, run.Changed, run.Unchanged, run.StampedOnly, run.Skipped, run.Failed, run.Remaining)
	return nil
}

// printChangeFlow prints the change-flow section [design 11]: per type, the
// records logged by kind and origin, hydrations, hydration failures, due
// backlog, optimistic-concurrency retries, repeated degraded hydrations and
// the sweep bound's inputs; and per consumer, its cursor, lag and liveness
// (seen_at, which replaces the retired heartbeat). A store that has no schema
// yet has none of this data, so the section reports "uninitialized" instead of
// failing.
//
// The sweep bound active_count / N x poll_interval <= D needs the router's
// poll interval, which pg-desk does not own and status has no router-config
// input for, so status prints the bound's inputs with poll_interval: unknown
// and NO verdict; only `doctor --router-config` evaluates it.
func printChangeFlow(ctx context.Context, w io.Writer, st *store.Store, version int) error {
	fmt.Fprintln(w, "change_flow:")
	if version < store.NewSchemaVersion {
		fmt.Fprintln(w, "  uninitialized: the store has no schema yet")
		return nil
	}
	// A config-load failure degrades to the documented defaults rather than
	// failing status (config resolution is doctor's check).
	cfg, cfgErr := deskConfigLoad(ctx)
	if cfgErr != nil || cfg == nil {
		cfg = &config.Config{}
		fmt.Fprintf(w, "  config: unavailable (%v); defaults assumed\n", cfgErr)
	}
	flow, err := changes.Observe(st, changesNow(), cfg.SweepMaxAge())
	if err != nil {
		return fmt.Errorf("observe change flow: %w", err)
	}
	for _, t := range flow.Types {
		in := changes.SweepInputsFor(cfg, t)
		fmt.Fprintf(w, "  type %s:\n", t.Type)
		fmt.Fprintf(w, "    active: %d\n", t.Active)
		fmt.Fprintf(w, "    due_backlog: %d\n", t.Due)
		if changes.HasSweepBound(t.Type) {
			fmt.Fprintf(w, "    sweep_bound: active_count=%d max_per_poll=%d max_age=%s poll_interval=unknown\n",
				in.ActiveCount, in.MaxPerPoll, in.MaxAge.Round(time.Second))
		}
		fmt.Fprintf(w, "    hydrations: %d\n", t.Stats.Hydrations)
		fmt.Fprintf(w, "    hydration_failures: %d\n", t.Stats.Failures)
		fmt.Fprintf(w, "    occ_retries: %d\n", t.Stats.OCCRetries)
		fmt.Fprintf(w, "    repeated_degraded: %d\n", len(t.Repeated))
		ids := make([]string, 0, len(t.Repeated))
		for id := range t.Repeated {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(w, "      %s count=%d since=%s\n", id, t.Repeated[id].Count, t.Repeated[id].Since)
		}
		if len(t.Records) == 0 {
			fmt.Fprintln(w, "    records: none")
		} else {
			fmt.Fprintln(w, "    records:")
			for _, r := range t.Records {
				fmt.Fprintf(w, "      kind=%s origin=%s count=%d\n", r.Kind, r.Origin, r.Count)
			}
		}
		fmt.Fprintln(w, "    consumers:")
		found := false
		for _, c := range flow.Consumers {
			if c.Type != t.Type {
				continue
			}
			found = true
			fmt.Fprintf(w, "      %s cursor=%d lag=%d seen_at=%s\n", c.Name, c.Cursor, c.Lag, orDash(c.SeenAt))
		}
		if !found {
			fmt.Fprintln(w, "      none")
		}
	}
	fmt.Fprintf(w, "  ingest_failed: %d (flagged, not retried: %d)\n", flow.IngestFailed, len(flow.IngestFlagged))
	for _, f := range flow.IngestFlagged {
		fmt.Fprintf(w, "    flagged %s %s seq=%d class=%s since=%s: %s\n",
			f.Instance, f.EntityID, f.Seq, orDash(f.FailureClass), f.FlaggedAt, f.LastError)
	}
	return nil
}
