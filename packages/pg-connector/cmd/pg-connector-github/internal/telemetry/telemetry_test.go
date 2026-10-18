package telemetry

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/scheduler"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

type fixture struct {
	t      *testing.T
	tel    *Telemetry
	reader *sdkmetric.ManualReader
	spans  *tracetest.SpanRecorder
	clock  *governor.ManualClock
	gov    *governor.Governor
	sched  *scheduler.Scheduler
	rt     *router.Router
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t}
	f.clock = governor.NewManualClock(time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	var err error
	f.gov, err = governor.New(governor.Config{
		GraphQL:         governor.Limits{Cap: 4000},
		RESTCore:        governor.Limits{Cap: 1000},
		RESTSearch:      governor.Limits{Cap: 20},
		BackgroundShare: 0.9,
	}, governor.Options{Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	f.sched = scheduler.New(scheduler.Options{Clock: f.clock})
	f.reader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.reader))
	f.spans = tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(f.spans))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()); _ = tp.Shutdown(context.Background()) })

	f.rt = router.New(router.Options{Instances: []string{"pr", "ci"}, Now: f.clock.Now})
	f.tel, err = New(Options{
		MeterProvider:  mp,
		TracerProvider: tp,
		Governor:       f.gov.State,
		Scheduler:      f.sched.State,
		KnownOp:        func(instance, op string) bool { return op == "list" || op == "show" },
		Now:            f.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.tel.Close() })
	f.rt.Use(f.tel.Middleware())
	return f
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

// labelSets returns every datapoint's attributes as "k=v,k=v" strings.
func labelSets(t *testing.T, m metricdata.Metrics) []string {
	t.Helper()
	var sets []attribute.Set
	switch d := m.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, p := range d.DataPoints {
			sets = append(sets, p.Attributes)
		}
	case metricdata.Gauge[float64]:
		for _, p := range d.DataPoints {
			sets = append(sets, p.Attributes)
		}
	case metricdata.Sum[int64]:
		for _, p := range d.DataPoints {
			sets = append(sets, p.Attributes)
		}
	case metricdata.Histogram[int64]:
		for _, p := range d.DataPoints {
			sets = append(sets, p.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, p := range d.DataPoints {
			sets = append(sets, p.Attributes)
		}
	default:
		t.Fatalf("%s: unexpected data type %T", m.Name, m.Data)
	}
	var out []string
	for _, s := range sets {
		var kv []string
		for _, a := range s.ToSlice() {
			kv = append(kv, string(a.Key)+"="+a.Value.String())
		}
		out = append(out, strings.Join(kv, ","))
	}
	sort.Strings(out)
	return out
}

func gaugeInt(t *testing.T, m metricdata.Metrics, labels string) int64 {
	t.Helper()
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("%s: %T is not an int gauge", m.Name, m.Data)
	}
	for _, p := range g.DataPoints {
		if setString(p.Attributes) == labels {
			return p.Value
		}
	}
	t.Fatalf("%s: no datapoint with labels %q in %v", m.Name, labels, labelSets(t, m))
	return 0
}

func gaugeFloat(t *testing.T, m metricdata.Metrics, labels string) float64 {
	t.Helper()
	g := m.Data.(metricdata.Gauge[float64])
	for _, p := range g.DataPoints {
		if setString(p.Attributes) == labels {
			return p.Value
		}
	}
	t.Fatalf("%s: no datapoint with labels %q", m.Name, labels)
	return 0
}

func setString(s attribute.Set) string {
	var kv []string
	for _, a := range s.ToSlice() {
		kv = append(kv, string(a.Key)+"="+a.Value.String())
	}
	return strings.Join(kv, ",")
}

func (f *fixture) register(op string, h func(ctx context.Context)) {
	f.rt.Register("pr", op, scriptout.OpHandler{SchemaVersion: 1, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		h(ctx)
		return map[string]any{}, nil
	}})
}

func (f *fixture) call(op string, args string) scriptout.Response {
	return f.rt.Dispatch(context.Background(), router.Call{
		Instance: "pr",
		Request:  scriptout.Request{Op: op, Args: json.RawMessage(args)},
	})
}

