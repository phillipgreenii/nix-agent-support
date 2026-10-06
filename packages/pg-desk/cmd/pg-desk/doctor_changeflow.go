package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/changes"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// doctorProbeQuery asks pg-connector whether it recognizes a watched query,
// side-effect free (gather.ProbeQuery: the read-only listing verb through
// internal/gather's exec chokepoint). An injectable seam like the other
// doctor probes.
var doctorProbeQuery = func(ctx context.Context, cfg *config.Config, entityType, query string) error {
	return gather.NewGatherer(cfg, nil).ProbeQuery(ctx, entityType, query)
}

// consumerStalledMultiple is how many router periods a consumer may go
// unseen before doctor flags it [design: 11].
const consumerStalledMultiple = 3

// doctorChangeFlow runs the change-flow checks of design 11 and prints their
// report to w, appending the name of every failing check to *failures:
//
//   - every configured watched query resolves in pg-connector;
//   - every registered consumer's seen_at is within 3x its expected period
//     (the period of the router query naming that consumer and type when
//     --router-config is given, otherwise consumer_stale_after);
//   - the sweep sizing bound active_count / N x poll_interval <= D, only
//     when --router-config supplies the poll interval;
//   - and, reported but never a failure, the entities with repeated degraded
//     hydrations and (with --router-config) the decider roles bound per type.
//
// An unmigrated store is REPORTED as such and the new-schema checks are
// skipped: doctor never refuses or crashes on it, and the old-schema lines
// the caller already printed stay as they were. cfg is nil when config
// resolution failed (the config check already failed); the checks that need
// it are then skipped with a note.
func doctorChangeFlow(ctx context.Context, w io.Writer, cfg *config.Config, rc *routerConfig, failures *[]string) {
	fmt.Fprintln(w, "change_flow:")
	st, err := deskStoreOpenRaw()
	if err != nil {
		fmt.Fprintf(w, "  store: FAIL (open store: %v)\n", err)
		*failures = append(*failures, "change_flow store")
		return
	}
	defer func() { _ = st.Close() }()
	version, err := st.SchemaVersion()
	if err != nil {
		fmt.Fprintf(w, "  store: FAIL (read schema version: %v)\n", err)
		*failures = append(*failures, "change_flow store")
		return
	}
	if version < store.NewSchemaVersion {
		fmt.Fprintln(w, "  unmigrated: the store is not on the change-flow schema; change-flow checks skipped; run pg-desk migrate --cutover")
		return
	}

	checkCfg := cfg
	if checkCfg == nil {
		checkCfg = &config.Config{}
	}
	now := changesNow()
	flow, err := changes.Observe(st, now, checkCfg.SweepMaxAge())
	if err != nil {
		fmt.Fprintf(w, "  observe: FAIL (%v)\n", err)
		*failures = append(*failures, "change_flow observe")
		return
	}

	doctorWatchedQueries(ctx, w, cfg, failures)
	doctorStalledConsumers(w, checkCfg, rc, flow, now, failures)
	doctorSweepBound(w, checkCfg, rc, flow, failures)
	doctorRepeatedDegraded(w, flow)
	if rc != nil {
		doctorRouterRoles(w, rc)
	}
}

// doctorWatchedQueries probes every configured watched query.
func doctorWatchedQueries(ctx context.Context, w io.Writer, cfg *config.Config, failures *[]string) {
	fmt.Fprintln(w, "  watched queries:")
	if cfg == nil {
		fmt.Fprintln(w, "    skipped (config unavailable)")
		return
	}
	found := false
	bad := false
	for _, t := range changes.FlowEntityTypes {
		for _, q := range cfg.WatchQueries(t) {
			found = true
			if err := doctorProbeQuery(ctx, cfg, t, q); err != nil {
				bad = true
				fmt.Fprintf(w, "    %s %q: FAIL (%v)\n", t, q, err)
			} else {
				fmt.Fprintf(w, "    %s %q: ok\n", t, q)
			}
		}
	}
	if !found {
		fmt.Fprintln(w, "    none configured")
	}
	if bad {
		*failures = append(*failures, "watched queries")
	}
}

