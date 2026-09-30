package telemetry

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// TestMetricsRegistration_NamesTypesAndLabels drives newMetricsInstruments
// directly against a fresh, test-local SDK meter (a ManualReader, no OTLP
// network involved) and asserts each of the seven D6 instruments is
// registered under exactly its specified name, aggregation type
// (Sum=counter, Gauge=gauge), and label set. This is independent of the
// package-level lazy singleton (ensureInstruments/instrumentsOnce) — it
// never touches it — so it is unaffected by whatever order Go runs the
// other tests in this package in.
func TestMetricsRegistration_NamesTypesAndLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()

	inst, err := newMetricsInstruments(mp.Meter("test"))
	if err != nil {
		t.Fatalf("newMetricsInstruments: %v", err)
	}

	ctx := context.Background()
	inst.retriesTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("class", "network")))
	inst.retryExhaustedTotal.Add(ctx, 1)
	inst.cancelTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success")))
	inst.reapClosuresTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "idle_ttl")))
	inst.reapPhantomPrunedTotal.Add(ctx, 1)
	inst.sessionsPreservedForHuman.Record(ctx, 3)
	inst.launchOutcomeTotal.Add(ctx, 1,
		metric.WithAttributes(attribute.String("route", "resume"), attribute.String("outcome", "success")))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	got := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			got[m.Name] = m
		}
	}

	cases := []struct {
		name        string
		wantCounter bool // true = Sum (counter), false = Gauge
		wantLabels  []string
	}{
		{"ccpool_retries_total", true, []string{"class"}},
		{"ccpool_retry_exhausted_total", true, nil},
		{"ccpool_cancel_total", true, []string{"outcome"}},
		{"ccpool_reap_closures_total", true, []string{"reason"}},
		{"ccpool_reap_phantom_pruned_total", true, nil},
		{"ccpool_sessions_preserved_for_human", false, nil},
		{"ccpool_launch_outcome_total", true, []string{"route", "outcome"}},
	}

	for _, c := range cases {
		m, ok := got[c.name]
		if !ok {
			t.Errorf("instrument %q not registered/collected", c.name)
			continue
		}

		var attrs attribute.Set
		switch data := m.Data.(type) {
		case metricdata.Sum[int64]:
			if !c.wantCounter {
				t.Errorf("%s: got Sum (counter), want Gauge", c.name)
			}
			if len(data.DataPoints) != 1 {
				t.Errorf("%s: got %d data points, want 1", c.name, len(data.DataPoints))
				continue
			}
			attrs = data.DataPoints[0].Attributes
		case metricdata.Gauge[int64]:
			if c.wantCounter {
				t.Errorf("%s: got Gauge, want Sum (counter)", c.name)
			}
			if len(data.DataPoints) != 1 {
				t.Errorf("%s: got %d data points, want 1", c.name, len(data.DataPoints))
				continue
			}
			attrs = data.DataPoints[0].Attributes
		default:
			t.Errorf("%s: unexpected aggregation type %T", c.name, m.Data)
			continue
		}

		if got, want := attrs.Len(), len(c.wantLabels); got != want {
			t.Errorf("%s: got %d labels, want %d (%v)", c.name, got, want, c.wantLabels)
		}
		for _, label := range c.wantLabels {
			if !attrs.HasValue(attribute.Key(label)) {
				t.Errorf("%s: missing expected label %q", c.name, label)
			}
		}
	}
}

// TestMetrics_NoInit_RecordFunctionsAreSafe calls every Record* function
// before telemetry.Init has ever run in this process, mirroring
// TestMeterProvider_NoInit_ReturnsUsableNoop in telemetry_test.go: the
// package-level MeterProvider defaults to a usable no-op even pre-Init, so
// every Record* call here MUST complete without panicking.
func TestMetrics_NoInit_RecordFunctionsAreSafe(t *testing.T) {
	recordAllForTest()
}

// TestMetrics_AfterNoopInit_RecordFunctionsAreSafe calls Init with no OTLP
// endpoint configured (installing the no-op MeterProvider explicitly, per
// Init's own documented no-endpoint behaviour — mirrors
// TestInit_NoEndpoint_InstallsNoopLoggerProvider in telemetry_test.go) and
// then exercises every Record* function, asserting the same no-panic safety.
func TestMetrics_AfterNoopInit_RecordFunctionsAreSafe(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown := Init(context.Background())
	defer func() { _ = shutdown(context.Background()) }()

	recordAllForTest()
}

