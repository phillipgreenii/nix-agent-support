// Package metrics computes the pg-review gauges from the ledger and pushes
// them, best effort. Every metric is a gauge computed from the ledger (a total
// is a gauge of the ledger's count), so the numbers survive process exits and
// restarts. A push never changes a command's outcome or exit code, never blocks
// a ruling and never delays a run beyond a short bounded timeout.
package metrics

import (
	"context"
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

// Gauge is one gauge point.
type Gauge struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Def is one catalog line: a metric name, its type and its label keys.
type Def struct {
	Name   string
	Type   string
	Labels []string
}

// Catalog lists every metric the code emits, in catalog-file order. The
// checked-in catalog file must agree with it; a test fails when they differ.
var Catalog = []Def{
	{"pg_review_runs", "gauge", []string{"lens", "scope", "outcome"}},
	{"pg_review_run_duration_seconds", "gauge", []string{"lens", "scope"}},
	{"pg_review_findings_last_run", "gauge", []string{"lens", "scope"}},
	{"pg_review_last_success_timestamp_seconds", "gauge", []string{"lens", "scope", "schedule"}},
	{"pg_review_last_window_seconds", "gauge", []string{"lens", "scope", "schedule"}},
	{"pg_review_consecutive_failed_runs", "gauge", []string{"lens", "scope", "schedule"}},
	{"pg_review_pending_items", "gauge", []string{"lens", "scope"}},
	{"pg_review_oldest_pending_age_seconds", "gauge", []string{"lens", "scope"}},
	{"pg_review_topics", "gauge", []string{"lens", "scope", "state"}},
	{"pg_review_decisions", "gauge", []string{"lens", "scope", "ruling"}},
	{"pg_review_subjects", "gauge", []string{"lens", "scope", "type"}},
	{"pg_review_outbox_failed", "gauge", []string{"lens", "scope"}},
}

func g(name string, v float64, kv ...string) Gauge {
	labels := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			labels[kv[i]] = kv[i+1]
		}
	}
	return Gauge{Name: name, Labels: labels, Value: v}
}

// Collect turns the ledger figures into gauges. now is the instant the age of
// the oldest pending item is measured against.
func Collect(md ledger.MetricsData, now time.Time) []Gauge {
	var out []Gauge
	for _, c := range md.Runs {
		out = append(out, g("pg_review_runs", float64(c.N), "lens", c.Lens, "scope", c.Scope, "outcome", c.Outcome))
	}
	for _, r := range md.Latest {
		out = append(out,
			g("pg_review_run_duration_seconds", r.DurationSeconds, "lens", r.Lens, "scope", r.Scope),
			g("pg_review_findings_last_run", float64(r.ItemsFiled), "lens", r.Lens, "scope", r.Scope))
	}
	for _, s := range md.Schedules {
		// Only scheduled runs reach here; a manual run has no schedule id, so
		// it neither produces a series nor touches the failure count.
		if s.HasFiled {
			out = append(out,
				g("pg_review_last_success_timestamp_seconds", float64(s.LastSuccess.Unix()), "lens", s.Lens, "scope", s.Scope, "schedule", s.Schedule),
				g("pg_review_last_window_seconds", s.WindowSeconds, "lens", s.Lens, "scope", s.Scope, "schedule", s.Schedule))
		}
		out = append(out, g("pg_review_consecutive_failed_runs", float64(s.ConsecutiveFailed), "lens", s.Lens, "scope", s.Scope, "schedule", s.Schedule))
	}
	for _, c := range md.Pending {
		out = append(out, g("pg_review_pending_items", float64(c.N), "lens", c.Lens, "scope", c.Scope))
	}
	for _, c := range md.Pending {
		age := 0.0
		if !c.Oldest.IsZero() && now.After(c.Oldest) {
			age = now.Sub(c.Oldest).Seconds()
		}
		out = append(out, g("pg_review_oldest_pending_age_seconds", age, "lens", c.Lens, "scope", c.Scope))
	}
	if len(md.Pending) == 0 {
		out = append(out, g("pg_review_pending_items", 0), g("pg_review_oldest_pending_age_seconds", 0))
	}
	for _, c := range md.Topics {
		out = append(out, g("pg_review_topics", float64(c.N), "lens", c.Lens, "scope", c.Scope, "state", c.Label))
	}
	for _, c := range md.Decisions {
		out = append(out, g("pg_review_decisions", float64(c.N), "lens", c.Lens, "scope", c.Scope, "ruling", c.Label))
	}
	for _, c := range md.Subjects {
		out = append(out, g("pg_review_subjects", float64(c.N), "lens", c.Lens, "scope", c.Scope, "type", c.Label))
	}
	if len(md.OutboxFailed) == 0 {
		out = append(out, g("pg_review_outbox_failed", 0))
	}
	for _, c := range md.OutboxFailed {
		out = append(out, g("pg_review_outbox_failed", float64(c.N), "lens", c.Lens, "scope", c.Scope))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Emit reads the ledger and pushes the gauges through Default. It returns
// nothing: a failure at any step is logged at debug level and swallowed.
func Emit(ctx context.Context, l *ledger.Ledger, now time.Time) {
	defer func() { _ = recover() }()
	if l == nil {
		return
	}
	md, err := l.MetricsData(ctx)
	if err != nil {
		Default.debugf("collect failed: %v", err)
		return
	}
	Default.Push(ctx, Collect(md, now))
}
