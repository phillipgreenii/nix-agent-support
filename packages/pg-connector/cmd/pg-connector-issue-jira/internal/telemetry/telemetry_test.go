package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/origin"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/summary"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/engine"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/feed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/store"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t      *testing.T
	tel    *Telemetry
	reader *sdkmetric.ManualReader
	spans  *tracetest.SpanRecorder
	clock  *governor.ManualClock
	gov    *governor.Governor
	sched  *scheduler.Scheduler
	rt     *router.Router
	st     *store.Store
}

// newFixture builds a Telemetry with EVERY source set over a real store, a real
// governor with the daemon's one declared bucket (cap 10) and a real scheduler.
// mutate adjusts the options before New.
func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	f := &fixture{t: t}
	f.clock = governor.NewManualClock(t0)
	var err error
	f.gov, err = governor.New(governor.Config{
		Declared: []governor.BucketSpec{{
			Bucket: origin.Bucket, Unit: "requests",
			Limits: governor.Limits{Cap: 10, Window: time.Second},
		}},
		BackgroundShare: 0.9,
	}, governor.Options{Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	f.sched = scheduler.New(scheduler.Options{Clock: f.clock})
	f.st, _, err = store.Open(filepath.Join(t.TempDir(), "store.db"), store.Options{Synchronous: "OFF"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })

	f.reader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))
	f.spans = tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(f.spans))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()); _ = tp.Shutdown(context.Background()) })

	f.rt = router.New(router.Options{Instances: []string{"main"}, Now: f.clock.Now})
	opts := Options{
		MeterProvider:  mp,
		TracerProvider: tp,
		Governor:       f.gov.State,
		Scheduler:      f.sched.State,
		KnownOp:        func(_, op string) bool { return op == "list" || op == "show" },
		StorePath:      filepath.Join(t.TempDir(), "absent.db"),
		Now:            f.clock.Now,
		Started:        t0,
		Tracked: func(context.Context) (summary.Counts, error) {
			return summary.Counts{Members: 4, OtherOpen: 2, ClosedGrace: 1, Watched: 3}, nil
		},
		RowsWithoutKind: func(ctx context.Context) (int, error) { return CountRowsWithoutKind(ctx, f.st) },
		Pending:         func(ctx context.Context) (int, error) { return f.st.CountPending(ctx) },
		Consumers: fixedPositions(
			feed.Position{Consumer: "desk", Scope: "mine", LagRows: 4, OldestUnacked: t0.Add(-90 * time.Second)},
			feed.Position{Consumer: "router", Scope: "adhoc?assignee=someone", LagRows: 2, OldestUnacked: t0.Add(-10 * time.Second)},
		),
		Queries: func(ctx context.Context) ([]store.QueryRow, error) {
			return []store.QueryRow{{Key: "mine", LastSuccessAt: t0.Add(-time.Minute)}}, nil
		},
		ConfiguredQueries: func() []string { return []string{"mine"} },
		Watched:           func(context.Context) (int, error) { return 3, nil },
	}
	if mutate != nil {
		mutate(&opts)
	}
	f.tel, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.tel.Close() })
	f.rt.Use(f.tel.Middleware())
	return f
}

func fixedPositions(ps ...feed.Position) func(context.Context) ([]feed.Position, error) {
	return func(context.Context) ([]feed.Position, error) { return ps, nil }
}

func (f *fixture) collect() map[string]metricdata.Metrics {
	f.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := f.reader.Collect(context.Background(), &rm); err != nil {
		f.t.Fatal(err)
	}
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func (f *fixture) metric(name string) metricdata.Metrics {
	f.t.Helper()
	m, ok := f.collect()[name]
	if !ok {
		f.t.Fatalf("metric %s missing", name)
	}
	return m
}

// points returns label-string -> value for a gauge or a counter ("k=v,k=v", keys
// sorted), and the observation count for a histogram.
func points(t *testing.T, m metricdata.Metrics) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	add := func(s attribute.Set, v float64) { out[setString(s)] = v }
	switch d := m.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, p := range d.DataPoints {
			add(p.Attributes, float64(p.Value))
		}
	case metricdata.Gauge[float64]:
		for _, p := range d.DataPoints {
			add(p.Attributes, p.Value)
		}
	case metricdata.Sum[int64]:
		for _, p := range d.DataPoints {
			add(p.Attributes, float64(p.Value))
		}
	case metricdata.Histogram[float64]:
		for _, p := range d.DataPoints {
			add(p.Attributes, float64(p.Count))
		}
	default:
		t.Fatalf("%s: unexpected data type %T", m.Name, m.Data)
	}
	return out
}