// doctorStalledConsumers flags a consumer unseen for more than 3x its
// expected period. A flagged consumer is reported, never pruned [design 6.9].
func doctorStalledConsumers(w io.Writer, cfg *config.Config, rc *routerConfig, flow changes.Flow, now time.Time, failures *[]string) {
	fmt.Fprintln(w, "  consumers:")
	if len(flow.Consumers) == 0 {
		fmt.Fprintln(w, "    none registered")
		return
	}
	fallback := cfg.ConsumerStaleAfter()
	if fallback <= 0 {
		fallback = store.DefaultConsumerStaleAfter
	}
	stalled := false
	for _, c := range flow.Consumers {
		threshold, basis := fallback, "consumer_stale_after"
		if rc != nil {
			if period, ok := rc.ConsumerPeriod(c.Type, c.Name); ok {
				threshold, basis = consumerStalledMultiple*period, fmt.Sprintf("%dx router period %s", consumerStalledMultiple, period)
			}
		}
		seen := c.SeenAt
		if seen == "" {
			seen = "never"
		}
		state := "ok"
		if consumerIsStale(c.SeenAt, now, threshold) {
			state = "STALLED"
			stalled = true
		}
		fmt.Fprintf(w, "    %s/%s: seen_at=%s threshold=%s (%s): %s\n", c.Type, c.Name, seen, threshold, basis, state)
	}
	if stalled {
		*failures = append(*failures, "stalled consumers")
	}
}

// doctorSweepBound evaluates active_count / N x poll_interval <= max_age per
// type AND per capped age tier (remote re-hydration against sweep.max_age,
// local reconcile against sweep.reconcile_age) when the router config supplies
// the poll interval; otherwise it reports the bound's inputs with
// poll_interval: unknown and NO verdict [design 8.4]. The verdict is
// changes.EvaluateSweepBound, the same evaluation behind the
// pg_desk_sweep_bound_violated metric and its alert, so the two agree.
func doctorSweepBound(w io.Writer, cfg *config.Config, rc *routerConfig, flow changes.Flow, failures *[]string) {
	fmt.Fprintln(w, "  sweep bound:")
	violated := false
	for _, t := range flow.Types {
		if !isFlowEntityType(t.Type) {
			continue
		}
		var poll time.Duration
		known := false
		if rc != nil {
			poll, known = rc.PollInterval(t.Type)
		}
		for _, tier := range []string{changes.TierRemote, changes.TierLocal} {
			in := changes.SweepInputsForTier(cfg, t, tier)
			inputs := fmt.Sprintf("active_count=%d max_per_poll=%d max_age=%s", in.ActiveCount, in.MaxPerPoll, in.MaxAge.Round(time.Second))
			switch changes.EvaluateSweepBound(in, poll, known) {
			case changes.BoundUnknown:
				fmt.Fprintf(w, "    %s %s: %s poll_interval: unknown (no verdict)\n", t.Type, tier, inputs)
			case changes.BoundHolds:
				fmt.Fprintf(w, "    %s %s: %s poll_interval=%s: holds\n", t.Type, tier, inputs, poll)
			case changes.BoundViolated:
				violated = true
				fmt.Fprintf(w, "    %s %s: %s poll_interval=%s: VIOLATED (the sweep falls behind; some active entities will go longer than max_age between hydrations)\n", t.Type, tier, inputs, poll)
			}
		}
	}
	if violated {
		*failures = append(*failures, "sweep bound")
	}
}

func isFlowEntityType(t string) bool {
	for _, k := range changes.FlowEntityTypes {
		if k == t {
			return true
		}
	}
	return false
}

// doctorRepeatedDegraded reports entities with repeated degraded hydrations
// (changes.RepeatedDegraded, the one definition shared with status).
func doctorRepeatedDegraded(w io.Writer, flow changes.Flow) {
	fmt.Fprintln(w, "  repeated degraded hydrations:")
	found := false
	for _, t := range flow.Types {
		if len(t.Repeated) == 0 {
			continue
		}
		found = true
		ids := make([]string, 0, len(t.Repeated))
		for id := range t.Repeated {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(w, "    %s %s: count=%d since=%s\n", t.Type, id, t.Repeated[id].Count, t.Repeated[id].Since)
		}
	}
	if !found {
		fmt.Fprintln(w, "    none")
	}
}

// doctorRouterRoles lists which decider roles bind to each type. The list is
// expected empty for issue and thread on day one [design 11].
func doctorRouterRoles(w io.Writer, rc *routerConfig) {
	fmt.Fprintln(w, "  router roles:")
	for _, t := range changes.FlowEntityTypes {
		roles := rc.RolesBoundTo(t)
		if len(roles) == 0 {
			fmt.Fprintf(w, "    %s: none\n", t)
			continue
		}
		for _, r := range roles {
			state := ""
			if !r.Enabled {
				state = " (disabled)"
			}
			fmt.Fprintf(w, "    %s: %s%s\n", t, r.Name, state)
		}
	}
}