// recordAllForTest exercises every exported Record* function once with
// representative arguments. Shared by both no-op/safe-fallback tests above.
func recordAllForTest() {
	attrs := MetricAttrs("/pools/alpha", []string{"pgrouter.role"},
		[]attribute.KeyValue{attribute.String("pgrouter.role", "review")})
	RecordRetry("network", attrs)
	RecordRetryExhausted(attrs)
	RecordCancel("success", attrs)
	RecordReapClosure("idle_ttl", attrs)
	RecordReapPhantomPruned(attrs)
	RecordSessionsPreservedForHuman(1, attrs)
	RecordLaunchOutcome("resume", "success", attrs)
	RecordSessionStates([]SessionStateCount{{State: "working", Live: false, Count: 1}}, attrs)
}

// TestSessionStates_GaugePerBucket asserts ccpool_session_states registers as
// a gauge and carries one point per (state, live) bucket recorded.
func TestSessionStates_GaugePerBucket(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	inst, err := newMetricsInstruments(mp.Meter("test"))
	if err != nil {
		t.Fatalf("newMetricsInstruments: %v", err)
	}
	ctx := context.Background()
	inst.sessionStates.Record(ctx, 2, metric.WithAttributes(attribute.String("state", "working"), attribute.Bool("live", false)))
	inst.sessionStates.Record(ctx, 0, metric.WithAttributes(attribute.String("state", "working"), attribute.Bool("live", true)))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "ccpool_session_states" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("ccpool_session_states: got %T, want Gauge[int64]", m.Data)
			}
			if len(g.DataPoints) != 2 {
				t.Fatalf("got %d data points, want 2", len(g.DataPoints))
			}
			for _, dp := range g.DataPoints {
				if !dp.Attributes.HasValue("state") || !dp.Attributes.HasValue("live") {
					t.Errorf("data point missing state/live labels: %v", dp.Attributes)
				}
			}
			found = true
		}
	}
	if !found {
		t.Error("ccpool_session_states not collected")
	}
}

// --- pool + allowlisted-label attributes on every record (pg2-om899.4) ---

// sessionAttrsWithBead is what telemetry.SessionAttrs would return for a
// session labelled with BOTH an allowlisted key (pgrouter.role) and a
// non-allowlisted one (pgrouter.bead): SessionAttrs applies no key filtering
// of its own (see session.go), so MetricAttrs is the only cardinality guard.
func sessionAttrsWithBead() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("pgrouter.bead", "pg2-secret-1"),
		attribute.String("pgrouter.role", "review"),
	}
}

// TestMetricAttrs_poolIsBasenameNeverAPath pins the pool value: the basename
// of the resolved pool root, and the literal default when the root is empty
// (default mode).
func TestMetricAttrs_poolIsBasenameNeverAPath(t *testing.T) {
	cases := []struct{ name, root, want string }{
		{"pool dir", "/Users/x/.local/share/pg-router-ccpool-review", "pg-router-ccpool-review"},
		{"default mode (empty root)", "", "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := attribute.NewSet(MetricAttrs(c.root, nil, nil)...)
			v, ok := set.Value("pool")
			if !ok || v.AsString() != c.want {
				t.Errorf("pool = %q (present=%v), want %q", v.AsString(), ok, c.want)
			}
			if set.Len() != 1 {
				t.Errorf("attrs = %v, want pool only when no allowlist is configured", set.ToSlice())
			}
		})
	}
}

// TestMetricAttrs_allowlistFiltersLabels: only allowlisted label keys reach
// the metric attributes; everything else is dropped (it still appears on logs).
func TestMetricAttrs_allowlistFiltersLabels(t *testing.T) {
	set := attribute.NewSet(MetricAttrs("/pools/alpha", []string{"pgrouter.role"}, sessionAttrsWithBead())...)
	if v, ok := set.Value("pgrouter.role"); !ok || v.AsString() != "review" {
		t.Errorf("pgrouter.role = %q (present=%v), want review", v.AsString(), ok)
	}
	if set.HasValue("pgrouter.bead") {
		t.Errorf("non-allowlisted pgrouter.bead leaked into metric attrs: %v", set.ToSlice())
	}
	if v, _ := set.Value("pool"); v.AsString() != "alpha" {
		t.Errorf("pool = %q, want alpha", v.AsString())
	}
}