func TestEveryPhase2MetricAppearsWithExactlyItsLabels(t *testing.T) {
	f := newFixture(t)
	f.register("list", func(ctx context.Context) { router.Annotate(ctx, "store", false, 3, 0) })
	if resp := f.call("list", `{}`); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	f.tel.OnSpend(governor.SpendEvent{
		At: f.clock.Now(), Bucket: governor.BucketGraphQL, Class: governor.ClassQuery,
		Purpose: governor.PurposeQuery, Group: "summary", Cost: 3, Outcome: "ok",
	})
	if err := f.gov.Observe(governor.Reading{
		Bucket: governor.BucketGraphQL, Remaining: 3990, Limit: 5000,
		ResetAt: f.clock.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	f.tel.OnBatchSent([]string{"a", "b"}, scheduler.RoleDue)
	if err := f.sched.Submit(scheduler.Task{
		Entity: "e1", Group: "summary", Class: governor.ClassQuery,
		Due: f.clock.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	got := f.collect()
	want := map[string][]string{
		"pg_connector_github.daemon.up":                    {""},
		"pg_connector_github.budget.spent":                 {"bucket=graphql,group=summary,purpose=query"},
		"pg_connector_github.budget.remaining":             {"bucket=graphql"},
		"pg_connector_github.governor.paused":              {"bucket=graphql,reason=none", "bucket=rest_core,reason=none", "bucket=rest_search,reason=none"},
		"pg_connector_github.queue.depth":                  {"class=fill", "class=interactive", "class=invalidated", "class=keepalive", "class=query"},
		"pg_connector_github.queue.oldest_overdue_seconds": {"class=fill", "class=interactive", "class=invalidated", "class=keepalive", "class=query"},
		"pg_connector_github.request.duration":             {"op=list,served_from=store"},
		"pg_connector_github.batch.ids":                    {"role=due"},
	}
	if len(got) != len(want) {
		var names []string
		for n := range got {
			names = append(names, n)
		}
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
	}
	if v := gaugeInt(t, got["pg_connector_github.daemon.up"], ""); v != 1 {
		t.Errorf("daemon.up = %d", v)
	}
	if v := gaugeInt(t, got["pg_connector_github.budget.remaining"], "bucket=graphql"); v != 3990 {
		t.Errorf("budget.remaining = %d", v)
	}
}

func TestBudgetSpentIncreasesByTheObservedCost(t *testing.T) {
	f := newFixture(t)
	ev := governor.SpendEvent{
		At: f.clock.Now(), Bucket: governor.BucketRESTCore, Purpose: governor.PurposeRefresh,
		Group: "detail", Cost: 7, Outcome: "ok",
	}
	f.tel.OnSpend(ev)
	sum := func() (total int64, labels []string) {
		m := f.collect()["pg_connector_github.budget.spent"]
		s := m.Data.(metricdata.Sum[int64])
		if !s.IsMonotonic {
			t.Error("budget.spent must be monotonic")
		}
		for _, p := range s.DataPoints {
			total += p.Value
			labels = append(labels, setString(p.Attributes))
		}
		return total, labels
	}
	if total, labels := sum(); total != 7 || len(labels) != 1 || labels[0] != "bucket=rest_core,group=detail,purpose=refresh" {
		t.Fatalf("after one spend: %d %v", total, labels)
	}
	f.tel.OnSpend(ev)
	ev.Bucket, ev.Group, ev.Purpose, ev.Cost = governor.BucketGraphQL, "", governor.PurposeInteractive, 11
	f.tel.OnSpend(ev)
	total, labels := sum()
	sort.Strings(labels)
	if total != 7+7+11 || strings.Join(labels, "|") != "bucket=graphql,group=none,purpose=interactive|bucket=rest_core,group=detail,purpose=refresh" {
		t.Fatalf("after three spends: %d %v", total, labels)
	}
	// A zero-cost call adds nothing to the counter.
	ev.Cost = 0
	f.tel.OnSpend(ev)
	if total, _ := sum(); total != 25 {
		t.Errorf("a zero-cost spend changed the counter: %d", total)
	}
}

func TestGovernorPausedIsOneWithAReasonWhilePausedAndZeroAfter(t *testing.T) {
	f := newFixture(t)
	f.gov.PauseAll(time.Minute, "secondary rate limit (HTTP 403, retry after 1m0s)")

	m := f.collect()["pg_connector_github.governor.paused"]
	for _, b := range []string{"graphql", "rest_core", "rest_search"} {
		if v := gaugeInt(t, m, "bucket="+b+",reason=secondary_rate_limit"); v != 1 {
			t.Errorf("%s paused = %d, want 1", b, v)
		}
	}
	if len(labelSets(t, m)) != 3 {
		t.Errorf("only the paused series may exist: %v", labelSets(t, m))
	}

	f.clock.Advance(2 * time.Minute)
	m = f.collect()["pg_connector_github.governor.paused"]
	for _, b := range []string{"graphql", "rest_core", "rest_search"} {
		if v := gaugeInt(t, m, "bucket="+b+",reason=none"); v != 0 {
			t.Errorf("%s paused after the pause = %d, want 0", b, v)
		}
	}
	if len(labelSets(t, m)) != 3 {
		t.Errorf("the reason series must be gone after the pause: %v", labelSets(t, m))
	}
}

func TestReasonLabelIsBounded(t *testing.T) {
	for in, want := range map[string]string{
		"secondary rate limit (HTTP 429, retry after 5m0s)": "secondary_rate_limit",
		"Rate limit: retry after 30s":                       "rate_limit",
		"":                                                  "other",
		"(only particulars)":                                "other",
		strings.Repeat("x", 100):                            strings.Repeat("x", 40),
	} {
		if got := reasonLabel(in); got != want {
			t.Errorf("reasonLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBudgetRemainingIsAbsentWithoutAReading(t *testing.T) {
	f := newFixture(t)
	if _, ok := f.collect()["pg_connector_github.budget.remaining"]; ok {
		t.Error("no reading in force: budget.remaining must report nothing, not a made-up value")
	}
}

func TestQueueGaugesFollowTheSchedulerUnderAFakeClock(t *testing.T) {
	f := newFixture(t)
	now := f.clock.Now()
	submit := func(entity string, class governor.Class, due time.Time) {
		t.Helper()
		if err := f.sched.Submit(scheduler.Task{Entity: entity, Group: "summary", Class: class, Due: due}); err != nil {
			t.Fatal(err)
		}
	}
	submit("a", governor.ClassInteractive, time.Time{})
	submit("b", governor.ClassQuery, now.Add(-90*time.Second))
	submit("c", governor.ClassQuery, now.Add(-30*time.Second))
	submit("d", governor.ClassFill, now.Add(time.Hour)) // not due

	got := f.collect()
	depth, overdue := got["pg_connector_github.queue.depth"], got["pg_connector_github.queue.oldest_overdue_seconds"]
	for class, want := range map[string]int64{"interactive": 1, "invalidated": 0, "query": 2, "keepalive": 0, "fill": 1} {
		if v := gaugeInt(t, depth, "class="+class); v != want {
			t.Errorf("depth[%s] = %d, want %d", class, v, want)
		}
	}
	for class, want := range map[string]float64{"interactive": 0, "query": 90, "fill": 0} {
		if v := gaugeFloat(t, overdue, "class="+class); v != want {
			t.Errorf("oldest_overdue[%s] = %v, want %v", class, v, want)
		}
	}

	f.clock.Advance(60 * time.Second)
	if v := gaugeFloat(t, f.collect()["pg_connector_github.queue.oldest_overdue_seconds"], "class=query"); v != 150 {
		t.Errorf("oldest_overdue[query] after 60s = %v, want 150", v)
	}
	f.sched.Cancel("b", "summary")
	f.sched.Cancel("c", "summary")
	got = f.collect()
	if v := gaugeInt(t, got["pg_connector_github.queue.depth"], "class=query"); v != 0 {
		t.Errorf("depth[query] after cancel = %d", v)
	}
	if v := gaugeFloat(t, got["pg_connector_github.queue.oldest_overdue_seconds"], "class=query"); v != 0 {
		t.Errorf("oldest_overdue[query] after cancel = %v", v)
	}
}

func TestBatchIDsRecordsOneObservationPerBatchWithItsRole(t *testing.T) {
	f := newFixture(t)
	f.tel.OnBatchSent([]string{"1", "2", "3"}, scheduler.RoleDue)
	f.tel.OnBatchSent([]string{"4", "5"}, scheduler.RoleFill)
	h := f.collect()["pg_connector_github.batch.ids"].Data.(metricdata.Histogram[int64])
	got := map[string][2]int64{}
	for _, p := range h.DataPoints {
		got[setString(p.Attributes)] = [2]int64{int64(p.Count), p.Sum}
	}
	if len(got) != 2 || got["role=due"] != [2]int64{1, 3} || got["role=fill"] != [2]int64{1, 2} {
		t.Errorf("batch.ids = %v", got)
	}
	f.tel.OnBatchSent([]string{"6"}, scheduler.RoleDue)
	h = f.collect()["pg_connector_github.batch.ids"].Data.(metricdata.Histogram[int64])
	for _, p := range h.DataPoints {
		if setString(p.Attributes) == "role=due" && (p.Count != 2 || p.Sum != 4) {
			t.Errorf("second due batch: count %d sum %d", p.Count, p.Sum)
		}
	}
}

func TestRequestDurationCarriesServedFromAndTheMeasuredDuration(t *testing.T) {
	f := newFixture(t)
	f.register("list", func(ctx context.Context) {
		f.clock.Advance(250 * time.Millisecond)
		router.Annotate(ctx, "origin", false, 0, 0)
	})
	f.register("show", func(context.Context) { f.clock.Advance(time.Second) }) // says nothing about where from
	f.call("list", `{}`)
	f.call("show", `{}`)

	h := f.collect()["pg_connector_github.request.duration"].Data.(metricdata.Histogram[float64])
	got := map[string]float64{}
	for _, p := range h.DataPoints {
		got[setString(p.Attributes)] = p.Sum
	}
	if len(got) != 2 || got["op=list,served_from=origin"] != 0.25 || got["op=show,served_from=none"] != 1 {
		t.Errorf("request.duration = %v", got)
	}
}

func TestNoLabelValueIsAnEntityIdOrAnAdHocQueryKey(t *testing.T) {
	f := newFixture(t)
	f.register("list", func(ctx context.Context) {
		router.Annotate(ctx, "store", false, 0, 0)
		f.tel.RecordSpend(ctx, governor.SpendEvent{
			At: f.clock.Now(), Bucket: governor.BucketGraphQL, Purpose: governor.PurposeQuery,
			Group: "summary", Cost: 2, Outcome: "ok",
		})
	})
	f.call("list", `{"ids":["example-owner/example-repo#41","example-owner/example-repo#42"],"query":"adhoc-query-key-77"}`)
	f.call("adhoc-query-key-77", `{"id":"example-owner/example-repo#43"}`) // an op nobody registered
	f.tel.OnBatchSent([]string{"example-owner/example-repo#44"}, scheduler.RoleDue)
	f.gov.PauseAll(time.Minute, "secondary rate limit (HTTP 403, retry after 1m0s)")
	if err := f.sched.Submit(scheduler.Task{Entity: "example-owner/example-repo#45", Group: "summary", Class: governor.ClassQuery}); err != nil {
		t.Fatal(err)
	}

	var values []string
	for _, m := range f.collect() {
		for _, set := range labelSets(t, m) {
			for _, kv := range strings.Split(set, ",") {
				if _, v, ok := strings.Cut(kv, "="); ok {
					values = append(values, v)
				}
			}
		}
	}
	if len(values) == 0 {
		t.Fatal("no label values recorded")
	}
	for _, v := range values {
		if strings.Contains(v, "#") || strings.Contains(v, "example-") || strings.Contains(v, "adhoc") || strings.Contains(v, "/") {
			t.Errorf("label value %q looks like an entity id or an ad-hoc key", v)
		}
	}
	h := f.collect()["pg_connector_github.request.duration"].Data.(metricdata.Histogram[float64])
	seen := map[string]bool{}
	for _, p := range h.DataPoints {
		seen[setString(p.Attributes)] = true
	}
	if !seen["op=unknown,served_from=none"] || !seen["op=list,served_from=store"] {
		t.Errorf("an unregistered op must fold to op=unknown: %v", seen)
	}
	// Spans carry no request argument either.
	for _, s := range f.spans.Ended() {
		for _, a := range s.Attributes() {
			if v := a.Value.String(); strings.Contains(v, "#") || strings.Contains(v, "adhoc") {
				t.Errorf("span %s attribute %s=%q leaks an id or query key", s.Name(), a.Key, v)
			}
		}
	}
}

func TestRequestSpanHasAChildPerOriginCallWithItsCost(t *testing.T) {
	f := newFixture(t)
	f.register("list", func(ctx context.Context) {
		router.Annotate(ctx, "origin", false, 0, 2*time.Second)
		f.tel.RecordSpend(ctx, governor.SpendEvent{
			At: f.clock.Now(), Bucket: governor.BucketGraphQL, Class: governor.ClassInteractive,
			Purpose: governor.PurposeInteractive, Group: "summary", BatchSize: 4, Cost: 5, Outcome: "ok",
		})
		f.tel.RecordSpend(ctx, governor.SpendEvent{
			At: f.clock.Now(), Bucket: governor.BucketRESTCore, Class: governor.ClassInteractive,
			Purpose: governor.PurposeInteractive, Cost: 1, Outcome: "rate_limited",
		})
	})
	f.call("list", `{}`)

	var req sdktrace.ReadOnlySpan
	var origins []sdktrace.ReadOnlySpan
	for _, s := range f.spans.Ended() {
		switch s.Name() {
		case spanRequest:
			req = s
		case spanOriginCall:
			origins = append(origins, s)
		}
	}
	if req == nil || len(origins) != 2 {
		t.Fatalf("request span %v, origin spans %d", req != nil, len(origins))
	}
	attrs := func(s sdktrace.ReadOnlySpan) map[string]string {
		m := map[string]string{}
		for _, a := range s.Attributes() {
			m[string(a.Key)] = a.Value.String()
		}
		return m
	}
	ra := attrs(req)
	if ra["op"] != "list" || ra["instance"] != "pr" || ra["served_from"] != "origin" || ra["wait_seconds"] != "2" {
		t.Errorf("request span attributes %v", ra)
	}
	for i, o := range origins {
		if o.Parent().SpanID() != req.SpanContext().SpanID() || o.SpanContext().TraceID() != req.SpanContext().TraceID() {
			t.Errorf("origin span %d is not a child of the request span", i)
		}
	}
	if a := attrs(origins[0]); a["cost"] != "5" || a["bucket"] != "graphql" || a["group"] != "summary" || a["batch_size"] != "4" {
		t.Errorf("first origin span attributes %v", a)
	}
	if a := attrs(origins[1]); a["cost"] != "1" || a["bucket"] != "rest_core" || a["outcome"] != "rate_limited" {
		t.Errorf("second origin span attributes %v", a)
	}
	if origins[1].Status().Code.String() != "Error" || origins[0].Status().Code.String() == "Error" {
		t.Errorf("only the rate-limited call is an error span: %v %v", origins[0].Status(), origins[1].Status())
	}
}

func TestRequestSpanMarksAnErrorAnswer(t *testing.T) {
	f := newFixture(t)
	f.call("show", `{}`) // nothing registered: unknown_op
	for _, s := range f.spans.Ended() {
		if s.Name() == spanRequest && s.Status().Code.String() == "Error" {
			return
		}
	}
	t.Error("an error answer must mark the request span")
}
