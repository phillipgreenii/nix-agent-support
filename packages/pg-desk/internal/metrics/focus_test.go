package metrics

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/focus"
)

func collectFocus(t *testing.T, fn FocusFunc) (metricdata.ResourceMetrics, error) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := RegisterFocus(mp, fn); err != nil {
		t.Fatalf("RegisterFocus: %v", err)
	}
	var rm metricdata.ResourceMetrics
	err := reader.Collect(context.Background(), &rm)
	return rm, err
}

// series flattens every collected focus series to "name{label=value,...}" ->
// value, labels sorted.
func series(rm metricdata.ResourceMetrics) map[string]int64 {
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				continue
			}
			for _, dp := range g.DataPoints {
				var labels []string
				for _, kv := range dp.Attributes.ToSlice() {
					labels = append(labels, fmt.Sprintf("%s=%s", kv.Key, kv.Value.AsString()))
				}
				sort.Strings(labels)
				out[m.Name+"{"+strings.Join(labels, ",")+"}"] = dp.Value
			}
		}
	}
	return out
}

func dur(d time.Duration) *time.Duration { return &d }

func fullHealth() focus.Health {
	three := 3
	return focus.Health{
		Selected:          4,
		PlanRows:          map[string]int{"in_plan": 3, "finished": 1, "open_slots": 3},
		Beads:             map[string]int{"open": 2, "claimed": 1, "held": 1},
		OldestBeadAge:     map[string]time.Duration{"open": 90 * time.Second, "held": 2 * time.Hour},
		Pending:           map[string]int{"unminted": 2, "hidden": 1},
		OldestPendingAge:  map[string]time.Duration{"unminted": 31 * time.Minute},
		Divergence:        map[string]int{"selected_bead_held": 1, "row_without_annotation": 2},
		UnwatchedSelected: 1,
		Runs:              map[[2]string]int{{"select", "ok"}: 5, {"select", "total"}: 1, {"pull", "usage"}: 2},
		RunAge:            dur(time.Hour),
		RankInputs:        focus.RankInputs{UnparseableDue: 1, StatusCategoryAbsent: 4},
		Listings: []focus.TypedListing{
			{Type: "pr", Listing: focus.Listing{Query: "a", Status: "ok"}},
			{Type: "pr", Listing: focus.Listing{Query: "b", Status: "failed"}},
			{Type: "issue", Listing: focus.Listing{Query: "c", Status: "degraded"}},
			{Type: "issue", Listing: focus.Listing{Query: "d", Status: "unknown"}},
		},
		Candidates:         map[string]int{"pr": 5, "jira": 6, "bead": 7},
		LinkedCandidates:   2,
		DraftAge:           dur(10 * time.Minute),
		LastLockDraftAge:   dur(3 * time.Hour),
		LastLockDriftRows:  &three,
		JiraCategoryAbsent: 4,
		Coverage:           focus.CoverageState{PerType: map[string]focus.TypeCoverage{"pr": {}}},
	}
}

