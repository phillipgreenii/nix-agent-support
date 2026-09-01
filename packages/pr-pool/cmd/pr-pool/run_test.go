package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/conformance"
	"github.com/phillipgreenii/pr-pool/internal/activity"
	"github.com/phillipgreenii/pr-pool/internal/backoff"
	"github.com/phillipgreenii/pr-pool/internal/config"
	"github.com/phillipgreenii/pr-pool/internal/core"
	"github.com/phillipgreenii/pr-pool/internal/dtest"
	"github.com/phillipgreenii/pr-pool/internal/event"
	"github.com/phillipgreenii/pr-pool/internal/eventqueue"
	"github.com/phillipgreenii/pr-pool/internal/item"
	"github.com/phillipgreenii/pr-pool/internal/metrics"
	"github.com/phillipgreenii/pr-pool/internal/orchestrator"
	"github.com/phillipgreenii/pr-pool/internal/query"
	"github.com/phillipgreenii/pr-pool/internal/roles"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// RoleSet.DeclaredBindTypes (moved from this package's own declaredBindTypes to
// a shared home, Task 1.1) must still count a role's Binds even when that role
// is disabled BY A RUN-SCOPED SELECTOR (applySelectors flips Enabled, never
// touches Binds): the "declared but inactive this run" half of INV-DISP-3
// depends on this — a selector-excluded role must still count as a DECLARED
// binding, never as if it were never configured at all.
func TestDeclaredBindTypes_selectorDisabledRoleStillCounts(t *testing.T) {
	rs := roles.RoleSet{
		{Name: "r1", Enabled: true, Binds: []string{"t1"}},
		{Name: "r2", Enabled: false, Binds: []string{"t2"}}, // as applySelectors would leave a --disable'd role
	}
	got := rs.DeclaredBindTypes()
	want := []string{"t1", "t2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeclaredBindTypes = %v, want %v (a run-scoped exclusion must not drop a type from the declared set)", got, want)
	}
}

// fakeCommander is a query.Commander stand-in that records every argv it is
// asked to run, so a "command"-type role's dispatch is observable without
// shelling out to a real executable. bootCore/queue.Dispatch tests below drive
// the offer phase SEQUENTIALLY (queue.Dispatch's phase 2 is a plain for-loop,
// not concurrent goroutines — internal/eventqueue/queue.go), so no locking is
// needed here.
type fakeCommander struct{ calls [][]string }

func (f *fakeCommander) Run(_ context.Context, argv []string) ([]byte, error) {
	f.calls = append(f.calls, argv)
	return nil, nil
}

