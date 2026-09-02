package core

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/phillipgreenii/pr-pool/conformance"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// A fresh registration starts in `starting` / `healthy` — the core must not route
// to it before it says it is started (INV-INTF-1).
func TestRegistry_RegisterStartsUnroutable(t *testing.T) {
	at := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	r := NewRegistry(fixedClock(at))

	reg, err := r.Register("src-beads", KindSource, "pr-pool ingest-event --socket s --token t", "pr-pool self-status --socket s --token t")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.State != conformance.Starting {
		t.Fatalf("state = %s, want starting", reg.State)
	}
	if reg.Self != SelfHealthy {
		t.Fatalf("self = %s, want healthy", reg.Self)
	}
	if !reg.RegisteredAt.Equal(at) || !reg.UpdatedAt.Equal(at) {
		t.Fatalf("timestamps = %v/%v, want the injected clock %v", reg.RegisteredAt, reg.UpdatedAt, at)
	}
	if r.Available("src-beads") {
		t.Fatal("Available = true while starting; the core must not route yet")
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1", r.Len())
	}
}

func TestRegistry_RejectsInvalidRegistrations(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("", KindSource, "", ""); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("empty id err = %v, want ErrInvalidRegistration", err)
	}
	if _, err := r.Register("x", Kind("wat"), "", ""); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("unknown kind err = %v, want ErrInvalidRegistration", err)
	}
}

// A participant that crashed and came back re-registers under the same id; the
// fresh registration must replace the stale one rather than be refused.
func TestRegistry_ReRegisterReplaces(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("h1", KindHandler, "cb-old", "self-old"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetLifecycle("h1", conformance.Started); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if _, err := r.Register("h1", KindHandler, "cb-new", "self-new"); err != nil {
		t.Fatalf("re-Register: %v", err)
	}
	got, ok := r.Get("h1")
	if !ok {
		t.Fatal("registration vanished")
	}
	if got.State != conformance.Starting {
		t.Fatalf("state = %s, want the re-registration to reset to starting", got.State)
	}
	if got.Callback != "cb-new" {
		t.Fatalf("callback = %q, want the new one", got.Callback)
	}
	if got.SelfStatusCallback != "self-new" {
		t.Fatalf("self-status callback = %q, want the new one", got.SelfStatusCallback)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (replace, not append)", r.Len())
	}
}

func TestRegistry_LifecycleAndAvailability(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("h1", KindHandler, "", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetLifecycle("h1", conformance.Started); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if !r.Available("h1") {
		t.Fatal("Available = false for a started, healthy participant")
	}
	// `degraded` is a quality warning, not a refusal — still routable.
	if err := r.SetSelfStatus("h1", SelfDegraded); err != nil {
		t.Fatalf("SetSelfStatus: %v", err)
	}
	if !r.Available("h1") {
		t.Fatal("Available = false for a degraded participant; degraded is still routable")
	}
	// `unavailable` is a PRE-ACCEPT decline: the core re-offers while unexpired.
	if err := r.SetSelfStatus("h1", SelfUnavailable); err != nil {
		t.Fatalf("SetSelfStatus: %v", err)
	}
	if r.Available("h1") {
		t.Fatal("Available = true for an unavailable participant (INV-FAIL-1 pre-accept decline)")
	}
	// Leaving `started` also makes it unroutable.
	if err := r.SetSelfStatus("h1", SelfHealthy); err != nil {
		t.Fatalf("SetSelfStatus: %v", err)
	}
	if err := r.SetLifecycle("h1", conformance.Stopping); err != nil {
		t.Fatalf("SetLifecycle: %v", err)
	}
	if r.Available("h1") {
		t.Fatal("Available = true while stopping (INV-INTF-1)")
	}
}

func TestRegistry_UnknownParticipant(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.SetLifecycle("nope", conformance.Started); !errors.Is(err, ErrUnknownParticipant) {
		t.Fatalf("SetLifecycle err = %v, want ErrUnknownParticipant", err)
	}
	if err := r.SetSelfStatus("nope", SelfHealthy); !errors.Is(err, ErrUnknownParticipant) {
		t.Fatalf("SetSelfStatus err = %v, want ErrUnknownParticipant", err)
	}
	if err := r.SetSubset("nope", []string{"queue_depth"}); !errors.Is(err, ErrUnknownParticipant) {
		t.Fatalf("SetSubset err = %v, want ErrUnknownParticipant", err)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("Get returned an entry for an unregistered id")
	}
	if r.Available("nope") {
		t.Fatal("Available = true for an unregistered id")
	}
}

// SetSubset (Task 3.6-prereq) is a plain follow-up field update, the same
// shape as SetLifecycle/SetSelfStatus: it records the metric catalog subset
// a kind=monitor registration may read via mon.read, without disturbing the
// rest of the entry.
func TestRegistry_SetSubset(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("m1", KindMonitor, "", "self-cb"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetSubset("m1", []string{"queue_depth", "unconsumed_expired"}); err != nil {
		t.Fatalf("SetSubset: %v", err)
	}
	got, ok := r.Get("m1")
	if !ok {
		t.Fatal("registration vanished")
	}
	if !reflect.DeepEqual(got.Subset, []string{"queue_depth", "unconsumed_expired"}) {
		t.Fatalf("Subset = %v, want [queue_depth unconsumed_expired]", got.Subset)
	}
	if got.SelfStatusCallback != "self-cb" {
		t.Fatalf("SetSubset disturbed an unrelated field: SelfStatusCallback = %q", got.SelfStatusCallback)
	}
}

func TestRegistry_SetSelfStatusRejectsUnknownValue(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("h1", KindHandler, "", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.SetSelfStatus("h1", SelfStatus("mostly-fine")); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("err = %v, want ErrInvalidRegistration (report, do not guess)", err)
	}
}

// Deregistering is idempotent: `stopped` and `crashing` can both reach it.
func TestRegistry_Deregister(t *testing.T) {
	r := NewRegistry(nil)
	if _, err := r.Register("s1", KindSource, "", ""); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !r.Deregister("s1") {
		t.Fatal("Deregister = false for a registered participant")
	}
	if r.Deregister("s1") {
		t.Fatal("second Deregister = true, want a no-op false")
	}
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
}

// List is sorted so any view built on it is deterministic.
func TestRegistry_ListIsSorted(t *testing.T) {
	r := NewRegistry(nil)
	for _, id := range []string{"zeta", "alpha", "mid"} {
		if _, err := r.Register(id, KindSource, "", ""); err != nil {
			t.Fatalf("Register %s: %v", id, err)
		}
	}
	got := r.List()
	ids := make([]string, len(got))
	for i, g := range got {
		ids[i] = g.ID
	}
	if strings.Join(ids, ",") != "alpha,mid,zeta" {
		t.Fatalf("List order = %v, want sorted by id", ids)
	}
}

// The registry is touched from many goroutines (the accept loop), so it must be
// race-free under -race.
func TestRegistry_ConcurrentUse(t *testing.T) {
	r := NewRegistry(nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i%5))
			if _, err := r.Register(id, KindSource, "", ""); err != nil {
				t.Errorf("Register: %v", err)
				return
			}
			_ = r.SetLifecycle(id, conformance.Started)
			_ = r.SetSelfStatus(id, SelfDegraded)
			_ = r.Available(id)
			_ = r.List()
			_ = r.Len()
		}(i)
	}
	wg.Wait()
}