func TestFocusFamiliesCarryExactLabelsAndValues(t *testing.T) {
	rm, err := collectFocus(t, func() (focus.Health, error) { return fullHealth(), nil })
	if err != nil {
		t.Fatal(err)
	}
	got := series(rm)
	want := map[string]int64{
		"pg_desk_focus_selected{}":                                         4,
		"pg_desk_focus_beads{state=open}":                                  2,
		"pg_desk_focus_beads{state=claimed}":                               1,
		"pg_desk_focus_beads{state=held}":                                  1,
		"pg_desk_focus_beads{state=deferred_other}":                        0,
		"pg_desk_focus_beads{state=blocked}":                               0,
		"pg_desk_focus_beads{state=closed}":                                0,
		"pg_desk_focus_oldest_bead_age_seconds{state=open}":                90,
		"pg_desk_focus_oldest_bead_age_seconds{state=held}":                7200,
		"pg_desk_focus_pending_beads{reason=unminted}":                     2,
		"pg_desk_focus_pending_beads{reason=hidden}":                       1,
		"pg_desk_focus_pending_beads{reason=suppressed}":                   0,
		"pg_desk_focus_pending_beads{reason=no_bead_by_design}":            0,
		"pg_desk_focus_pending_beads{reason=unwatched}":                    0,
		"pg_desk_focus_pending_beads{reason=source_terminal}":              0,
		"pg_desk_focus_oldest_pending_bead_age_seconds{reason=unminted}":   1860,
		"pg_desk_focus_divergence{kind=row_without_annotation}":            2,
		"pg_desk_focus_divergence{kind=annotation_without_row}":            0,
		"pg_desk_focus_divergence{kind=stale_period}":                      0,
		"pg_desk_focus_divergence{kind=selected_bead_held}":                1,
		"pg_desk_focus_divergence{kind=removed_bead_open_unclaimed}":       0,
		"pg_desk_focus_divergence{kind=removed_bead_claimed}":              0,
		"pg_desk_focus_divergence{kind=terminal_source_bead_open}":         0,
		"pg_desk_focus_unwatched_selected{}":                               1,
		"pg_desk_focus_runs{outcome=ok,verb=select}":                       5,
		"pg_desk_focus_runs{outcome=total,verb=select}":                    1,
		"pg_desk_focus_runs{outcome=usage,verb=pull}":                      2,
		"pg_desk_focus_run_age_seconds{}":                                  3600,
		"pg_desk_focus_rank_degraded_inputs{input=unparseable_due}":        1,
		"pg_desk_focus_rank_degraded_inputs{input=unmapped_priority}":      0,
		"pg_desk_focus_rank_degraded_inputs{input=age_fallback}":           0,
		"pg_desk_focus_rank_degraded_inputs{input=unblocks_unavailable}":   0,
		"pg_desk_focus_rank_degraded_inputs{input=status_category_absent}": 4,
		"pg_desk_watch_listing_status{status=ok,type=pr}":                  1,
		"pg_desk_watch_listing_status{status=degraded,type=pr}":            0,
		"pg_desk_watch_listing_status{status=failed,type=pr}":              1,
		"pg_desk_watch_listing_status{status=ok,type=issue}":               0,
		"pg_desk_watch_listing_status{status=degraded,type=issue}":         1,
		"pg_desk_watch_listing_status{status=failed,type=issue}":           0,
		"pg_desk_focus_candidates{source=pr}":                              5,
		"pg_desk_focus_candidates{source=jira}":                            6,
		"pg_desk_focus_candidates{source=bead}":                            7,
		"pg_desk_focus_linked_candidates{}":                                2,
		"pg_desk_focus_draft_age_seconds{}":                                600,
		"pg_desk_focus_plan_rows{state=in_plan}":                           3,
		"pg_desk_focus_plan_rows{state=finished}":                          1,
		"pg_desk_focus_plan_rows{state=open_slots}":                        3,
		"pg_desk_focus_last_lock_draft_age_seconds{}":                      10800,
		"pg_desk_focus_last_lock_drift_rows{}":                             3,
		"pg_desk_focus_jira_status_category_absent{}":                      4,
	}
	for k, v := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("series %s missing", k)
		} else if g != v {
			t.Errorf("%s = %d, want %d", k, g, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("unexpected series %s", k)
		}
	}
}

func TestFocusSeriesNeverCarryAKeyOrBeadIDLabel(t *testing.T) {
	rm, err := collectFocus(t, func() (focus.Health, error) { return fullHealth(), nil })
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"state": true, "reason": true, "kind": true, "verb": true, "outcome": true, "input": true, "type": true, "status": true, "source": true}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				continue
			}
			for _, dp := range g.DataPoints {
				for _, kv := range dp.Attributes.ToSlice() {
					if !allowed[string(kv.Key)] {
						t.Errorf("%s carries label %q: no series may carry an entity key or a bead id", m.Name, kv.Key)
					}
				}
			}
		}
	}
}

func TestFocusOptionalSeriesAreAbsentWhenTheirSourceIsAbsent(t *testing.T) {
	h := fullHealth()
	h.DraftAge, h.RunAge, h.LastLockDraftAge, h.LastLockDriftRows = nil, nil, nil, nil
	h.OldestBeadAge, h.OldestPendingAge = nil, nil
	rm, err := collectFocus(t, func() (focus.Health, error) { return h, nil })
	if err != nil {
		t.Fatal(err)
	}
	for k := range series(rm) {
		for _, absent := range []string{
			MetricFocusDraftAge, MetricFocusRunAge, MetricFocusLastLockDraftAge, MetricFocusLastLockDriftRows,
			MetricFocusOldestBeadAge, MetricFocusOldestPendingBeadAge,
		} {
			if strings.HasPrefix(k, absent+"{") {
				t.Errorf("series %s present, want it absent (no source value)", k)
			}
		}
	}
}

func TestFocusEmitsNothingForAnUnmigratedStoreAndDoesNotFailTheScrape(t *testing.T) {
	rm, err := collectFocus(t, func() (focus.Health, error) { return focus.Health{}, nil })
	if err != nil {
		t.Fatalf("collect: %v, want no failure", err)
	}
	if got := series(rm); len(got) != 0 {
		t.Errorf("series = %v, want none", got)
	}
}

func TestFocusReadErrorSurfacesFromTheCollection(t *testing.T) {
	_, err := collectFocus(t, func() (focus.Health, error) { return focus.Health{}, errors.New("boom") })
	if err == nil {
		t.Fatal("want the read error, never an empty all-clear")
	}
}
