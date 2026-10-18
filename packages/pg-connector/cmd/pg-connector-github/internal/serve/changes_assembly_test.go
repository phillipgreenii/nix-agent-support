package serve_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/explain"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ops/status"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/serve"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/servetest"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/telemetry"
)

// The change engine assembled in the daemon: a summary change waits for its
// dependents, then one row is delivered and acknowledged; a ci_changed row
// reaches the pr feed and a consumer that holds only the ci key; the live
// status and explain carry the engine's fields; the change metrics are
// reported; a drain appends the pending rows; and a start recovers them.

// headChangeConfig makes the summary re-read every second (a head change is
// seen at once) and keeps the settle barrier open far longer than any test, so
// only a refreshed dependent (or a drain, or a recovery) ends a pending row.
var headChangeConfig = map[string]any{
	"summary_ttl": "1s", "terminal_ttl": "1s", "detail_ttl": "6s", "detail_max_age": "1h",
	"conversation_ttl": "6s", "conversation_max_age": "1h", "settle_timeout": "5m",
}

// prAHead is the head sha prA moves to; newRun is the failing run it brings.
const prAHead = "head-3-new"

// armHeadChange arms the origin for a head change of prA that its runs group
// would show as a new failing run, and holds the runs fetch so the change stays
// pending. Files and commits stay fast, so only the runs group is awaited when
// the pending row is checked.
func armHeadChange(t *testing.T, d *servetest.Daemon) {
	t.Helper()
	d.Shim.Block("runs", 4)
	d.Shim.SetRuns(prA.Repo, "feature-3",
		servetest.Run{ID: 3001, Name: "ci", Status: "completed", Conclusion: "failure", Head: "head-3"},
		servetest.Run{ID: 3002, Name: "lint", Status: "completed", Conclusion: "success", Head: "head-3"},
		servetest.Run{ID: 3003, Name: "ci", Status: "completed", Conclusion: "failure", Head: prAHead})
	d.Shim.SetJobs(3003, servetest.Job{ID: 70030, Name: "build", Conclusion: "failure"})
	changed := prA
	changed.Head = prAHead
	d.Shim.SetPRs(servetest.MineExpr, changed, prB, prC)
	d.Shim.SetPRs(servetest.TeamExpr, changed, prB)
}

// awaitPendingRow waits until the runs fetch is held and the head change has
// been detected as a pending row.
func awaitPendingRow(t *testing.T, d *servetest.Daemon) {
	t.Helper()
	until(t, 60*time.Second, "the runs fetch to be held and the head change to be pending", func() bool {
		return d.Shim.Held("runs") >= 1 && countRows(t, d, "pending_change") == 1
	})
}

// metricNames returns the set of metric names the reader reports.
func metricNames(t *testing.T, reader *sdkmetric.ManualReader) map[string]bool {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = true
		}
	}
	return out
}

// gaugeValue is the sum of the data points of an int64 gauge (0 when absent).
func gaugeValue(t *testing.T, reader *sdkmetric.ManualReader, name string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if g, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == name {
				for _, dp := range g.DataPoints {
					total += dp.Value
				}
			}
		}
	}
	return total
}

type storedChange struct {
	Seq     int64
	Version int64
	Head    string
	Kinds   []string
}