func TestParseSelfStatusAndKind(t *testing.T) {
	for _, s := range []string{"healthy", "degraded", "unavailable"} {
		if got, err := ParseSelfStatus(s); err != nil || string(got) != s {
			t.Fatalf("ParseSelfStatus(%q) = %q, %v", s, got, err)
		}
	}
	if _, err := ParseSelfStatus("busy"); err == nil {
		t.Fatal("ParseSelfStatus accepted an undeclared value")
	}
	for _, s := range []string{"source", "handler", "monitor", "storage"} {
		if got, err := ParseKind(s); err != nil || string(got) != s {
			t.Fatalf("ParseKind(%q) = %q, %v", s, got, err)
		}
	}
	if _, err := ParseKind("operator"); err == nil {
		t.Fatal("ParseKind accepted an undeclared kind")
	}
}

// Registering through the SERVICE hands a source its ingest-event callback with
// the socket and token baked in; a handler gets none, because session-status is
// dropped and acceptance arrives in the dispatch reply instead. EVERY kind, the
// source included, also gets the self-status callback (bead pg2-zaghi:
// interfaces.md "Self-status" is "any participant", not sources only).
func TestService_RegisterHandsOutTheCallback(t *testing.T) {
	svc := &Service{
		state:   conformance.Started,
		reg:     NewRegistry(nil),
		command: "pr-pool",
		ref:     Ref{Socket: "/s/core.sock", Token: "tok"},
	}
	wantSelfStatus := `pr-pool self-status --socket '/s/core.sock' --token 'tok'`
	src, err := svc.Register("src-1", KindSource)
	if err != nil {
		t.Fatalf("Register source: %v", err)
	}
	want := `pr-pool ingest-event --socket '/s/core.sock' --token 'tok'`
	if src.Callback != want {
		t.Fatalf("source callback = %q, want %q", src.Callback, want)
	}
	if src.SelfStatusCallback != wantSelfStatus {
		t.Fatalf("source self-status callback = %q, want %q", src.SelfStatusCallback, wantSelfStatus)
	}
	for _, kind := range []Kind{KindHandler, KindMonitor, KindStorage} {
		reg, err := svc.Register("p-"+string(kind), kind)
		if err != nil {
			t.Fatalf("Register %s: %v", kind, err)
		}
		if reg.Callback != "" {
			t.Fatalf("%s callback = %q, want empty (no event-delivery callback target)", kind, reg.Callback)
		}
		if reg.SelfStatusCallback != wantSelfStatus {
			t.Fatalf("%s self-status callback = %q, want %q (every kind gets one)", kind, reg.SelfStatusCallback, wantSelfStatus)
		}
	}
	if svc.Registry().Len() != 4 {
		t.Fatalf("registry len = %d, want 4", svc.Registry().Len())
	}
}

