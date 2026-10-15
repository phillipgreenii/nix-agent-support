package serve_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/telemetry"
)

// A settle that its dependents do not satisfy: the row is still appended at the
// deadline, the group is left stale (and still queued), and the result word says
// why. A governor pause ends as "timeout", a dependent fetch that failed as
// "failed". The metric is read in-process; the failing dependent is shown on the
// real binary through its log.

// settleResults sums the change.settle counter by its result label.
func settleResults(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != telemetry.MetricChangeSettle {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is a %T, want an int64 sum", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				result, _ := dp.Attributes.Value("result")
				out[result.AsString()] += dp.Value
			}
		}
	}
	return out
}

// requireNoToken fails when the fixture token is in anything the daemon wrote
// to its state directory (the store, its WAL, the event and structured logs and
// their rotations) or printed.
func requireNoToken(t *testing.T, d *servetest.Daemon, output string) {
	t.Helper()
	if hits := filesWith(t, d.StateDir, []byte(d.Token)); len(hits) != 0 {
		t.Errorf("the fixture token appears in %v", hits)
	}
	if strings.Contains(output, d.Token) {
		t.Errorf("the fixture token appears in the daemon's output: %s", output)
	}
}

func TestChangesSettle_AGovernorPauseTimesOutTheRowAppendsItAndIsCountedTimeout(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a, Config: settleConfig("3s")})
	reader := sdkmetric.NewManualReader()
	p := runInProcess(t, d, func(o *serve.Options) { o.MeterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)) })
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()})
	quiet(t, d)
	versionBefore := d.EntityVersion(t, "pr", prA.NodeID)
	callsBefore := d.Shim.CountKind("files") + d.Shim.CountKind("commits")

	// The governor refuses every GraphQL spend once GitHub reports less than the
	// reserve (rate_reserve_points, 1000) left: that, and not a rate-limit answer,
	// is a pause that REFUSES the dependents without trying them (a secondary
	// limit holds only the background classes, and the dependents are class 2:
	// they go out, meet the limit and fail, which is the other test). The head
	// moves in the same breath, so the very summary response that detects the
	// change also carries the reading, and the files and commits fetches it
	// queues are refused. (The runs group is a REST read in a bucket of its own,
	// which this reading does not close.)
	d.Shim.Remaining(500)
	changed := prA
	changed.Head = prAHead
	d.Shim.SetPRs(servetest.MineExpr, changed, prB, prC)
	d.Shim.SetPRs(servetest.TeamExpr, changed, prB)
	var pending servetest.PendingRow
	until(t, 60*time.Second, "the head change to be stored as a pending row", func() bool {
		rows := d.PendingRows(t)
		if len(rows) != 1 {
			if len(d.ChangeLog(t)) > 0 {
				t.Fatalf("the row was appended without waiting: %+v", d.ChangeLog(t))
			}
			return false
		}
		pending = rows[0]
		return true
	})
	if !slices.Contains(pending.Awaited, "files") || !slices.Contains(pending.Awaited, "commits") {
		t.Fatalf("the pending row = %+v, want it to await files and commits", pending)
	}

	// The refused dependents cannot be fetched: the row is appended at its deadline.
	until(t, 60*time.Second, "the row to be appended at its deadline", func() bool { return len(d.ChangeLog(t)) >= 1 })
	if now := time.Now(); now.Before(pending.Deadline.Add(-time.Second)) {
		t.Errorf("the row was appended at %s, long before its deadline %s", now, pending.Deadline)
	}
	rows := d.ChangeLog(t)
	if len(rows) != 1 {
		t.Fatalf("change log = %+v, want the one row", rows)
	}
	row := rows[0]
	if !slices.Contains(row.Kinds, "head_changed") || row.HeadSHA != prAHead {
		t.Errorf("the row = %+v, want head_changed at %s", row, prAHead)
	}
	if got := d.EntityVersion(t, "pr", prA.NodeID); got != versionBefore+1 || row.Version != got {
		t.Errorf("entity version = %d (row %d), want %d: moved once", got, row.Version, versionBefore+1)
	}
	if left := d.PendingRows(t); len(left) != 0 {
		t.Errorf("pending rows after the deadline: %+v", left)
	}
	if got := d.Shim.CountKind("files") + d.Shim.CountKind("commits"); got != callsBefore {
		t.Errorf("%d files or commits calls reached the origin under the pause, want none", got-callsBefore)
	}
	// The groups were left stale, as the store shows it, without an error: the
	// budget refused them, the origin never failed them.
	for _, g := range []string{"files", "commits"} {
		st := d.Group(t, prA.NodeID, g)
		if !st.Stale(time.Now()) || st.LastError != "" {
			t.Errorf("group %s after the timeout = %+v, want stale with no last_error", g, st)
		}
	}

	// The result word is "timeout", once.
	until(t, 10*time.Second, "change.settle{result=timeout}", func() bool { return settleResults(t, reader)["timeout"] == 1 })
	got := settleResults(t, reader)
	if got["timeout"] != 1 || got["settled"] != 0 || got["failed"] != 0 {
		t.Errorf("change.settle by result = %v, want exactly one timeout", got)
	}
	if ended := logLines(d.DaemonLog(t), "settle_ended"); len(ended) != 1 || ended[0]["result"] != "timeout" {
		t.Errorf("settle_ended lines = %v, want one with result timeout", ended)
	}
	if code := p.stop(t); code != serve.ExitOK {
		t.Fatalf("the drain exited %d", code)
	}
	requireNoToken(t, d, "")
}