// TestBootCore_selectorExcludedRoleNotRegisteredAsListener proves the
// acceptance criterion end to end: a role a run-scoped --disable excludes
// (applySelectors flips its Enabled to false) never gets a Listener
// registered by bootCore (run.go), so an event of its bound type is never
// offered to it — while an INCLUDED role's sibling event IS dispatched in the
// same pass. This is the composition of applySelectors' new Enabled-flip with
// bootCore's PRE-EXISTING role.Enabled skip (run.go) — nothing in bootCore
// itself needed to change.
func TestBootCore_selectorExcludedRoleNotRegisteredAsListener(t *testing.T) {
	cmd := &fakeCommander{}
	cfg := config.Config{
		LogDir: shortDir(t), // AF_UNIX path length cap; see shortDir's doc (ingest_event_test.go)
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Type: "command", Binds: []string{"t1"}, Command: &roles.CommandConfig{Argv: []string{"r1-cmd"}}},
			{Name: "r2", Enabled: true, Type: "command", Binds: []string{"t2"}, Command: &roles.CommandConfig{Argv: []string{"r2-cmd"}}},
		},
	}
	cfg, err := applySelectors(cfg, runSelectors{Disable: []string{"role:r2"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if roleEnabled(cfg.Roles, "r2") {
		t.Fatalf("precondition: r2 must be selector-disabled")
	}
	if !roleEnabled(cfg.Roles, "r1") {
		t.Fatalf("precondition: r1 must remain enabled")
	}

	// BD must be a non-nil beads.Runner: the dispatched role's Offer path calls
	// o.snapshotIDs/o.buildResult (a "created beads" diff + final status read),
	// which shell out through it. Give it a status for the one item ("bd-t1")
	// r1's dispatch will actually reach, so beads.Status finds a real entry
	// rather than indexing an empty per-id sequence.
	bd := &dtest.ScriptBD{StatusSeq: map[string][]string{"bd-t1": {"open"}}}
	o := &orchestrator.Orchestrator{Cfg: cfg, Cmd: cmd, BD: bd}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	future := time.Now().Add(time.Hour)
	// Payload carries the dispatch item (discover.ItemFromPayload's "item" key)
	// so the dispatched role's Offer resolves a real bead id, matching bd's
	// StatusSeq above, instead of "".
	payloadFor := func(typ string) map[string]any {
		return map[string]any{"item": map[string]any{"id": "bd-" + typ, "type": "task"}}
	}
	for _, typ := range []string{"t1", "t2"} {
		if _, err := q.Enqueue(eventqueue.Event{ID: "ev-" + typ, Type: typ, ExpiresAt: future, Payload: payloadFor(typ)}); err != nil {
			t.Fatalf("enqueue %s: %v", typ, err)
		}
	}
	q.Dispatch()

	if len(cmd.calls) != 1 {
		t.Fatalf("commander calls = %v, want exactly 1 (r1 only; r2 has no registered listener so its event is never offered)", cmd.calls)
	}
	if cmd.calls[0][0] != "r1-cmd" {
		t.Errorf("dispatched argv = %v, want r1's command", cmd.calls[0])
	}
}

// TestBootCore_InProcessParticipantAvailableImmediately proves Task 2.1's own
// named acceptance test: a registered in-process handler (a role's
// roleListener, registered onto the queue AND the registry by bootCore's own
// loop) is Available IMMEDIATELY after bootCore returns — before Accept ever
// runs, before any register verb crosses the wire at all. RED against the
// pre-fix code: bootCore never touched svc.Registry() (only q.Register), so
// Available was false for every role.
func TestBootCore_InProcessParticipantAvailableImmediately(t *testing.T) {
	cfg := config.Config{
		LogDir: shortDir(t),
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Type: "command", Binds: []string{"t1"}, Command: &roles.CommandConfig{Argv: []string{"r1-cmd"}}},
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Cmd: &fakeCommander{}, BD: &dtest.ScriptBD{}}
	svc, _, _, storeClose, err := bootCore(context.Background(), cfg, o)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if !svc.Registry().Available("r1") {
		t.Fatal("registered in-process handler r1 must be Available immediately after bootCore")
	}
}

// selTestQuery is a minimal query.Query stand-in (mirrors internal/discover's
// own unexported fakeQuery, copied here since that one is package-private):
// it records whether Run was ever called and returns one canned event of its
// configured emit type.
type selTestQuery struct {
	query.Meta
	ran *bool
	typ string
}

func (q selTestQuery) Validate() error        { return nil }
func (q selTestQuery) BackingCommand() string { return "" }
func (q selTestQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	*q.ran = true
	return []event.Event{event.NewItemEvent(q.typ, "", item.Item{ID: "x-" + q.typ, Type: "task"})}, nil
}

// TestApplySelectors_queryExcludedNeverProduces proves the second acceptance
// criterion: a query a run-scoped selector excludes (applySelectors drops it
// from cfg.Queries entirely) never has its Run called by ProduceTick, so no
// event from it ever reaches the queue — while a sibling INCLUDED query's
// event does.
func TestApplySelectors_queryExcludedNeverProduces(t *testing.T) {
	var q1Ran, q2Ran bool
	cfg := config.Config{
		Queries: query.SourceSet{
			{Name: "q1", Query: selTestQuery{Meta: query.Meta{EmitTypes: []string{"t1"}}, ran: &q1Ran, typ: "t1"}},
			{Name: "q2", Query: selTestQuery{Meta: query.Meta{EmitTypes: []string{"t2"}}, ran: &q2Ran, typ: "t2"}},
		},
	}
	cfg, err := applySelectors(cfg, runSelectors{Disable: []string{"query:q2"}})
	if err != nil {
		t.Fatalf("applySelectors: %v", err)
	}
	if len(cfg.Queries) != 1 || cfg.Queries[0].Name != "q1" {
		t.Fatalf("cfg.Queries = %v, want only q1", queryNames(cfg.Queries))
	}

	// t1/t2 declared directly (this test wires no Roles): a real run derives this
	// set from cfg.Roles.DeclaredBindTypes() (bootCore), but this test's own
	// concern is selector exclusion, not the undeclared-type rejection Task 1.1
	// added — so declare both query types to keep that orthogonal.
	o := &orchestrator.Orchestrator{Cfg: cfg, Bindings: core.NewBindings("t1", "t2")}
	queue, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	if _, err := o.ProduceTick(context.Background(), queue); err != nil {
		t.Fatalf("ProduceTick: %v", err)
	}

	if !q1Ran {
		t.Error("q1 (not excluded) should have run")
	}
	if q2Ran {
		t.Error("q2 (selector-excluded) must never run")
	}
	depth := queue.DepthByType()
	if depth["t1"] != 1 {
		t.Errorf("depth[t1] = %d, want 1 (q1's event)", depth["t1"])
	}
	if depth["t2"] != 0 {
		t.Errorf("depth[t2] = %d, want 0 (q2 excluded, never produced)", depth["t2"])
	}
}

// recordingDispatchFailureObserver is a minimal eventqueue.Observer that only
// records OnDispatchFailure calls — enough to prove fanOutObserver.
// OnDispatchFailure (bead pg2-icm3u) reaches both fanned-out observers.
type recordingDispatchFailureObserver struct{ dispatchFailed []string }

func (*recordingDispatchFailureObserver) OnEnqueue(eventqueue.Event)        {}
func (*recordingDispatchFailureObserver) OnAccept(string, string)           {}
func (*recordingDispatchFailureObserver) OnUnconsumedExpired(string)        {}
func (*recordingDispatchFailureObserver) OnDeclined(string, string, string) {}
func (*recordingDispatchFailureObserver) OnDeduped(string)                  {}
func (r *recordingDispatchFailureObserver) OnDispatchFailure(t string) {
	r.dispatchFailed = append(r.dispatchFailed, t)
}

// fanOutObserver.OnDispatchFailure must call BOTH fanned-out observers, in
// order — exactly like its siblings OnEnqueue/OnAccept/OnUnconsumedExpired/
// OnDeclined already do (bootCore's one construction site relies on this to
// feed the metrics.Emitter and the activity.Ring from the same queue hook).
func TestFanOutObserver_OnDispatchFailureCallsBoth(t *testing.T) {
	a := &recordingDispatchFailureObserver{}
	b := &recordingDispatchFailureObserver{}
	f := fanOutObserver{a, b}

	f.OnDispatchFailure("review-requested")

	if !reflect.DeepEqual(a.dispatchFailed, []string{"review-requested"}) {
		t.Fatalf("a.dispatchFailed = %v, want [review-requested]", a.dispatchFailed)
	}
	if !reflect.DeepEqual(b.dispatchFailed, []string{"review-requested"}) {
		t.Fatalf("b.dispatchFailed = %v, want [review-requested]", b.dispatchFailed)
	}
}

// flakySourceQuery is a minimal pull-source query.Query stand-in (mirrors
// internal/discover's own unexported flakyQuery, copied here since that one is
// package-private, the same convention selTestQuery above already follows):
// it fails its first failTimes Run calls, then succeeds. calls counts every
// Run invocation via a shared pointer so it survives Source.Query's by-value
// interface storage.
type flakySourceQuery struct {
	query.Meta
	failTimes int
	calls     *int
}

func (f flakySourceQuery) Validate() error        { return nil }
func (f flakySourceQuery) BackingCommand() string { return "" }
func (f flakySourceQuery) Run(context.Context, query.Env) ([]event.Event, error) {
	*f.calls++
	if *f.calls <= f.failTimes {
		return nil, errors.New("source unavailable")
	}
	return nil, nil
}

// TestBootCore_wiresMetricsEmitterAsProduceTickSourceFailureObserver proves
// the production wiring bootCore now performs (INV-FAIL-3, register gap R21 /
// bead pg2-00jpn): a pull-source query that fails and retries drives the SAME
// metrics.Emitter bootCore constructs, via o.SourceFailureObserver threaded
// through Orchestrator.ProduceTick's discover.Produce call — so
// MetricSourceFailures actually increments in the running binary, closing the
// gap where source failures were recorded to logs only (discover.go's
// runAndEnqueue Warn line existed; nothing fed its metrics half).
func TestBootCore_wiresMetricsEmitterAsProduceTickSourceFailureObserver(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	calls := 0
	cfg := config.Config{
		LogDir:        shortDir(t), // AF_UNIX path length cap; see shortDir's doc (ingest_event_test.go)
		MeterProvider: mp,
		Queries: query.SourceSet{
			{Name: "flaky-src", Query: flakySourceQuery{
				Meta: query.Meta{
					EmitTypes: []string{"t1"},
					FB: query.FailureBackoff{
						Policy:  backoff.Policy{Initial: time.Millisecond, Factor: 2, Max: time.Millisecond},
						Retries: 1,
					},
				},
				failTimes: 1, // fails once, succeeds on the 2nd Run
				calls:     &calls,
			}},
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if _, err := o.ProduceTick(ctx, q); err != nil {
		t.Fatalf("ProduceTick: %v", err)
	}
	if calls != 2 {
		t.Fatalf("Run was called %d times, want 2 (1 failure + 1 success)", calls)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := int64(-1)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricSourceFailures {
				continue
			}
			s, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range s.DataPoints {
				if v, present := dp.Attributes.Value(attribute.Key("source")); present && v.AsString() == "flaky-src" {
					got = dp.Value
				}
			}
		}
	}
	if got != 1 {
		t.Fatalf("%s{source=flaky-src} = %d, want 1 (bootCore must wire the emitter as o.SourceFailureObserver so ProduceTick's retry reaches it)", metrics.MetricSourceFailures, got)
	}
}

// TestBootCore_DefaultMeterProviderWiresReadableMetricsReader proves the
// Task 3.6-prereq value-read-back acceptance criterion end to end at the
// production wiring site: when Config.MeterProvider is unset (the default —
// nothing sets it in production today), bootCore must give the returned
// core.Service a NON-NIL MetricsReader whose Snapshot actually reflects a
// live queue mutation, not the plain no-op provider Config.Meter() itself
// still defaults to (that provider can never be read back — see
// resolveMeterProvider's doc for why bootCore stopped calling cfg.Meter()
// directly).
func TestBootCore_DefaultMeterProviderWiresReadableMetricsReader(t *testing.T) {
	cfg := config.Config{LogDir: shortDir(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, q, _, storeClose, err := bootCore(ctx, cfg, o)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	reader := svc.MetricsReader()
	if reader == nil {
		t.Fatal("MetricsReader() = nil, want a wired read-back handle when Config.MeterProvider is unset")
	}

	future := time.Now().Add(time.Hour)
	if _, err := q.Enqueue(eventqueue.Event{ID: "ev-1", Type: "review-requested", ExpiresAt: future}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	rm, err := reader.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := int64(-1)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metrics.MetricQueueDepth {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				continue
			}
			for _, dp := range g.DataPoints {
				if v, present := dp.Attributes.Value(attribute.Key("type")); present && v.AsString() == "review-requested" {
					got = dp.Value
				}
			}
		}
	}
	if got != 1 {
		t.Fatalf("%s{type=review-requested} via svc.MetricsReader().Snapshot() = %d, want 1 (bootCore's default MeterProvider must be read-back-capable)", metrics.MetricQueueDepth, got)
	}
}

// TestBootCore_ExternalMeterProviderLeavesMetricsReaderNil proves the
// documented degradation: when a deployment binds its OWN external
// MeterProvider (Config.MeterProvider, Task 3.3's binding decision), bootCore
// must use it as-is (unchanged from Task 3.3) and MUST NOT report a
// MetricsReader — the OTel SDK provides no way to retrofit a second reader
// onto an already-constructed provider this function does not own.
func TestBootCore_ExternalMeterProviderLeavesMetricsReaderNil(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	cfg := config.Config{LogDir: shortDir(t), MeterProvider: mp}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, _, gotMP, storeClose, err := bootCore(ctx, cfg, o)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	if gotMP != mp {
		t.Fatalf("bootCore's returned MeterProvider changed identity; want the exact configured one back unmodified")
	}
	if got := svc.MetricsReader(); got != nil {
		t.Fatalf("MetricsReader() = %v, want nil when Config.MeterProvider is externally set", got)
	}
}

// TestBootCore_ThreadsMonitorSubsetsIntoCoreOptions proves Task 3.6-prereq's
// second acceptance criterion end to end: Config.MonitorSubsets (resolved
// from config, BEFORE any mon.read caller ever calls register) actually
// reaches core.Service.Register's resolution, through bootCore's
// monitorSubsetResolverFrom adaptation — the full path Task 3.6's mon.read
// handler will rely on.
func TestBootCore_ThreadsMonitorSubsetsIntoCoreOptions(t *testing.T) {
	cfg := config.Config{
		LogDir:         shortDir(t),
		MonitorSubsets: map[string][]string{"mon-1": {"queue_depth", "unconsumed_expired"}},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	ctx := context.Background()
	svc, _, _, storeClose, err := bootCore(ctx, cfg, o)
	if err != nil {
		t.Fatalf("bootCore: %v", err)
	}
	defer func() { _ = storeClose() }()
	defer func() { _ = svc.Close() }()

	reg, err := svc.Register("mon-1", core.KindMonitor)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !reflect.DeepEqual(reg.Subset, []string{"queue_depth", "unconsumed_expired"}) {
		t.Fatalf("Subset = %v, want [queue_depth unconsumed_expired] (from Config.MonitorSubsets)", reg.Subset)
	}

	unconfigured, err := svc.Register("mon-2", core.KindMonitor)
	if err != nil {
		t.Fatalf("Register mon-2: %v", err)
	}
	if unconfigured.Subset != nil {
		t.Fatalf("Subset = %v, want nil for an id absent from Config.MonitorSubsets", unconfigured.Subset)
	}
}

// activityObserver.OnDispatchFailure (bead pg2-icm3u) must append a
// "dispatch_failed" Entry to the ring — the fourth outcome its own doc
// comment now enumerates, alongside delivered/missed/declined.
func TestActivityObserver_OnDispatchFailureAppendsEntry(t *testing.T) {
	ring := activity.New(4)
	a := newActivityObserver(ring)

	a.OnDispatchFailure("review-requested")

	buf := make([]activity.Entry, 4)
	n, _ := ring.Read(0, buf)
	if n != 1 {
		t.Fatalf("ring entries = %d, want 1", n)
	}
	if buf[0].Type != "review-requested" || buf[0].Outcome != "dispatch_failed" {
		t.Fatalf("entry = %+v, want {Type: review-requested, Outcome: dispatch_failed}", buf[0])
	}
}

// TestResolvedConfigFor_drainAndExitOmitsPollInterval is the run-mode gating
// test [design: Task 3.5 Step 7]: "drain-and-exit" omits PollInterval
// (Task 3.8's eventual tickIntervalMs) from the composed view entirely — a
// nil pointer, not a zero duration — while "long-running" carries it.
func TestResolvedConfigFor_drainAndExitOmitsPollInterval(t *testing.T) {
	cfg := config.Config{PollInterval: 7 * time.Second}

	drain := resolvedConfigFor(cfg, core.RunModeDrainAndExit)
	if drain.PollInterval != nil {
		t.Fatalf("PollInterval = %v, want nil (omitted) in drain-and-exit mode", *drain.PollInterval)
	}

	long := resolvedConfigFor(cfg, core.RunModeLongRunning)
	if long.PollInterval == nil || *long.PollInterval != cfg.PollInterval {
		t.Fatalf("PollInterval = %v, want %v in long-running mode", long.PollInterval, cfg.PollInterval)
	}
}

// TestResolvedConfigFor_countsActiveRolesAndQueries proves the other
// ResolvedConfig fields reflect the post-selector active set, not the
// configuration's full declared set.
func TestResolvedConfigFor_countsActiveRolesAndQueries(t *testing.T) {
	cfg := config.Config{
		RepoRoot:    "/repo",
		BeadsPrefix: "pfx",
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true},
			{Name: "r2", Enabled: false}, // as applySelectors would leave a --disable'd role
		},
		Queries: query.SourceSet{{Name: "q1"}},
	}

	rc := resolvedConfigFor(cfg, core.RunModeLongRunning)
	if rc.RepoRoot != "/repo" || rc.BeadsPrefix != "pfx" {
		t.Fatalf("RepoRoot/BeadsPrefix = %q/%q, want /repo / pfx", rc.RepoRoot, rc.BeadsPrefix)
	}
	if rc.ActiveRoles != 1 {
		t.Fatalf("ActiveRoles = %d, want 1 (only the enabled role)", rc.ActiveRoles)
	}
	if rc.ActiveQueries != 1 {
		t.Fatalf("ActiveQueries = %d, want 1", rc.ActiveQueries)
	}
}

// TestSourceReportsFor_oneReportPerActiveSource proves sourceReportsFor
// reflects cfg.Queries verbatim — the already-post-selector active subset —
// and that an empty set produces nil, not an empty non-nil slice.
func TestSourceReportsFor_oneReportPerActiveSource(t *testing.T) {
	got := sourceReportsFor(query.SourceSet{{Name: "beads-ready"}, {Name: "e2e-source"}})
	want := []core.SourceReport{{Name: "beads-ready"}, {Name: "e2e-source"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sourceReportsFor = %+v, want %+v", got, want)
	}

	if got := sourceReportsFor(nil); got != nil {
		t.Fatalf("sourceReportsFor(nil) = %+v, want nil", got)
	}
}

// TestGateFileInfo_unsetWhenPathEmptyOrAbsent matches
// orchestrator.gated()'s own "" ⇒ never-gated short-circuit, and reports the
// file's mtime when it does exist.
func TestGateFileInfo_unsetWhenPathEmptyOrAbsent(t *testing.T) {
	if got := gateFileInfo(""); got.Set {
		t.Fatalf("gateFileInfo(\"\") = %+v, want unset", got)
	}

	dir := t.TempDir()
	missing := dir + "/no-such-gate"
	if got := gateFileInfo(missing); got.Set {
		t.Fatalf("gateFileInfo(%q) = %+v, want unset (file absent)", missing, got)
	}

	present := dir + "/quota-paused"
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
	got := gateFileInfo(present)
	if !got.Set {
		t.Fatalf("gateFileInfo(%q) = %+v, want Set=true", present, got)
	}
	fi, err := os.Stat(present)
	if err != nil {
		t.Fatalf("stat gate file: %v", err)
	}
	if !got.Mtime.Equal(fi.ModTime()) {
		t.Fatalf("Mtime = %v, want %v", got.Mtime, fi.ModTime())
	}
}

// TestCurrentGateFiles_namesBothFileDirectGates proves currentGateFiles
// reports both file-direct gates (Task 1.2b, ADR 0036) under the fixed
// gateTickKeyQuotaPaused/gateTickKeyCICDDown keys svc.ObserveGateFromTick's caller and,
// eventually, Task 3.9's socket verbs must agree on.
func TestCurrentGateFiles_namesBothFileDirectGates(t *testing.T) {
	dir := t.TempDir()
	quota := dir + "/quota-paused"
	if err := os.WriteFile(quota, nil, 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
	cfg := config.Config{QuotaPaused: quota, CICDDown: dir + "/cicd-down-absent"}

	gates := currentGateFiles(cfg)
	if got := gates[gateTickKeyQuotaPaused]; !got.Set {
		t.Fatalf("gates[%q] = %+v, want Set=true", gateTickKeyQuotaPaused, got)
	}
	if got := gates[gateTickKeyCICDDown]; got.Set {
		t.Fatalf("gates[%q] = %+v, want unset (file absent)", gateTickKeyCICDDown, got)
	}
}

// writeGateFile creates a gate sentinel file under its OWN fresh t.TempDir()
// (deliberately never cfg.LogDir — a gate sentinel is not core state, and
// mixing the two would leave a stray gates-shaped file in a LogDir fixture)
// and returns its path.
func writeGateFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "quota-paused")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunUntilIdleGated_reachableAnswersIngestNoDispatch(t *testing.T) {
	cmd := &fakeCommander{}
	logDir := shortDir(t)
	cfg := config.Config{
		LogDir:      logDir,
		QuotaPaused: writeGateFile(t),
		Roles: roles.RoleSet{
			{Name: "r1", Enabled: true, Type: "command", Binds: []string{"t1"}, Command: &roles.CommandConfig{Argv: []string{"r1-cmd"}}},
		},
	}
	o := &orchestrator.Orchestrator{Cfg: cfg, Cmd: cmd, BD: &dtest.ScriptBD{}, CC: &dtest.FakeCC{}}
	if !o.Gated() {
		t.Fatal("precondition: cfg must be gated (QuotaPaused sentinel present)")
	}

	done := make(chan int, 1)
	go func() { done <- runUntilIdleGated(context.Background(), cfg, o) }()

	var ref core.Ref
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, err := core.Discover(logDir); err == nil {
			ref = r
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ref == (core.Ref{}) {
		t.Fatal("gated run-until-idle never became discoverable; it must still boot the core (INV-LIFE-1)")
	}

	var stdout, stderr strings.Builder
	code := callCore(&stdout, &stderr, ref, core.SubcommandIngestEvent,
		[]byte(`{"schemaVersion":"1","id":"trk-1","events":[{"id":"e1","type":"t1"}]}`))
	if code != conformance.ExitOK {
		t.Fatalf("ingest-event while gated: exit = %d, want 0; stderr=%s", code, stderr.String())
	}
	var reply map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &reply); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", stdout.String(), err)
	}
	if reply["accepted"] != float64(1) {
		t.Fatalf("accepted = %v, want 1 — a gated core must still answer ingest-event and durably enqueue", reply["accepted"])
	}

	exitCode := <-done
	if exitCode != exitOK {
		t.Fatalf("runUntilIdleGated exit = %d, want %d", exitCode, exitOK)
	}
	if len(cmd.calls) != 0 {
		t.Fatalf("gated run-until-idle must dispatch nothing; commander calls = %v", cmd.calls)
	}
}

// TestRunOneTick_gatedStillExpiresDueEvent proves INV-LIFE-2's "Expiry MUST
// continue while gated": a gated tick must still run q.Expire(), even though
// it skips ProduceTick/Dispatch entirely. An orphan event (no listener bound
// to its type) past its ExpiresAt is retained only until SOME Expire() call
// evicts it (eventqueue.Queue.Expire's retainedLocked: an unmatched type is
// vacuously not owed an attempt), so this needs no dispatch/listener setup at
// all to prove the point. RED against the pre-fix code: the gated branch
// never called q.Expire() (or anything else) at all.
func TestRunOneTick_gatedStillExpiresDueEvent(t *testing.T) {
	svc := &core.Service{}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := q.Enqueue(eventqueue.Event{ID: "ev1", Type: "orphan", ExpiresAt: past}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if depth := q.DepthByType()["orphan"]; depth != 1 {
		t.Fatalf("precondition: depth = %d, want 1", depth)
	}

	cfg := config.Config{QuotaPaused: writeGateFile(t)}
	o := &orchestrator.Orchestrator{Cfg: cfg}
	if !o.Gated() {
		t.Fatal("precondition: must be gated")
	}

	var stderr strings.Builder
	runOneTick(context.Background(), cfg, o, svc, q, false, &stderr)

	if depth := q.DepthByType()["orphan"]; depth != 0 {
		t.Fatalf("gated tick must still run q.Expire(): depth = %d, want 0 (due event expired)", depth)
	}
}

// TestRunOneTick_gateNoticeOncePerTransition proves the stderr notice fires
// exactly on the gate-state TRANSITION — including startup-while-gated,
// since the very first call passes wasGated=false regardless of history —
// stays silent across repeat ticks in the SAME gated state, and fires again
// after a clear-and-reset (an ungated tick, then re-gated). RED against the
// pre-fix code: runOneTick/gateNotice did not exist (the old gated branch
// logged an unconditional per-tick slog.Info, never a stderr notice).
func TestRunOneTick_gateNoticeOncePerTransition(t *testing.T) {
	svc := &core.Service{}
	q, err := eventqueue.New(eventqueue.NewMemStore())
	if err != nil {
		t.Fatalf("eventqueue.New: %v", err)
	}
	gatedCfg := config.Config{QuotaPaused: writeGateFile(t)}
	gatedO := &orchestrator.Orchestrator{Cfg: gatedCfg}
	ungatedO := &orchestrator.Orchestrator{}

	const noticeMarker = "pr-pool: gated by"
	var buf strings.Builder
	wasGated := false
	for i := 0; i < 3; i++ {
		wasGated = runOneTick(context.Background(), gatedCfg, gatedO, svc, q, wasGated, &buf)
	}
	if !wasGated {
		t.Fatal("after 3 gated ticks wasGated must be true")
	}
	if n := strings.Count(buf.String(), noticeMarker); n != 1 {
		t.Fatalf("notice count across 3 gated ticks = %d, want exactly 1; output=%q", n, buf.String())
	}

	// clear: one ungated tick resets the transition edge.
	wasGated = runOneTick(context.Background(), config.Config{}, ungatedO, svc, q, wasGated, &buf)
	if wasGated {
		t.Fatal("an ungated tick must report wasGated=false")
	}

	// reset: gate again — a FRESH transition, must notice again.
	wasGated = runOneTick(context.Background(), gatedCfg, gatedO, svc, q, wasGated, &buf)
	if !wasGated {
		t.Fatal("re-gated tick must report wasGated=true")
	}
	if n := strings.Count(buf.String(), noticeMarker); n != 2 {
		t.Fatalf("notice count after clear-and-reset = %d, want exactly 2 total; output=%q", n, buf.String())
	}
}
