package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/lanestate"
)

// The persisted PR lane families (PR lane refactor, P3b1a). The composed
// command is short-lived, so it persists per-PR state and cumulative counters
// through lanestate.Record and `pg-desk serve` aggregates them here at scrape
// time. Every series is emitted with a value (0 before any run), so "no
// series" never means "healthy" and increase() sees the first event.
const (
	// MetricPRLaneRuns counts the lane's non-clean run events by stage and
	// class (exported as pg_desk_pr_lane_runs_total).
	MetricPRLaneRuns = "pg_desk_pr_lane_runs"
	// MetricPRDecisions counts the decider's per-rule results by rule, action,
	// outcome and reason (exported as pg_decider_decisions_total).
	MetricPRDecisions = "pg_decider_decisions"
	// MetricPRMarkedInactive counts PRs marked inactive by the lane (exported
	// as pg_desk_pr_marked_inactive_total).
	MetricPRMarkedInactive = "pg_desk_pr_marked_inactive"
	// MetricPRLaneResidualPRs is the number of PRs with a non-empty residual in
	// the lane-level mode, labelled mode in off|plan|apply: the value sits on
	// the lane-level mode's series and the other two are 0.
	MetricPRLaneResidualPRs = "pg_desk_pr_lane_residual_prs"
	// MetricPRLaneResidualOldestAge is the age in seconds of the oldest such
	// residual (0 when there is none), labelled like MetricPRLaneResidualPRs.
	MetricPRLaneResidualOldestAge = "pg_desk_pr_lane_residual_oldest_age_seconds"
	// MetricPRLaneLookupItems and MetricPRLaneLookupCeiling are the decider's
	// last reported work-item lookup item count and configured ceiling (the
	// decider is a short-lived command that emits no telemetry of its own); 0
	// before any run reports them.
	MetricPRLaneLookupItems   = "pg_desk_pr_lane_lookup_items"
	MetricPRLaneLookupCeiling = "pg_desk_pr_lane_lookup_ceiling"
)

// PRLaneStateFunc supplies the persisted lane state at collect time. ok=false
// (with a nil error) means the store cannot answer (not migrated to schema 2):
// the families then export no series. A non-nil error also exports none.
type PRLaneStateFunc func() (snap lanestate.Snapshot, ok bool, err error)

// RegisterPRLaneState registers the persisted lane families on a meter from
// mp, read through one callback so a scrape sees one consistent snapshot.
func RegisterPRLaneState(mp metric.MeterProvider, fn PRLaneStateFunc) error {
	m := mp.Meter("github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk")

	runs, err := m.Int64ObservableCounter(MetricPRLaneRuns,
		metric.WithDescription("non-clean PR lane run events by stage and class (persisted by the composed command)"))
	if err != nil {
		return err
	}
	decisions, err := m.Int64ObservableCounter(MetricPRDecisions,
		metric.WithDescription("decider per-rule results by rule, action, outcome and reason (persisted by the composed command)"))
	if err != nil {
		return err
	}
	marked, err := m.Int64ObservableCounter(MetricPRMarkedInactive,
		metric.WithDescription("PRs the lane marked inactive (persisted)"))
	if err != nil {
		return err
	}
	residual, err := m.Int64ObservableGauge(MetricPRLaneResidualPRs,
		metric.WithDescription("PRs whose plan after the last run still lists an action, labelled with the lane-level mode (0 on the other modes)"))
	if err != nil {
		return err
	}
	oldest, err := m.Int64ObservableGauge(MetricPRLaneResidualOldestAge,
		metric.WithDescription("age in seconds of the oldest non-empty plan residual, 0 when there is none, labelled with the lane-level mode"))
	if err != nil {
		return err
	}
	items, err := m.Int64ObservableGauge(MetricPRLaneLookupItems,
		metric.WithDescription("work items the decider's last lookup returned (0 before a run reports it)"))
	if err != nil {
		return err
	}
	ceiling, err := m.Int64ObservableGauge(MetricPRLaneLookupCeiling,
		metric.WithDescription("the decider's configured work-item lookup ceiling (0 before a run reports it)"))
	if err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		snap, ok, err := fn()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		for _, stage := range lanestate.Stages {
			for _, class := range lanestate.Classes {
				o.ObserveInt64(runs, snap.Runs[lanestate.RunKey{Stage: stage, Class: class}],
					metric.WithAttributes(attribute.String("stage", stage), attribute.String("class", class)))
			}
		}
		// The zero-filled set first, then any recorded series outside it, so
		// nothing recorded is dropped.
		seen := map[lanestate.Decision]bool{}
		observeDecision := func(d lanestate.Decision) {
			if seen[d] {
				return
			}
			seen[d] = true
			o.ObserveInt64(decisions, snap.Decisions[d], metric.WithAttributes(
				attribute.String("rule", d.Rule), attribute.String("action", d.Action),
				attribute.String("outcome", d.Outcome), attribute.String("reason", d.Reason),
			))
		}
		for _, d := range lanestate.ZeroFilledDecisions() {
			observeDecision(d)
		}
		for d := range snap.Decisions {
			observeDecision(d)
		}
		o.ObserveInt64(marked, snap.MarkedInactive)
		for _, mode := range lanestate.Modes {
			var n, age int64
			if mode == snap.LaneMode {
				n, age = int64(snap.ResidualPRs), int64(snap.ResidualOldestAgeSeconds)
			}
			attr := metric.WithAttributes(attribute.String("mode", mode))
			o.ObserveInt64(residual, n, attr)
			o.ObserveInt64(oldest, age, attr)
		}
		o.ObserveInt64(items, snap.LookupItems)
		o.ObserveInt64(ceiling, snap.LookupCeiling)
		return nil
	}, runs, decisions, marked, residual, oldest, items, ceiling)
	return err
}