func labelSets(t *testing.T, m metricdata.Metrics) []string {
	t.Helper()
	var out []string
	for k := range points(t, m) {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func setString(s attribute.Set) string {
	var kv []string
	for _, a := range s.ToSlice() {
		kv = append(kv, string(a.Key)+"="+a.Value.String())
	}
	return strings.Join(kv, ",")
}

func (f *fixture) register(op string, h func(ctx context.Context)) {
	f.rt.Register("main", op, scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		h(ctx)
		return map[string]any{}, nil
	}})
}

func (f *fixture) call(op string) scriptout.Response {
	return f.rt.Dispatch(context.Background(), router.Call{
		Instance: "main",
		Request:  scriptout.Request{Op: op, Args: json.RawMessage(`{}`)},
	})
}

// exerciseEverything drives every instrument at least once.
func (f *fixture) exerciseEverything() {
	f.t.Helper()
	f.register("list", func(ctx context.Context) { router.Annotate(ctx, "store", false, 3, 0) })
	if resp := f.call("list"); resp.Error != nil {
		f.t.Fatal(resp.Error)
	}
	f.tel.Request(origin.ResultOK, time.Millisecond)
	f.tel.Served("summary", 3*time.Second)
	f.tel.RecordEvictions(2)
	ob := f.tel.ChangeObserver()
	ob.Detected("closed", engine.OriginQuery, time.Minute)
	ob.Settled(engine.ResultSettled)
	f.collect() // a poll teaches the stored consumer names
	f.tel.FeedObserver().Redelivered("desk", "mine", 2)
	f.admitOne()
	if err := f.sched.Submit(scheduler.Task{
		Entity: "e1", Group: "summary", Class: governor.ClassQuery, Due: f.clock.Now().Add(-time.Minute),
	}); err != nil {
		f.t.Fatal(err)
	}
}