// TestMetricAttrs_emptyAllowlistFailsClosed: a nil/empty allowlist passes NO
// session labels (cardinality guard fails closed), only pool.
func TestMetricAttrs_emptyAllowlistFailsClosed(t *testing.T) {
	for _, allow := range [][]string{nil, {}} {
		set := attribute.NewSet(MetricAttrs("/pools/alpha", allow, sessionAttrsWithBead())...)
		if set.Len() != 1 || !set.HasValue("pool") {
			t.Errorf("allowlist %v: attrs = %v, want pool only", allow, set.ToSlice())
		}
	}
}

// TestMetricAttrs_poolKeyIsReserved: a session label (or allowlist entry)
// named "pool" can never displace the real pool attribute.
func TestMetricAttrs_poolKeyIsReserved(t *testing.T) {
	set := attribute.NewSet(MetricAttrs("/pools/alpha", []string{"pool"},
		[]attribute.KeyValue{attribute.String("pool", "spoofed")})...)
	if v, _ := set.Value("pool"); v.AsString() != "alpha" {
		t.Errorf("pool = %q, want alpha (a label named pool must not override it)", v.AsString())
	}
}

// TestMetricAttrs_doesNotMutateInput: MetricAttrs must not reorder or alias
// the SessionAttrs slice it is given.
func TestMetricAttrs_doesNotMutateInput(t *testing.T) {
	in := sessionAttrsWithBead()
	_ = MetricAttrs("/pools/alpha", []string{"pgrouter.role"}, in)
	if len(in) != 2 || in[0].Key != "pgrouter.bead" || in[1].Key != "pgrouter.role" {
		t.Errorf("input mutated: %v", in)
	}
}

// TestInstruments_everyRecordCarriesPoolAndAllowlistedLabels is the
// per-instrument acceptance test: each of the eight instruments, driven
// through the same attribute path the production Record* functions use
// (instrument methods against a test-local meter; the Record* wrappers add
// only the lazy singleton), emits pool and the allowlisted pgrouter.role and
// NEVER the non-allowlisted pgrouter.bead.
func TestInstruments_everyRecordCarriesPoolAndAllowlistedLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	inst, err := newMetricsInstruments(mp.Meter("test"))
	if err != nil {
		t.Fatalf("newMetricsInstruments: %v", err)
	}

	ctx := context.Background()
	attrs := MetricAttrs("/pools/pg-router-ccpool-review", []string{"pgrouter.role"}, sessionAttrsWithBead())
	inst.retry(ctx, "transient_server", attrs)
	inst.retryExhausted(ctx, attrs)
	inst.cancel(ctx, "success", attrs)
	inst.reapClosure(ctx, "idle_ttl", attrs)
	inst.reapPhantomPruned(ctx, attrs)
	inst.sessionsPreserved(ctx, 2, attrs)
	inst.launchOutcome(ctx, "resume", "success", attrs)
	inst.states(ctx, []SessionStateCount{{State: "working", Live: true, Count: 1}}, attrs)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	got := map[string][]attribute.Set{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					got[m.Name] = append(got[m.Name], dp.Attributes)
				}
			case metricdata.Gauge[int64]:
				for _, dp := range d.DataPoints {
					got[m.Name] = append(got[m.Name], dp.Attributes)
				}
			}
		}
	}

	wantNames := []string{
		"ccpool_retries_total", "ccpool_retry_exhausted_total", "ccpool_cancel_total",
		"ccpool_reap_closures_total", "ccpool_reap_phantom_pruned_total",
		"ccpool_sessions_preserved_for_human", "ccpool_launch_outcome_total", "ccpool_session_states",
	}
	for _, name := range wantNames {
		sets := got[name]
		if len(sets) != 1 {
			t.Errorf("%s: got %d data points, want 1", name, len(sets))
			continue
		}
		set := sets[0]
		if v, ok := set.Value("pool"); !ok || v.AsString() != "pg-router-ccpool-review" {
			t.Errorf("%s: pool = %q (present=%v), want pg-router-ccpool-review", name, v.AsString(), ok)
		}
		if v, ok := set.Value("pgrouter.role"); !ok || v.AsString() != "review" {
			t.Errorf("%s: pgrouter.role = %q (present=%v), want review", name, v.AsString(), ok)
		}
		if set.HasValue("pgrouter.bead") {
			t.Errorf("%s: non-allowlisted pgrouter.bead leaked: %v", name, set.ToSlice())
		}
	}
	// The per-instrument attributes still ride alongside pool/labels.
	if v, _ := got["ccpool_retries_total"][0].Value("class"); v.AsString() != "transient_server" {
		t.Errorf("ccpool_retries_total class = %q, want transient_server", v.AsString())
	}
	if v, _ := got["ccpool_launch_outcome_total"][0].Value("route"); v.AsString() != "resume" {
		t.Errorf("ccpool_launch_outcome_total route = %q, want resume", v.AsString())
	}
}

