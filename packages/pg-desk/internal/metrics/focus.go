package metrics

import (
	"context"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

// The focus catalog (daily-focus store-first design, observability). Every
// family is a gauge computed from the store at scrape time, like the
// change-flow families. The label sets are closed and bounded; NO series
// carries an entity key or a bead id. "Selected rows" and "the current plan"
// mean the rows of the NEWEST period that holds rows, never the local day.
const (
	// MetricFocusSelected is the number of rows of the current plan.
	MetricFocusSelected = "pg_desk_focus_selected"
	// MetricFocusBeads counts focus beads by state (open, claimed, held,
	// deferred_other, blocked, closed).
	MetricFocusBeads = "pg_desk_focus_beads"
	// MetricFocusOldestBeadAge is the age in seconds of the oldest focus bead
	// in state open (minted and never claimed) and held.
	MetricFocusOldestBeadAge = "pg_desk_focus_oldest_bead_age_seconds"
	// MetricFocusPendingBeads counts rows of the current plan with no bead to
	// wait for, by reason (unminted, hidden, suppressed, no_bead_by_design,
	// unwatched, source_terminal). Only unminted can page.
	MetricFocusPendingBeads = "pg_desk_focus_pending_beads"
	// MetricFocusOldestPendingBeadAge is the age in seconds of the oldest
	// pending row per reason.
	MetricFocusOldestPendingBeadAge = "pg_desk_focus_oldest_pending_bead_age_seconds"
	// MetricFocusDivergence counts the seven divergence kinds.
	MetricFocusDivergence = "pg_desk_focus_divergence"
	// MetricFocusUnwatchedSelected counts selected rows in no watch set.
	MetricFocusUnwatchedSelected = "pg_desk_focus_unwatched_selected"
	// MetricFocusRuns counts focus_run rows by verb and outcome.
	MetricFocusRuns = "pg_desk_focus_runs"
	// MetricFocusRunAge is the seconds since the last non-dry-run select;
	// absent when none ran.
	MetricFocusRunAge = "pg_desk_focus_run_age_seconds"
	// MetricFocusRankDegradedInputs counts the silent rank degradations by
	// input.
	MetricFocusRankDegradedInputs = "pg_desk_focus_rank_degraded_inputs"
	// MetricWatchListingStatus counts watch queries by entity type and
	// persisted listing status (ok, degraded, failed).
	MetricWatchListingStatus = "pg_desk_watch_listing_status"
	// MetricFocusCandidates counts the candidate set by source (pr, jira,
	// bead).
	MetricFocusCandidates = "pg_desk_focus_candidates"
	// MetricFocusLinkedCandidates counts the candidates in the set only
	// through a link.
	MetricFocusLinkedCandidates = "pg_desk_focus_linked_candidates"
	// MetricFocusDraftAge is the seconds since the stored draft's made_at;
	// absent when no draft is stored.
	MetricFocusDraftAge = "pg_desk_focus_draft_age_seconds"
	// MetricFocusPlanRows counts the plan against its cap: in_plan, finished,
	// open_slots.
	MetricFocusPlanRows = "pg_desk_focus_plan_rows"
	// MetricFocusLastLockDraftAge is the stored draft's age at the last lock;
	// absent when no select run recorded it.
	MetricFocusLastLockDraftAge = "pg_desk_focus_last_lock_draft_age_seconds"
	// MetricFocusLastLockDriftRows is the draft rows a fresh rank would have
	// placed differently at the last lock; absent when no select run recorded
	// it.
	MetricFocusLastLockDriftRows = "pg_desk_focus_last_lock_drift_rows"
	// MetricFocusJiraStatusCategoryAbsent counts active Jira issues whose
	// stored facts lack a status category.
	MetricFocusJiraStatusCategoryAbsent = "pg_desk_focus_jira_status_category_absent"
)

// FocusFunc supplies the focus health at collect time (focus.ComputeHealth).
// It MUST return a zero Health (Coverage.PerType empty) for a store that is
// not migrated for the focus tables: the catalog then emits no focus series
// rather than failing the scrape.
type FocusFunc func() (focus.Health, error)

// RegisterFocus registers the focus instruments on a meter from mp. One
// callback reads the health once per scrape and feeds every instrument, so the
// families always agree with each other (and with the alert rules and the
// doctor block, which read the same focus.Health).
func RegisterFocus(mp metric.MeterProvider, fn FocusFunc) error {
	m := mp.Meter("github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk")

	var all []metric.Observable
	gauge := func(name, desc string) (metric.Int64ObservableGauge, error) {
		g, err := m.Int64ObservableGauge(name, metric.WithDescription(desc))
		if err == nil {
			all = append(all, g)
		}
		return g, err
	}

	selected, err := gauge(MetricFocusSelected, "rows of the current focus plan (the newest period that holds rows)")
	if err != nil {
		return err
	}
	beads, err := gauge(MetricFocusBeads, "focus beads by state: open, claimed, held, deferred_other, blocked, closed")
	if err != nil {
		return err
	}
	beadAge, err := gauge(MetricFocusOldestBeadAge, "age of the oldest focus bead in state open (minted, never claimed) or held, seconds")
	if err != nil {
		return err
	}
	pending, err := gauge(MetricFocusPendingBeads, "rows of the current plan with no bead to wait for, by reason; only unminted can page")
	if err != nil {
		return err
	}
	pendingAge, err := gauge(MetricFocusOldestPendingBeadAge, "age of the oldest pending row per reason, seconds since it was selected")
	if err != nil {
		return err
	}
	diverge, err := gauge(MetricFocusDivergence, "focus store disagreements by kind (row_without_annotation, annotation_without_row, stale_period, selected_bead_held, removed_bead_open_unclaimed, removed_bead_claimed, terminal_source_bead_open)")
	if err != nil {
		return err
	}
	unwatched, err := gauge(MetricFocusUnwatchedSelected, "selected rows whose entity is in no watch query set")
	if err != nil {
		return err
	}
	runs, err := gauge(MetricFocusRuns, "focus_run rows by verb and outcome (ok, partial, total, period_closed, usage, empty)")
	if err != nil {
		return err
	}
	runAge, err := gauge(MetricFocusRunAge, "seconds since the last non-dry-run select; absent when none ran")
	if err != nil {
		return err
	}
	degraded, err := gauge(MetricFocusRankDegradedInputs, "silent rank degradations counted per rank, by input")
	if err != nil {
		return err
	}
	listing, err := gauge(MetricWatchListingStatus, "watch queries by entity type and persisted listing status (ok, degraded, failed)")
	if err != nil {
		return err
	}
	cands, err := gauge(MetricFocusCandidates, "focus candidate set by source (pr, jira, bead)")
	if err != nil {
		return err
	}
	linked, err := gauge(MetricFocusLinkedCandidates, "candidates in the set only through a link")
	if err != nil {
		return err
	}
	draftAge, err := gauge(MetricFocusDraftAge, "seconds since the stored draft's made_at; absent when no draft is stored")
	if err != nil {
		return err
	}
	planRows, err := gauge(MetricFocusPlanRows, "the current plan against its cap: in_plan, finished, open_slots")
	if err != nil {
		return err
	}
	lockAge, err := gauge(MetricFocusLastLockDraftAge, "age of the stored draft at the last select run, seconds; absent when not recorded")
	if err != nil {
		return err
	}
	lockDrift, err := gauge(MetricFocusLastLockDriftRows, "draft rows a fresh rank would have placed differently at the last select run; absent when not recorded")
	if err != nil {
		return err
	}
	catAbsent, err := gauge(MetricFocusJiraStatusCategoryAbsent, "active Jira issues whose stored facts lack a status category")
	if err != nil {
		return err
	}

	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		h, err := fn()
		if err != nil {
			return err
		}
		if !h.Migrated() {
			return nil // degrade: no focus series on an unmigrated store
		}
		label := func(k, v string) metric.MeasurementOption {
			return metric.WithAttributes(attribute.String(k, v))
		}
		o.ObserveInt64(selected, int64(h.Selected))
		for _, s := range focus.HealthBeadStates {
			o.ObserveInt64(beads, int64(h.Beads[s]), label("state", s))
		}
		for _, s := range focus.HealthAgedBeadStates {
			if age, ok := h.OldestBeadAge[s]; ok {
				o.ObserveInt64(beadAge, int64(age.Seconds()), label("state", s))
			}
		}
		for _, r := range focus.HealthPendingReasons {
			o.ObserveInt64(pending, int64(h.Pending[r]), label("reason", r))
			if age, ok := h.OldestPendingAge[r]; ok {
				o.ObserveInt64(pendingAge, int64(age.Seconds()), label("reason", r))
			}
		}
		for _, k := range focus.HealthDivergenceKinds {
			o.ObserveInt64(diverge, int64(h.Divergence[k]), label("kind", k))
		}
		o.ObserveInt64(unwatched, int64(h.UnwatchedSelected))

		verbOutcomes := make([][2]string, 0, len(h.Runs))
		for k := range h.Runs {
			verbOutcomes = append(verbOutcomes, k)
		}
		sort.Slice(verbOutcomes, func(i, j int) bool {
			if verbOutcomes[i][0] != verbOutcomes[j][0] {
				return verbOutcomes[i][0] < verbOutcomes[j][0]
			}
			return verbOutcomes[i][1] < verbOutcomes[j][1]
		})
		for _, k := range verbOutcomes {
			o.ObserveInt64(runs, int64(h.Runs[k]), metric.WithAttributes(
				attribute.String("verb", k[0]), attribute.String("outcome", k[1]),
			))
		}
		if h.RunAge != nil {
			o.ObserveInt64(runAge, int64(h.RunAge.Seconds()))
		}

		ri := map[string]int{
			"unparseable_due":        h.RankInputs.UnparseableDue,
			"unmapped_priority":      h.RankInputs.UnmappedPriority,
			"age_fallback":           h.RankInputs.AgeFallback,
			"unblocks_unavailable":   h.RankInputs.UnblocksUnavailable,
			"status_category_absent": h.RankInputs.StatusCategoryAbsent,
		}
		for _, in := range focus.HealthRankInputs {
			o.ObserveInt64(degraded, int64(ri[in]), label("input", in))
		}

		// One series per (type, status) for every type that has a configured
		// watch query, zero-filled so a recovery is visible as a fall to 0.
		counts := map[string]map[string]int{}
		for _, l := range h.Listings {
			if counts[l.Type] == nil {
				counts[l.Type] = map[string]int{}
			}
			counts[l.Type][l.Status]++
		}
		types := make([]string, 0, len(counts))
		for t := range counts {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, t := range types {
			for _, s := range focus.HealthListingStatuses {
				o.ObserveInt64(listing, int64(counts[t][s]), metric.WithAttributes(
					attribute.String("type", t), attribute.String("status", s),
				))
			}
		}

		for _, s := range focus.HealthCandidateSources {
			o.ObserveInt64(cands, int64(h.Candidates[s]), label("source", s))
		}
		o.ObserveInt64(linked, int64(h.LinkedCandidates))
		if h.DraftAge != nil {
			o.ObserveInt64(draftAge, int64(h.DraftAge.Seconds()))
		}
		for _, s := range focus.HealthPlanRowStates {
			o.ObserveInt64(planRows, int64(h.PlanRows[s]), label("state", s))
		}
		if h.LastLockDraftAge != nil {
			o.ObserveInt64(lockAge, int64(h.LastLockDraftAge.Seconds()))
		}
		if h.LastLockDriftRows != nil {
			o.ObserveInt64(lockDrift, int64(*h.LastLockDriftRows))
		}
		o.ObserveInt64(catAbsent, int64(h.JiraCategoryAbsent))
		return nil
	}, all...)
	return err
}