func TestChangesSettle_AFailingDependentAppendsTheRowAtTheDeadlineWithTheGroupLeftStale(t *testing.T) {
	t.Parallel()
	// A failed fetch is retried after a second, so the group's later refresh is
	// within the test's reach once the origin recovers.
	d := startFilled(t, servetest.Options{Config: settleConfig("3s"), Hooks: "retry_delay=1s"})
	versionBefore := d.EntityVersion(t, "pr", prA.NodeID)

	// The runs fetch answers 502 for the next ten calls; the other dependents
	// (files and commits) are served.
	d.Shim.Fail("runs", 502, 10)
	armHeadChange(t, d)
	d.Shim.Release("runs") // the rest of armHeadChange's hold: the failure is the point
	var pending servetest.PendingRow
	until(t, 60*time.Second, "the head change to be stored as a pending row", func() bool {
		rows := d.PendingRows(t)
		if len(rows) != 1 {
			return false
		}
		pending = rows[0]
		return true
	})
	if !slices.Contains(pending.Kinds, "head_changed") {
		t.Fatalf("the pending row = %+v", pending)
	}
	until(t, 60*time.Second, "the row to be appended", func() bool { return len(d.ChangeLog(t)) >= 1 })
	rows := d.ChangeLog(t)
	if len(rows) != 1 {
		t.Fatalf("change log = %+v, want the one row", rows)
	}
	row := rows[0]
	if !slices.Contains(row.Kinds, "head_changed") || slices.Contains(row.Kinds, "ci_changed") {
		t.Errorf("the row = %+v, want head_changed without ci_changed (the runs fetch failed)", row)
	}
	if got := d.EntityVersion(t, "pr", prA.NodeID); got != versionBefore+1 || row.Version != got {
		t.Errorf("entity version = %d (row %d), want %d: moved once", got, row.Version, versionBefore+1)
	}
	ended := logLines(d.DaemonLog(t), "settle_ended")
	if len(ended) != 1 || ended[0]["result"] != "failed" {
		t.Errorf("settle_ended lines = %v, want one with result failed", ended)
	}
	if runs := d.Group(t, prA.NodeID, "runs"); !runs.Stale(time.Now()) {
		t.Errorf("the runs group is fresh although its fetch failed: %+v", runs)
	}

	// The group stays queued: once the origin recovers its refresh succeeds and
	// appends its own row.
	d.Shim.Remove("fail.runs")
	until(t, 60*time.Second, "the group's own row after the origin recovered", func() bool { return len(d.ChangeLog(t)) >= 2 })
	quiet(t, d)
	all := d.ChangeLog(t)
	if len(all) != 2 || !slices.Contains(all[1].Kinds, "ci_changed") || all[1].Version != all[0].Version+1 {
		t.Errorf("change log = %+v, want the head row then the runs' own ci_changed row", all)
	}
	if code := servetest.StopDaemon(t, d); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	requireNoToken(t, d, d.Stderr())
}