// storedChanges reads the change log.
func storedChanges(t *testing.T, d *servetest.Daemon) []storedChange {
	t.Helper()
	rows, err := d.OpenStore(t).Query(`SELECT seq, version, head_sha, kinds FROM change_log ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []storedChange
	for rows.Next() {
		var c storedChange
		var kinds string
		if err := rows.Scan(&c.Seq, &c.Version, &c.Head, &kinds); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(kinds), &c.Kinds); err != nil {
			t.Fatalf("change_log kinds %q: %v", kinds, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestChangesAssembly_ASummaryChangeWaitsForItsDependentsThenIsDeliveredOnceAndAcknowledged(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a, Config: headChangeConfig})
	reader := sdkmetric.NewManualReader()
	p := runInProcess(t, d, func(o *serve.Options) { o.MeterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)) })
	defer p.stop(t)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	// Fill files and commits so a head change has them to re-pull.
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()})
	quiet(t, d)

	// A pr consumer and a consumer that holds only the ci key, both at the tail.
	if got := pollPR(t, d, "pr-reader", servetest.QueryMine); len(got.Changes) != 0 {
		t.Fatalf("the first pr poll answered %+v", got.Changes)
	}
	if got := pollCI(t, d, "ci-reader", servetest.QueryMine); len(got.Changes) != 0 {
		t.Fatalf("the first ci poll answered %+v", got.Changes)
	}
	if n := countRows(t, d, "change_log"); n != 0 {
		t.Fatalf("the baseline and the first polls appended %d rows", n)
	}

	// The head moves while the runs fetch is slow: the change is detected and
	// stays pending; the feed shows nothing, and the live reports show the
	// pending row.
	armHeadChange(t, d)
	awaitPendingRow(t, d)
	if n := countRows(t, d, "change_log"); n != 0 {
		t.Fatalf("a row was appended while a dependent was still slow (%d rows)", n)
	}
	if got := pollPR(t, d, "pr-reader", servetest.QueryMine); len(got.Changes) != 0 {
		t.Errorf("a pending row was delivered: %+v", got.Changes)
	}
	ex := decode[explain.Result](t, callOK(t, d, "pr", "explain", map[string]any{"id": prA.Ref()}))
	if ex.PendingSettle == nil {
		t.Fatal("explain shows no pending_settle while a row is pending")
	}
	awaited := map[string]bool{}
	for _, g := range ex.PendingSettle.Awaited {
		awaited[g.Group] = true
	}
	if !awaited["runs"] || !slices.Contains(ex.PendingSettle.Kinds, "head_changed") {
		t.Errorf("explain pending_settle = %+v, want the runs group awaited and head_changed among its kinds", ex.PendingSettle)
	}
	rep := decode[status.Report](t, callOK(t, d, "pr", "status", nil))
	if rep.Changes == nil || len(rep.Changes.PendingRows) != 1 || rep.Changes.PendingRows[0].ID != prA.Ref() {
		t.Fatalf("status changes = %+v, want one pending row for %s", rep.Changes, prA.Ref())
	}
	if !slices.Contains(rep.Changes.PendingRows[0].Awaited, "runs") {
		t.Errorf("status pending row awaits %v, want runs", rep.Changes.PendingRows[0].Awaited)
	}
	until(t, 10*time.Second, "change.settle_pending to report the pending row", func() bool {
		return gaugeValue(t, reader, telemetry.MetricChangeSettlePending) == 1
	})

	// The slow dependent finishes: exactly one row is appended.
	d.Shim.Release("runs")
	until(t, 60*time.Second, "the settled row", func() bool { return countRows(t, d, "change_log") >= 1 })
	quiet(t, d)
	rows := storedChanges(t, d)
	if len(rows) != 1 {
		t.Fatalf("the change appended %d rows, want one: %+v", len(rows), rows)
	}
	row := rows[0]
	for _, k := range []string{"head_changed", "ci_changed"} {
		if !slices.Contains(row.Kinds, k) {
			t.Errorf("row kinds = %v, want %s among them", row.Kinds, k)
		}
	}
	if row.Head != prAHead || row.Version <= 0 {
		t.Errorf("row = %+v, want head %s and a version", row, prAHead)
	}
	if n := countRows(t, d, "pending_change"); n != 0 {
		t.Errorf("%d pending rows remain after the settle", n)
	}

	// The row is delivered with its own version and head, and delivered again
	// until it is acknowledged.
	first := pollPR(t, d, "pr-reader", servetest.QueryMine)
	if len(first.Changes) != 1 {
		t.Fatalf("the poll answered %d changes, want the one row: %+v", len(first.Changes), first.Changes)
	}
	ch := first.Changes[0]
	if ch.Seq != row.Seq || ch.Entity.ID != prA.Ref() || ch.Entity.Version != row.Version || ch.Entity.HeadSHA != row.Head || ch.Change != "changed" {
		t.Errorf("delivered %+v, want seq %d, %s, version %d, head %s, changed", ch, row.Seq, prA.Ref(), row.Version, row.Head)
	}
	again := pollPR(t, d, "pr-reader", servetest.QueryMine)
	if len(again.Changes) != 1 || again.Changes[0].Seq != row.Seq {
		t.Errorf("a second poll before the ack answered %+v, want the same row again", again.Changes)
	}
	next := sourceNext(t, again.Sources)
	if ack := ackChanges(t, d, "pr", "pr-reader", servetest.QueryMine, next); ack.AckedSeq != next {
		t.Errorf("acked_seq = %d, want %d", ack.AckedSeq, next)
	}
	if after := pollPR(t, d, "pr-reader", servetest.QueryMine); len(after.Changes) != 0 {
		t.Errorf("a poll after the ack answered %+v", after.Changes)
	}

	// The ci_changed row is also the ci feed's: the consumer that holds only the
	// ci key gets it, with the same version.
	ci := pollCI(t, d, "ci-reader", servetest.QueryMine)
	if len(ci.Changes) != 1 {
		t.Fatalf("the ci poll answered %d changes, want the ci_changed row: %+v", len(ci.Changes), ci.Changes)
	}
	if c := ci.Changes[0]; c.Seq != row.Seq || c.Entity.PRID != prA.Ref() || c.Entity.Version != row.Version || !slices.Contains(c.Kinds, "ci_changed") {
		t.Errorf("ci change %+v, want seq %d, %s, version %d and ci_changed", c, row.Seq, prA.Ref(), row.Version)
	}

	// The metrics of the engine are reported.
	want := []string{
		telemetry.MetricChangeSettle, telemetry.MetricChangeSettlePending, telemetry.MetricChangeDetectionDelay,
		telemetry.MetricConsumerLagRows, telemetry.MetricConsumerRedeliveries, telemetry.MetricQueryLastSuccessAge,
	}
	until(t, 10*time.Second, "the change metrics to be reported", func() bool {
		got := metricNames(t, reader)
		for _, name := range want {
			if !got[name] {
				return false
			}
		}
		return true
	})
}

func TestChangesAssembly_ADrainAppendsEveryPendingRowBeforeTheProcessExits(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a, Config: headChangeConfig})
	p := runInProcess(t, d, nil)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine, servetest.QueryTeam)
	callOK(t, d, "pr", "files", map[string]any{"id": prA.Ref()})
	callOK(t, d, "pr", "commits", map[string]any{"id": prA.Ref()})
	quiet(t, d)

	armHeadChange(t, d)
	awaitPendingRow(t, d)
	if n := countRows(t, d, "change_log"); n != 0 {
		t.Fatalf("a row was appended before the drain (%d rows)", n)
	}

	// SIGTERM while a dependent is still slow: the row is appended before the
	// daemon exits, not lost with the process.
	if code := p.stop(t); code != serve.ExitOK {
		t.Fatalf("the drain exited %d", code)
	}
	rows := storedChanges(t, d)
	if len(rows) != 1 || !slices.Contains(rows[0].Kinds, "head_changed") {
		t.Fatalf("change log after the drain = %+v, want the one head_changed row", rows)
	}
	if n := countRows(t, d, "pending_change"); n != 0 {
		t.Errorf("%d pending rows remain after the drain", n)
	}
}

func TestChangesAssembly_AStartRequeuesTheGroupsAStoredPendingRowAwaitsAndSettlesIt(t *testing.T) {
	d := servetest.NewDaemon(t, servetest.Options{Before: seed3a})
	p := runInProcess(t, d, nil)
	settle(t, d)
	awaitBaselines(t, d, servetest.QueryMine)
	if code := p.stop(t); code != serve.ExitOK {
		t.Fatalf("the first drain exited %d", code)
	}

	// A pending row the previous run left behind, awaiting the runs group, with a
	// deadline far ahead: only the start's re-queue of the group can settle it (the
	// runs group is fresh, so nothing else would fetch it).
	detected := time.Now().Add(-time.Minute).UnixMilli()
	deadline := time.Now().Add(time.Hour).UnixMilli()
	awaited := `[{"group":"runs","since":"` + time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) + `"}]`
	db := d.WritableStore(t)
	if _, err := db.Exec(`INSERT INTO pending_change (kind, id, kinds, fields, awaited, head_sha, base_sha, origin, detected_at, deadline)
		VALUES ('pr', ?, '["head_changed"]', '[]', ?, 'head-3', 'base-1', 'refresh', ?, ?)`, prA.NodeID, awaited, detected, deadline); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	p2 := runInProcess(t, d, nil)
	defer p2.stop(t)
	until(t, 30*time.Second, "the re-queued group to settle the stored pending row", func() bool {
		return countRows(t, d, "pending_change") == 0 && countRows(t, d, "change_log") == 1
	})
	if rows := storedChanges(t, d); len(rows) != 1 {
		t.Errorf("change log = %+v, want the settled row", rows)
	}
}