// TestInstruments_recordSpecificAttrWinsOverLabelCollision: an allowlisted
// label that collides with an instrument's own attribute key (for example a
// session label literally named "outcome") cannot override the instrument's
// own value.
func TestInstruments_recordSpecificAttrWinsOverLabelCollision(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	inst, err := newMetricsInstruments(mp.Meter("test"))
	if err != nil {
		t.Fatalf("newMetricsInstruments: %v", err)
	}
	attrs := MetricAttrs("/pools/alpha", []string{"outcome"}, []attribute.KeyValue{attribute.String("outcome", "spoofed")})
	inst.cancel(context.Background(), "success", attrs)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	sum := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	if v, _ := sum.DataPoints[0].Attributes.Value("outcome"); v.AsString() != "success" {
		t.Errorf("outcome = %q, want success (instrument's own attribute wins)", v.AsString())
	}
}

// TestSessionsPreservedForHuman_descriptionStatesLastValueWins: the gauge's
// description MUST warn that a short-lived CLI is last-value-wins.
func TestSessionsPreservedForHuman_descriptionStatesLastValueWins(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	inst, err := newMetricsInstruments(mp.Meter("test"))
	if err != nil {
		t.Fatalf("newMetricsInstruments: %v", err)
	}
	inst.sessionsPreserved(context.Background(), 1, MetricAttrs("/pools/alpha", nil, nil))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "ccpool_sessions_preserved_for_human" {
				continue
			}
			if !strings.Contains(strings.ToLower(m.Description), "last-value-wins") {
				t.Errorf("description %q must state that a short-lived CLI is last-value-wins", m.Description)
			}
			return
		}
	}
	t.Error("ccpool_sessions_preserved_for_human not collected")
}

// TestSessionInfo_gaugeValueOneWithClaudeSessionID: ccpool_session_info is an
// Int64 gauge of value 1 carrying claude_session_id plus the supplied attrs;
// an empty id records nothing.
func TestSessionInfo_gaugeValueOneWithClaudeSessionID(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(context.Background()) }()
	inst, err := newMetricsInstruments(mp.Meter("test"))
	if err != nil {
		t.Fatalf("newMetricsInstruments: %v", err)
	}
	attrs := []attribute.KeyValue{attribute.String("pgrouter.role", "review"), attribute.String("pool", "p1")}
	inst.info(context.Background(), "csid-1", attrs)
	inst.info(context.Background(), "", attrs)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var dps []metricdata.DataPoint[int64]
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "ccpool_session_info" {
				g, ok := m.Data.(metricdata.Gauge[int64])
				if !ok {
					t.Fatalf("got %T, want Gauge[int64]", m.Data)
				}
				dps = g.DataPoints
			}
		}
	}
	if len(dps) != 1 {
		t.Fatalf("data points = %d, want 1 (empty id must record nothing)", len(dps))
	}
	dp := dps[0]
	if dp.Value != 1 {
		t.Errorf("value = %d, want 1", dp.Value)
	}
	for k, want := range map[string]string{"claude_session_id": "csid-1", "pool": "p1", "pgrouter.role": "review"} {
		v, ok := dp.Attributes.Value(attribute.Key(k))
		if !ok || v.AsString() != want {
			t.Errorf("attr %s = %q (present=%v), want %q", k, v.AsString(), ok, want)
		}
	}
	if dp.Attributes.Len() != 3 {
		t.Errorf("attrs = %v, want exactly 3", dp.Attributes)
	}
}