// admitOne spends one request from the declared bucket.
func (f *fixture) admitOne() {
	f.t.Helper()
	tk, err := f.gov.Admit(governor.Request{Bucket: origin.Bucket, Class: governor.ClassInteractive, Cost: 1})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := tk.Settle(governor.Spend{
		Bucket: origin.Bucket, Class: governor.ClassInteractive, Purpose: governor.PurposeInteractive,
		Cost: 1, Outcome: "ok",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func TestEveryInstrumentEmitsUnderItsDocumentedLabelSet(t *testing.T) {
	f := newFixture(t, nil)
	f.exerciseEverything()
	got := f.collect()

	allClasses := []string{"class=fill", "class=interactive", "class=invalidated", "class=keepalive", "class=query"}
	want := map[string][]string{
		MetricDaemonUp:              {""},
		MetricGovernorPaused:        {"reason=none"},
		MetricJiraRequests:          {"result=ok"},
		MetricBudgetRemaining:       {""},
		MetricQueueDepth:            allClasses,
		MetricQueueOldestOverdue:    allClasses,
		MetricGroupStaleness:        {"group=summary"},
		MetricRequestDuration:       {"op=list,served_from=store"},
		MetricTrackedEntities:       {"class=closed_grace", "class=members", "class=other_open"},
		MetricStoreBytes:            {""},
		MetricStoreEvictions:        {""},
		MetricRowsWithoutKind:       {""},
		MetricChangeSettle:          {"result=settled"},
		MetricChangeSettlePending:   {""},
		MetricChangeDetectionDelay:  {"kind=closed,origin=query"},
		MetricConsumerLagRows:       {"consumer=desk,query=mine", "consumer=router,query=other"},
		MetricConsumerOldestUnacked: {"consumer=desk,query=mine", "consumer=router,query=other"},
		MetricConsumerRedeliveries:  {"consumer=desk,query=mine"},
		MetricQueryLastSuccessAge:   {"query=mine"},
		MetricWatchedEntities:       {""},
	}
	if len(got) != len(want) {
		var names []string
		for n := range got {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("metrics = %v, want exactly %d", names, len(want))
	}
	for name, labels := range want {
		m, ok := got[name]
		if !ok {
			t.Errorf("metric %s missing", name)
			continue
		}
		if g := labelSets(t, m); strings.Join(g, "|") != strings.Join(labels, "|") {
			t.Errorf("%s labels = %q, want %q", name, g, labels)
		}
		if strings.HasSuffix(name, "_total") {
			t.Errorf("%s ends in _total", name)
		}
	}
}

func TestPolledValuesFollowTheirSources(t *testing.T) {
	f := newFixture(t, nil)
	f.admitOne()
	got := f.collect()
	if v := points(t, got[MetricDaemonUp])[""]; v != 1 {
		t.Errorf("daemon.up = %v", v)
	}
	if v := points(t, got[MetricBudgetRemaining])[""]; v != 9 {
		t.Errorf("budget.remaining = %v, want 9 (cap 10 minus 1 spent)", v)
	}
	tracked := points(t, got[MetricTrackedEntities])
	for class, want := range map[string]float64{"members": 4, "other_open": 2, "closed_grace": 1} {
		if v := tracked["class="+class]; v != want {
			t.Errorf("tracked.entities{%s} = %v, want %v", class, v, want)
		}
	}
	if v := points(t, got[MetricWatchedEntities])[""]; v != 3 {
		t.Errorf("watched.entities = %v", v)
	}
	// the window is one second: after it the headroom is the whole cap again
	f.clock.Advance(2 * time.Second)
	if v := points(t, f.metric(MetricBudgetRemaining))[""]; v != 10 {
		t.Errorf("budget.remaining after the window = %v, want 10", v)
	}
}

func TestBudgetRemainingNeverGoesNegative(t *testing.T) {
	f := newFixture(t, nil)
	for range 12 {
		tk, err := f.gov.Admit(governor.Request{Bucket: origin.Bucket, Class: governor.ClassInteractive, Cost: 1})
		if err != nil {
			break
		}
		_ = tk.Settle(governor.Spend{Bucket: origin.Bucket, Class: governor.ClassInteractive, Cost: 1, Outcome: "ok"})
	}
	if v := points(t, f.metric(MetricBudgetRemaining))[""]; v < 0 {
		t.Errorf("budget.remaining = %v, want at least 0", v)
	}
}

func TestGovernorPausedIsOneWithAReducedReasonWhilePausedAndZeroAfter(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.gov.Pause(origin.Bucket, time.Minute, "rate limited (HTTP 429, retry after 1m0s)"); err != nil {
		t.Fatal(err)
	}
	p := points(t, f.metric(MetricGovernorPaused))
	if len(p) != 1 || p["reason=rate_limited"] != 1 {
		t.Errorf("paused = %v, want only reason=rate_limited at 1", p)
	}
	f.clock.Advance(2 * time.Minute)
	p = points(t, f.metric(MetricGovernorPaused))
	if len(p) != 1 || p["reason=none"] != 0 {
		t.Errorf("paused after the pause = %v, want only reason=none at 0", p)
	}
}

func TestReasonLabelIsBounded(t *testing.T) {
	for in, want := range map[string]string{
		"rate limited (retry after 1m0s)": "rate_limited",
		"  ":                              "other",
		"":                                "other",
		"Server:  Busy now":               "server",
		strings.Repeat("a", 100):          strings.Repeat("a", maxReasonLabelLen),
	} {
		if got := reasonLabel(in); got != want {
			t.Errorf("reasonLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestCountsEachResultAndFoldsAnythingElseToError(t *testing.T) {
	f := newFixture(t, nil)
	for _, r := range []string{
		origin.ResultOK, origin.ResultOK, origin.ResultError, origin.ResultRateLimited,
		origin.ResultUnauthenticated, origin.ResultRefused, origin.ResultNotFound, "PROJ-123 exploded",
	} {
		f.tel.Request(r, time.Second)
	}
	p := points(t, f.metric(MetricJiraRequests))
	want := map[string]float64{
		"result=ok": 2, "result=error": 2, "result=rate_limited": 1,
		"result=unauthenticated": 1, "result=refused": 1, "result=not_found": 1,
	}
	if len(p) != len(want) {
		t.Errorf("requests = %v, want %v", p, want)
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("requests{%s} = %v, want %v", k, p[k], v)
		}
	}
	var _ origin.Observer = f.tel // *Telemetry is passed to origin.Options.Observer as is
}

func TestOpIsUnknownForAnUnregisteredOp(t *testing.T) {
	f := newFixture(t, nil)
	f.register("list", func(ctx context.Context) { router.Annotate(ctx, "origin", false, 0, 0) })
	f.call("list")
	f.call("PROJ-123")
	f.call("")
	p := points(t, f.metric(MetricRequestDuration))
	for _, k := range []string{"op=list,served_from=origin"} {
		if p[k] != 1 {
			t.Errorf("request.duration %s count = %v, want 1 (%v)", k, p[k], p)
		}
	}
	if p["op=unknown,served_from=none"] != 2 {
		t.Errorf("unregistered ops = %v, want 2 under op=unknown,served_from=none (%v)", p["op=unknown,served_from=none"], p)
	}
	for k := range p {
		if strings.Contains(k, "PROJ") {
			t.Errorf("label set %q carries a client-supplied op", k)
		}
	}
}

func TestRequestDurationIsTheMeasuredDuration(t *testing.T) {
	f := newFixture(t, nil)
	f.register("show", func(ctx context.Context) {
		f.clock.Advance(250 * time.Millisecond)
		router.Annotate(ctx, "origin", false, 0, 0)
	})
	f.call("show")
	h, ok := f.metric(MetricRequestDuration).Data.(metricdata.Histogram[float64])
	if !ok || len(h.DataPoints) != 1 {
		t.Fatalf("request.duration = %#v", f.metric(MetricRequestDuration).Data)
	}
	if got := h.DataPoints[0].Sum; got < 0.249 || got > 0.251 {
		t.Errorf("duration sum = %v, want 0.25", got)
	}
}

func TestGroupStalenessFoldsAnUnknownGroupAndClampsNegative(t *testing.T) {
	f := newFixture(t, nil)
	f.tel.Served("summary", -5*time.Second)
	f.tel.Served("detail", time.Minute)
	f.tel.Served("PROJ-123", 0)
	f.tel.Revalidated("summary", "extended") // no series; must not panic
	p := points(t, f.metric(MetricGroupStaleness))
	want := map[string]float64{"group=summary": 1, "group=detail": 1, "group=unknown": 1}
	if len(p) != len(want) {
		t.Errorf("group.staleness = %v, want %v", p, want)
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("group.staleness{%s} = %v, want %v", k, p[k], v)
		}
	}
}

func TestQueueGaugesFollowTheSchedulerUnderAFakeClock(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.sched.Submit(scheduler.Task{
		Entity: "e1", Group: "summary", Class: governor.ClassQuery, Due: f.clock.Now().Add(-90 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if v := points(t, f.metric(MetricQueueDepth))["class=query"]; v != 1 {
		t.Errorf("queue.depth{query} = %v, want 1", v)
	}
	if v := points(t, f.metric(MetricQueueOldestOverdue))["class=query"]; v != 90 {
		t.Errorf("queue.oldest_overdue_seconds{query} = %v, want 90", v)
	}
}

func TestEvictionsAccumulateAndIgnoreNonPositive(t *testing.T) {
	f := newFixture(t, nil)
	f.tel.RecordEvictions(0)
	f.tel.RecordEvictions(-3)
	f.tel.RecordEvictions(2)
	f.tel.RecordEvictions(3)
	if v := points(t, f.metric(MetricStoreEvictions))[""]; v != 5 {
		t.Errorf("store.evictions = %v, want 5", v)
	}
}

func TestStoreBytesSumsTheStoreFileItsWalAndShm(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.StorePath = filepath.Join(t.TempDir(), "x.db") })
	if v := points(t, f.metric(MetricStoreBytes))[""]; v != 0 {
		t.Errorf("store.bytes of an absent store = %v, want 0", v)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "s.db")
	for p, n := range map[string]int{path: 100, path + "-wal": 20, path + "-shm": 3} {
		writeFile(t, p, n)
	}
	if got := storeSize(path); got != 123 {
		t.Errorf("storeSize = %d, want 123", got)
	}
}

func TestRowsWithoutKindIsZeroThenOneAfterAKindlessRowIsInserted(t *testing.T) {
	f := newFixture(t, nil)
	ctx := context.Background()
	if v := points(t, f.metric(MetricRowsWithoutKind))[""]; v != 0 {
		t.Fatalf("change.rows_without_kind on an empty log = %v, want 0", v)
	}
	appendRow := func(kinds []string) {
		t.Helper()
		err := f.st.InTx(ctx, func(tx *store.Tx) error {
			_, err := tx.AppendChange(ctx, store.ChangeRow{Kind: "issue", ID: "PROJ-1", Kinds: kinds, Origin: engine.OriginQuery, At: t0})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendRow([]string{"closed"})
	if v := points(t, f.metric(MetricRowsWithoutKind))[""]; v != 0 {
		t.Errorf("change.rows_without_kind with only kinded rows = %v, want 0", v)
	}
	appendRow(nil)
	if v := points(t, f.metric(MetricRowsWithoutKind))[""]; v != 1 {
		t.Errorf("change.rows_without_kind with one kind-less row = %v, want 1", v)
	}
	appendRow([]string{})
	if n, err := CountRowsWithoutKind(ctx, f.st); err != nil || n != 2 {
		t.Errorf("CountRowsWithoutKind = %d, %v; want 2", n, err)
	}
}

func TestNewAcceptsNilSourcesAndLeavesTheirSeriesUnreported(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		*o = Options{MeterProvider: o.MeterProvider, TracerProvider: o.TracerProvider, Now: o.Now}
	})
	got := f.collect()
	for name := range got {
		if name != MetricDaemonUp {
			t.Errorf("metric %s reported with every source nil", name)
		}
	}
	if _, ok := got[MetricDaemonUp]; !ok {
		t.Error("daemon.up missing")
	}
}

func TestAFailingSourceSkipsOnlyItsOwnSeries(t *testing.T) {
	boom := errors.New("boom")
	f := newFixture(t, func(o *Options) {
		o.Tracked = func(context.Context) (summary.Counts, error) { return summary.Counts{}, boom }
		o.RowsWithoutKind = func(context.Context) (int, error) { return 0, boom }
	})
	got := f.collect()
	if _, ok := got[MetricTrackedEntities]; ok {
		t.Error("tracked.entities reported although its source failed")
	}
	if _, ok := got[MetricRowsWithoutKind]; ok {
		t.Error("change.rows_without_kind reported although its source failed")
	}
	if _, ok := got[MetricWatchedEntities]; !ok {
		t.Error("watched.entities missing: a failing source must skip only its own series")
	}
}

func TestChangeFoldsAreClosed(t *testing.T) {
	f := newFixture(t, nil)
	ob := f.tel.ChangeObserver()
	ob.Detected("closed", engine.OriginWrite, time.Second)
	ob.Detected("added", engine.OriginRefresh, -time.Second)
	ob.Detected("entered_query", "PROJ-1", time.Second) // not a stored kind of this daemon, bad origin
	ob.Settled(engine.ResultTimeout)
	ob.Settled("PROJ-1")
	if p := points(t, f.metric(MetricChangeDetectionDelay)); len(p) != 3 ||
		p["kind=closed,origin=write"] != 1 || p["kind=added,origin=refresh"] != 1 || p["kind=unknown,origin=unknown"] != 1 {
		t.Errorf("detection delay label sets = %v", p)
	}
	if p := points(t, f.metric(MetricChangeSettle)); len(p) != 2 || p["result=timeout"] != 1 || p["result=unknown"] != 1 {
		t.Errorf("settle label sets = %v", p)
	}
}

func TestRedeliveriesFoldUnknownConsumerAndUnconfiguredQueryToOther(t *testing.T) {
	f := newFixture(t, nil)
	f.collect() // a poll teaches the stored consumer names
	fo := f.tel.FeedObserver()
	fo.Redelivered("desk", "mine", 1)
	fo.Redelivered("PROJ-1", "mine", 2)
	fo.Redelivered("desk", "adhoc?assignee=someone", 4)
	fo.Redelivered("desk", "mine", 0)
	p := points(t, f.metric(MetricConsumerRedeliveries))
	want := map[string]float64{
		"consumer=desk,query=mine":  1,
		"consumer=other,query=mine": 2,
		"consumer=desk,query=other": 4,
	}
	if len(p) != len(want) {
		t.Errorf("redeliveries = %v, want %v", p, want)
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("redeliveries{%s} = %v, want %v", k, p[k], v)
		}
	}
}

func TestConsumerAndQueryGaugesFoldUnconfiguredQueries(t *testing.T) {
	f := newFixture(t, nil)
	lag := points(t, f.metric(MetricConsumerLagRows))
	if lag["consumer=desk,query=mine"] != 4 || lag["consumer=router,query=other"] != 2 || len(lag) != 2 {
		t.Errorf("consumer.lag_rows = %v", lag)
	}
	age := points(t, f.metric(MetricConsumerOldestUnacked))
	if age["consumer=desk,query=mine"] != 90 || age["consumer=router,query=other"] != 10 {
		t.Errorf("consumer.oldest_unacked_age_seconds = %v", age)
	}
	if q := points(t, f.metric(MetricQueryLastSuccessAge)); len(q) != 1 || q["query=mine"] != 60 {
		t.Errorf("query.last_success_age_seconds = %v, want only mine at 60", q)
	}
}

func TestNilTelemetryIsInert(t *testing.T) {
	var tel *Telemetry
	tel.ChangeObserver().Detected("closed", engine.OriginQuery, time.Second)
	tel.ChangeObserver().Settled(engine.ResultSettled)
	tel.FeedObserver().Redelivered("desk", "mine", 1)
	tel.Request(origin.ResultOK, time.Second)
	tel.OnSpend(governor.SpendEvent{Cost: 1})
	tel.RecordEvictions(1)
	tel.Served("summary", time.Second)
	tel.Revalidated("summary", "extended")
}

func TestOriginCallSpanIsAChildOfTheRequestSpan(t *testing.T) {
	f := newFixture(t, nil)
	f.register("show", func(ctx context.Context) {
		f.tel.RecordSpend(ctx, governor.SpendEvent{
			At: f.clock.Now(), Bucket: origin.Bucket, Class: governor.ClassQuery,
			Purpose: governor.PurposeQuery, Group: "summary", BatchSize: 2, Cost: 1, Outcome: "rate_limited",
		})
		router.Annotate(ctx, "origin", false, 0, 0)
	})
	f.call("show")
	ended := f.spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("spans = %d, want 2 (request and origin call)", len(ended))
	}
	var req, call sdktrace.ReadOnlySpan
	for _, s := range ended {
		switch s.Name() {
		case spanRequest:
			req = s
		case spanOriginCall:
			call = s
		}
	}
	if req == nil || call == nil {
		t.Fatalf("spans = %v", ended)
	}
	if call.Parent().SpanID() != req.SpanContext().SpanID() {
		t.Error("the origin-call span is not a child of the request span")
	}
	if call.Status().Code.String() != "Error" {
		t.Errorf("origin-call status = %v, want Error for a rate_limited outcome", call.Status())
	}
}

func TestRequestSpanMarksAnErrorAnswer(t *testing.T) {
	f := newFixture(t, nil)
	if resp := f.call("nope"); resp.Error == nil {
		t.Fatal("an unregistered op must be answered with an error")
	}
	var found bool
	for _, s := range f.spans.Ended() {
		if s.Name() == spanRequest {
			found = true
			if s.Status().Code.String() != "Error" {
				t.Errorf("request span status = %v, want Error", s.Status())
			}
		}
	}
	if !found {
		t.Error("no request span recorded")
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	f := newFixture(t, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.tel.Request(origin.ResultOK, time.Second)
			f.tel.Served("summary", time.Second)
			f.tel.FeedObserver().Redelivered("desk", "mine", 1)
			f.tel.ChangeObserver().Settled(engine.ResultSettled)
			f.collect()
		}()
	}
	wg.Wait()
}

// ---- Setup -----------------------------------------------------------

type fakeMetricExporter struct {
	sdkmetric.Exporter
	endpoint string
	shutdown bool
}

func (*fakeMetricExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (*fakeMetricExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}
func (*fakeMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error { return nil }
func (*fakeMetricExporter) ForceFlush(context.Context) error                          { return nil }
func (e *fakeMetricExporter) Shutdown(context.Context) error                          { e.shutdown = true; return nil }

type fakeSpanExporter struct {
	endpoint string
	shutdown bool
}

func (*fakeSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (e *fakeSpanExporter) Shutdown(context.Context) error                           { e.shutdown = true; return nil }

// stubExporters replaces the exporter constructors for one test and restores
// them and the global providers afterwards.
func stubExporters(t *testing.T, m func(string) (sdkmetric.Exporter, error), s func(string) (sdktrace.SpanExporter, error)) {
	t.Helper()
	om, os := newMetricExporter, newSpanExporter
	newMetricExporter = func(_ context.Context, ep string) (sdkmetric.Exporter, error) { return m(ep) }
	newSpanExporter = func(_ context.Context, ep string) (sdktrace.SpanExporter, error) { return s(ep) }
	t.Cleanup(func() {
		newMetricExporter, newSpanExporter = om, os
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	})
}

func TestSetupWithNoEndpointInstallsNoExporter(t *testing.T) {
	for _, endpoint := range []string{"", "   "} {
		stubExporters(t,
			func(string) (sdkmetric.Exporter, error) {
				t.Fatal("a metric exporter was built with no endpoint")
				return nil, nil
			},
			func(string) (sdktrace.SpanExporter, error) {
				t.Fatal("a span exporter was built with no endpoint")
				return nil, nil
			})
		shutdown, err := Setup(Config{Endpoint: endpoint})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); ok {
			t.Error("the SDK meter provider was installed with no endpoint")
		}
		// a Telemetry built on the global no-op providers works and is inert
		tel, err := New(Options{})
		if err != nil {
			t.Fatal(err)
		}
		tel.Request(origin.ResultOK, time.Second)
		tel.Served("summary", time.Second)
		if err := tel.Close(); err != nil {
			t.Error(err)
		}
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("no-op shutdown: %v", err)
		}
	}
}

func TestSetupWithAnEndpointBuildsBothExportersAndShutdownFlushesThem(t *testing.T) {
	me, se := &fakeMetricExporter{}, &fakeSpanExporter{}
	stubExporters(t,
		func(ep string) (sdkmetric.Exporter, error) { me.endpoint = ep; return me, nil },
		func(ep string) (sdktrace.SpanExporter, error) { se.endpoint = ep; return se, nil })
	shutdown, err := Setup(Config{Endpoint: " http://127.0.0.1:4317 ", ServiceVersion: "1.2.3", Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if me.endpoint != "http://127.0.0.1:4317" || se.endpoint != "http://127.0.0.1:4317" {
		t.Errorf("endpoints = %q, %q", me.endpoint, se.endpoint)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !me.shutdown || !se.shutdown {
		t.Errorf("shutdown flushed metric=%v span=%v, want both", me.shutdown, se.shutdown)
	}
}

func TestSetupExporterFailuresAreErrorsAndLeaveNothingInstalled(t *testing.T) {
	boom := errors.New("boom")
	me := &fakeMetricExporter{}
	stubExporters(t,
		func(string) (sdkmetric.Exporter, error) { return nil, boom },
		func(string) (sdktrace.SpanExporter, error) { return &fakeSpanExporter{}, nil })
	if _, err := Setup(Config{Endpoint: "http://x:1"}); !errors.Is(err, boom) {
		t.Errorf("metric exporter failure = %v, want boom", err)
	}
	stubExporters(t,
		func(string) (sdkmetric.Exporter, error) { return me, nil },
		func(string) (sdktrace.SpanExporter, error) { return nil, boom })
	if _, err := Setup(Config{Endpoint: "http://x:1"}); !errors.Is(err, boom) {
		t.Errorf("span exporter failure = %v, want boom", err)
	}
	if !me.shutdown {
		t.Error("the metric exporter was not shut down after the span exporter failed")
	}
	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); ok {
		t.Error("a failed Setup must not install the SDK providers")
	}
}

func TestRealExportersConstructWithoutDialing(t *testing.T) {
	ctx := context.Background()
	me, err := newMetricExporter(ctx, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	se, err := newSpanExporter(ctx, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if err := me.Shutdown(ctx); err != nil {
		t.Errorf("metric exporter shutdown: %v", err)
	}
	if err := se.Shutdown(ctx); err != nil {
		t.Errorf("trace exporter shutdown: %v", err)
	}
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
}