// Registering as kind=monitor resolves the caller's metric catalog subset
// from the configured MonitorSubsetResolver and records it on the
// Registration BEFORE Register returns (Task 3.6-prereq / Task 3.6 Binding
// decisions: "resolved BEFORE it ever calls register... looked up from
// config by registration id, not carried on the mon.read request itself").
// Every OTHER kind's Subset stays empty even though the resolver would
// return something for its id too — the resolver is consulted ONLY for
// KindMonitor.
func TestService_RegisterResolvesMonitorSubsetForMonitorKindOnly(t *testing.T) {
	resolver := MonitorSubsetResolver(func(id string) []string {
		return map[string][]string{
			"mon-1": {"queue_depth", "unconsumed_expired"},
			"h1":    {"should-never-be-consulted"},
		}[id]
	})
	svc := &Service{
		state:          conformance.Started,
		reg:            NewRegistry(nil),
		command:        "pr-pool",
		ref:            Ref{Socket: "/s/core.sock", Token: "tok"},
		monitorSubsets: resolver,
	}

	mon, err := svc.Register("mon-1", KindMonitor)
	if err != nil {
		t.Fatalf("Register monitor: %v", err)
	}
	if !reflect.DeepEqual(mon.Subset, []string{"queue_depth", "unconsumed_expired"}) {
		t.Fatalf("monitor Subset = %v, want [queue_depth unconsumed_expired]", mon.Subset)
	}

	handler, err := svc.Register("h1", KindHandler)
	if err != nil {
		t.Fatalf("Register handler: %v", err)
	}
	if handler.Subset != nil {
		t.Fatalf("handler Subset = %v, want nil (resolver must not be consulted for a non-monitor kind)", handler.Subset)
	}

	// Get agrees with what Register returned — the subset actually landed in
	// the registry, not just in Register's return value.
	got, ok := svc.Registry().Get("mon-1")
	if !ok || !reflect.DeepEqual(got.Subset, mon.Subset) {
		t.Fatalf("Registry().Get(mon-1).Subset = %v, ok=%v; want %v, true", got.Subset, ok, mon.Subset)
	}
}

// A Service with no MonitorSubsetResolver configured (the production
// default when Config.MonitorSubsets is unset) must resolve every
// kind=monitor registration to an empty subset, not panic.
func TestService_RegisterMonitorSubsetDefaultsToEmptyWithNoResolver(t *testing.T) {
	svc := &Service{
		state:   conformance.Started,
		reg:     NewRegistry(nil),
		command: "pr-pool",
		ref:     Ref{Socket: "/s/core.sock", Token: "tok"},
	}
	mon, err := svc.Register("mon-1", KindMonitor)
	if err != nil {
		t.Fatalf("Register monitor: %v", err)
	}
	if mon.Subset != nil {
		t.Fatalf("Subset = %v, want nil with no MonitorSubsetResolver configured", mon.Subset)
	}
}

// Service.MetricsReader (Task 3.6-prereq) returns exactly the handle Options
// wired in at Listen, and nil when none was configured — the "no read-back
// capability wired" case documented on Options.MetricsReader.
func TestService_MetricsReader(t *testing.T) {
	stub := stubMetricsReader{}
	withReader := &Service{metricsReader: stub}
	if got := withReader.MetricsReader(); got != stub {
		t.Fatalf("MetricsReader() = %v, want the configured stub", got)
	}

	withoutReader := &Service{}
	if got := withoutReader.MetricsReader(); got != nil {
		t.Fatalf("MetricsReader() = %v, want nil when none configured", got)
	}
}

type stubMetricsReader struct{}

func (stubMetricsReader) Snapshot(context.Context) (metricdata.ResourceMetrics, error) {
	return metricdata.ResourceMetrics{}, nil
}
