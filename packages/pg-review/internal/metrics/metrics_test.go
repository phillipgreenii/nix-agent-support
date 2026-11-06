package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/internal/ledger"
)

var ctx = context.Background()

func openLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	l, err := ledger.OpenPath(ctx, filepath.Join(t.TempDir(), "state", "pg-review", "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

var t0 = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// addRun inserts a terminal run of a synthetic lens and scope.
func addRun(t *testing.T, l *ledger.Ledger, lens, scope, schedule, state string, windowEnd time.Time, window time.Duration, filed int) {
	t.Helper()
	err := l.WithTx(ctx, func(tx *ledger.Tx) error {
		_, err := tx.InsertRun(ctx, ledger.Run{
			Lens: lens, Scope: scope, ScheduleID: schedule, State: state, PID: 1,
			WindowFrom: windowEnd.Add(-window).Format(time.RFC3339Nano), WindowTo: windowEnd.Format(time.RFC3339Nano),
			EndedAt: t0, ItemsFiled: filed,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func collect(t *testing.T, l *ledger.Ledger) []Gauge {
	t.Helper()
	md, err := l.MetricsData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return Collect(md, t0.Add(time.Hour))
}

func find(gs []Gauge, name string, kv ...string) (Gauge, bool) {
next:
	for _, g := range gs {
		if g.Name != name {
			continue
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if g.Labels[kv[i]] != kv[i+1] {
				continue next
			}
		}
		return g, true
	}
	return Gauge{}, false
}

func catalogFile(t *testing.T) []Def {
	t.Helper()
	b, err := os.ReadFile("../../metrics.catalog")
	if err != nil {
		t.Fatal(err)
	}
	var out []Def
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			t.Fatalf("catalog line %q is not <name> <type> <labels>", line)
		}
		out = append(out, Def{Name: f[0], Type: f[1], Labels: strings.Split(f[2], ",")})
	}
	return out
}

func TestCatalogFileAgreesWithCode(t *testing.T) {
	file := catalogFile(t)
	if len(file) != len(Catalog) {
		t.Fatalf("catalog file has %d lines, code has %d", len(file), len(Catalog))
	}
	for i, d := range Catalog {
		f := file[i]
		if f.Name != d.Name || f.Type != d.Type || strings.Join(f.Labels, ",") != strings.Join(d.Labels, ",") {
			t.Errorf("line %d: file %+v, code %+v", i+1, f, d)
		}
		for _, bad := range []string{"_total", "_bucket", "_sum", "_count"} {
			if strings.HasSuffix(d.Name, bad) {
				t.Errorf("%s has suffix %s", d.Name, bad)
			}
		}
		if d.Type != "gauge" {
			t.Errorf("%s is %s; every metric is a gauge", d.Name, d.Type)
		}
	}
}

// TestEveryEmittedGaugeIsInTheCatalog fills every table and checks that the
// names and label keys Collect emits are exactly the catalog's.
func TestEveryEmittedGaugeIsInTheCatalog(t *testing.T) {
	l := openLedger(t)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFiled, t0, 24*time.Hour, 2)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFailed, t0, 24*time.Hour, 0)
	seedTopic(t, l, "lens-a", "scope-a")
	gs := collect(t, l)
	defs := map[string]Def{}
	for _, d := range Catalog {
		defs[d.Name] = d
	}
	seen := map[string]bool{}
	for _, g := range gs {
		d, ok := defs[g.Name]
		if !ok {
			t.Errorf("gauge %s is not in the catalog", g.Name)
			continue
		}
		seen[g.Name] = true
		for k := range g.Labels {
			if !contains(d.Labels, k) {
				t.Errorf("%s carries label %s not in the catalog", g.Name, k)
			}
		}
	}
	for _, d := range Catalog {
		if !seen[d.Name] && d.Name != "pg_review_outbox_failed" && d.Name != "pg_review_decisions" {
			t.Errorf("catalog metric %s was never emitted", d.Name)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

const layout = "2006-01-02T15:04:05.000000000Z"

func seedTopic(t *testing.T, l *ledger.Ledger, lens, scope string) {
	t.Helper()
	at := t0.Format(layout)
	var runID int64
	err := l.WithTx(ctx, func(tx *ledger.Tx) error {
		run, err := tx.InsertRun(ctx, ledger.Run{Lens: lens, Scope: scope + "-x", PID: 1})
		runID = run.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO subject(defined_in,key,type,first_seen,last_seen) VALUES('a.md','s1','file',?,?)`, []any{at, at}},
		{`INSERT INTO topic(topic_id,defined_in,subject_key,kind,lens,state,seq,created_at,updated_at)
			VALUES('t1','a.md','s1','stale',?,'Pending',1000,?,?)`, []any{lens, at, at}},
		{`INSERT INTO occurrence(topic_id,run_id,scope,lens,created_at) VALUES('t1',?,?,?,?)`, []any{runID, scope, lens, at}},
	} {
		if _, err := l.DB().Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPendingAndOldestAge(t *testing.T) {
	l := openLedger(t)
	seedTopic(t, l, "lens-a", "scope-a")
	gs := collect(t, l)
	g, ok := find(gs, "pg_review_pending_items", "lens", "lens-a", "scope", "scope-a")
	if !ok || g.Value != 1 {
		t.Fatalf("pending: %+v %v", g, ok)
	}
	g, ok = find(gs, "pg_review_oldest_pending_age_seconds", "lens", "lens-a")
	if !ok || g.Value != 3600 {
		t.Fatalf("age: %+v %v", g, ok)
	}
	if g, ok := find(gs, "pg_review_topics", "state", "Pending"); !ok || g.Value != 1 {
		t.Fatalf("topics: %+v %v", g, ok)
	}
	if g, ok := find(gs, "pg_review_subjects", "type", "file", "lens", "lens-a"); !ok || g.Value != 1 {
		t.Fatalf("subjects: %+v %v", g, ok)
	}
}

func TestEmptyLedgerEmitsZeroPending(t *testing.T) {
	gs := collect(t, openLedger(t))
	if g, ok := find(gs, "pg_review_pending_items"); !ok || g.Value != 0 {
		t.Fatalf("%+v %v", g, ok)
	}
	if _, ok := find(gs, "pg_review_last_success_timestamp_seconds"); ok {
		t.Error("a ledger with no run has a last-success series")
	}
}

func TestRunsCountedByOutcomeAndLatestRun(t *testing.T) {
	l := openLedger(t)
	addRun(t, l, "lens-a", "scope-a", "", ledger.RunFiled, t0, time.Hour, 3)
	addRun(t, l, "lens-a", "scope-a", "", ledger.RunSkipped, t0, time.Hour, 0)
	addRun(t, l, "lens-a", "scope-a", "", ledger.RunFailed, t0, time.Hour, 0)
	addRun(t, l, "lens-a", "scope-a", "", ledger.RunFailed, t0, time.Hour, 0)
	addRun(t, l, "lens-a", "scope-a", "", ledger.RunRunning, t0, time.Hour, 0)
	gs := collect(t, l)
	for outcome, want := range map[string]float64{"filed": 1, "skipped": 1, "failed": 2} {
		if g, ok := find(gs, "pg_review_runs", "outcome", outcome); !ok || g.Value != want {
			t.Errorf("%s: %+v %v", outcome, g, ok)
		}
	}
	if _, ok := find(gs, "pg_review_runs", "outcome", "running"); ok {
		t.Error("a Running run is counted")
	}
	// the latest terminal run is the last Failed one; findings come from the Filed run.
	if g, ok := find(gs, "pg_review_findings_last_run"); !ok || g.Value != 3 {
		t.Errorf("findings: %+v %v", g, ok)
	}
	if _, ok := find(gs, "pg_review_run_duration_seconds", "lens", "lens-a"); !ok {
		t.Error("no duration gauge")
	}
}

func TestScheduleGauges(t *testing.T) {
	l := openLedger(t)
	end := t0.Add(-2 * time.Hour)
	// daily: filed, then two failures and a skip (a skip is neither counted nor a reset).
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFiled, end, 24*time.Hour, 1)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFailed, t0, 24*time.Hour, 0)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunSkipped, t0, 24*time.Hour, 0)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFailed, t0, 24*time.Hour, 0)
	// weekly of the same lens and scope: filed, nothing after.
	addRun(t, l, "lens-a", "scope-a", "weekly", ledger.RunFiled, end.Add(-time.Hour), 7*24*time.Hour, 1)
	// manual runs (no schedule id): a Filed and a Failed one.
	addRun(t, l, "lens-a", "scope-a", "", ledger.RunFiled, t0, time.Hour, 1)
	addRun(t, l, "lens-a", "scope-a", "", ledger.RunFailed, t0, time.Hour, 0)
	// a schedule that only ever failed.
	addRun(t, l, "lens-b", "scope-a", "daily", ledger.RunFailed, t0, time.Hour, 0)
	gs := collect(t, l)

	if g, ok := find(gs, "pg_review_consecutive_failed_runs", "lens", "lens-a", "schedule", "daily"); !ok || g.Value != 2 {
		t.Errorf("daily failures: %+v %v", g, ok)
	}
	if g, ok := find(gs, "pg_review_consecutive_failed_runs", "lens", "lens-a", "schedule", "weekly"); !ok || g.Value != 0 {
		t.Errorf("weekly failures: %+v %v", g, ok)
	}
	if g, ok := find(gs, "pg_review_last_success_timestamp_seconds", "lens", "lens-a", "schedule", "daily"); !ok || g.Value != float64(end.Unix()) {
		t.Errorf("daily last success: %+v %v", g, ok)
	}
	if g, ok := find(gs, "pg_review_last_window_seconds", "lens", "lens-a", "schedule", "weekly"); !ok || g.Value != 7*24*3600 {
		t.Errorf("weekly window: %+v %v", g, ok)
	}
	if g, ok := find(gs, "pg_review_consecutive_failed_runs", "lens", "lens-b"); !ok || g.Value != 1 {
		t.Errorf("never-filed schedule failures: %+v %v", g, ok)
	}
	if _, ok := find(gs, "pg_review_last_success_timestamp_seconds", "lens", "lens-b"); ok {
		t.Error("a schedule that never filed has a last-success series")
	}
	for _, g := range gs {
		if strings.Contains(g.Name, "last_success") || strings.Contains(g.Name, "consecutive") || strings.Contains(g.Name, "last_window") {
			if g.Labels["schedule"] == "" || g.Labels["schedule"] == "manual" {
				t.Errorf("%s has a series without a schedule id: %v", g.Name, g.Labels)
			}
		}
	}
}

func TestFiledRunResetsConsecutiveFailures(t *testing.T) {
	l := openLedger(t)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFailed, t0, time.Hour, 0)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFailed, t0, time.Hour, 0)
	addRun(t, l, "lens-a", "scope-a", "daily", ledger.RunFiled, t0, time.Hour, 1)
	if g, ok := find(collect(t, l), "pg_review_consecutive_failed_runs", "schedule", "daily"); !ok || g.Value != 0 {
		t.Fatalf("%+v %v", g, ok)
	}
}
