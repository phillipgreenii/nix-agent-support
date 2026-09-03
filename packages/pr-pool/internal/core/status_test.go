package core

import (
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/pr-pool/internal/metrics"
	"github.com/phillipgreenii/pr-pool/internal/roles"
)

// TestGateStateNewerObservationWins is the red-first test for Task 3.5 Step 1's
// compare rule: a concurrent tick-stat write with an OLDER observation MUST
// NOT overwrite a socket verb's newer one.
func TestGateStateNewerObservationWins(t *testing.T) {
	var svc Service
	newer := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)

	svc.ObserveGateFromSocketVerb(newer, "quota_paused", GateInfo{Set: true, Owner: "operator"})
	svc.ObserveGateFromTick(older, map[string]GateInfo{"quota_paused": {Set: false}})

	gates, observedAt := svc.GateSnapshot()
	if !observedAt.Equal(newer) {
		t.Fatalf("gatesObservedAt = %v, want unchanged at the socket verb's %v", observedAt, newer)
	}
	if got := gates["quota_paused"]; !got.Set {
		t.Fatalf("quota_paused = %+v, want the socket verb's Set=true to survive the older drive-loop write", got)
	}
}

// TestSocketPauseReflectsImmediately: a socket pause/resume verb write is
// visible to an immediate status read with a fresh gatesObservedAt, and a
// LATER drive-loop tick (still older than the socket write, or simply
// observing a different gate) never reverts it.
func TestSocketPauseReflectsImmediately(t *testing.T) {
	var svc Service
	pauseAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	svc.ObserveGateFromSocketVerb(pauseAt, "cicd_down", GateInfo{Set: true, Mtime: pauseAt, Owner: "operator"})

	gates, observedAt := svc.GateSnapshot()
	if got := gates["cicd_down"]; !got.Set || !got.Mtime.Equal(pauseAt) {
		t.Fatalf("cicd_down = %+v, want an immediate Set=true with Mtime %v", got, pauseAt)
	}
	if !observedAt.Equal(pauseAt) {
		t.Fatalf("gatesObservedAt = %v, want the fresh %v the socket verb just recorded", observedAt, pauseAt)
	}

	// The next drive-loop tick observes an OLDER snapshot of gate-file state
	// (e.g. it started its pass just before the socket write landed) — it
	// must not revert what the socket verb just recorded.
	tickAt := pauseAt.Add(-time.Second)
	svc.ObserveGateFromTick(tickAt, map[string]GateInfo{"cicd_down": {Set: false}})

	gates, observedAt = svc.GateSnapshot()
	if got := gates["cicd_down"]; !got.Set {
		t.Fatalf("cicd_down = %+v, want the socket pause unreverted by the older tick", got)
	}
	if !observedAt.Equal(pauseAt) {
		t.Fatalf("gatesObservedAt = %v, want still %v (the older tick write must drop)", observedAt, pauseAt)
	}
}

// TestFileDirectPauseLagsUntilNextTick: a file-direct pause (Task 1.2b, ADR
// 0036) never calls into Service at all — it can only become visible once the
// drive loop's own next periodic gate-file read calls ObserveGateFromTick. An
// immediate status read in between reports the PRIOR observation with a
// stale gatesObservedAt, flipping only at that next tick.
func TestFileDirectPauseLagsUntilNextTick(t *testing.T) {
	var svc Service
	priorTick := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	svc.ObserveGateFromTick(priorTick, map[string]GateInfo{"quota_paused": {Set: false}})

	// A file-direct pause happens here, out-of-band — nothing calls into svc.

	// An immediate status read still sees the PRIOR (unset) state, stamped
	// with the stale priorTick observation time.
	gates, observedAt := svc.GateSnapshot()
	if got := gates["quota_paused"]; got.Set {
		t.Fatalf("quota_paused = %+v, want the prior unset state until the next tick observes the file", got)
	}
	if !observedAt.Equal(priorTick) {
		t.Fatalf("gatesObservedAt = %v, want the stale %v (unrefreshed until the next tick)", observedAt, priorTick)
	}

	// The next drive-loop tick reads the gate file and observes it set.
	nextTick := priorTick.Add(10 * time.Second)
	svc.ObserveGateFromTick(nextTick, map[string]GateInfo{"quota_paused": {Set: true, Mtime: nextTick.Add(-5 * time.Second)}})

	gates, observedAt = svc.GateSnapshot()
	if got := gates["quota_paused"]; !got.Set {
		t.Fatalf("quota_paused = %+v, want it to flip to set at the next tick", got)
	}
	if !observedAt.Equal(nextTick) {
		t.Fatalf("gatesObservedAt = %v, want the fresh %v", observedAt, nextTick)
	}
}

// TestCurrentTick_nilBeforeFirstPublish is the boot-window test: a freshly
// constructed Service must not panic when status-composing logic touches its
// tick cell before any PublishTick call [design: Task 3.5 Step 4].
func TestCurrentTick_nilBeforeFirstPublish(t *testing.T) {
	var svc Service

	got := svc.CurrentTick()
	if got != nil {
		t.Fatalf("CurrentTick() = %+v, want nil before the first PublishTick", got)
	}

	// Status-composing logic (Task 3.8) must nil-check rather than deref
	// unconditionally; simulate that check here so a regression that removes
	// the nil-check panics this test instead of a live status call.
	var runMode string
	if tick := svc.CurrentTick(); tick != nil {
		runMode = tick.RunMode
	} else {
		runMode = "boot"
	}
	if runMode != "boot" {
		t.Fatalf("runMode = %q, want %q", runMode, "boot")
	}
}

// TestPublishTick_currentTickRoundTrips proves PublishTick/CurrentTick carry
// a value through unchanged, and that a second publish fully replaces the
// first (no partial-merge of stale fields).
func TestPublishTick_currentTickRoundTrips(t *testing.T) {
	var svc Service
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	svc.PublishTick(TickSnapshot{
		RunMode:    RunModeLongRunning,
		Version:    "v1",
		LastTickAt: t0,
		SnapshotAt: t0,
	})
	got := svc.CurrentTick()
	if got == nil || got.RunMode != RunModeLongRunning || got.Version != "v1" {
		t.Fatalf("CurrentTick() = %+v, want the just-published long-running v1 snapshot", got)
	}

	t1 := t0.Add(time.Minute)
	svc.PublishTick(TickSnapshot{
		RunMode:    RunModeDrainAndExit,
		Version:    "v1",
		LastTickAt: t1,
		SnapshotAt: t1,
	})
	got = svc.CurrentTick()
	if got == nil || got.RunMode != RunModeDrainAndExit || !got.LastTickAt.Equal(t1) {
		t.Fatalf("CurrentTick() = %+v, want the second publish to fully replace the first", got)
	}
}

// TestGateSnapshot_returnsIndependentCopy proves the map GateSnapshot hands
// back is a copy: a caller mutating it must not corrupt the Service's own
// cache.
func TestGateSnapshot_returnsIndependentCopy(t *testing.T) {
	var svc Service
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	svc.ObserveGateFromSocketVerb(now, "quota_paused", GateInfo{Set: true})

	gates, _ := svc.GateSnapshot()
	gates["quota_paused"] = GateInfo{Set: false}

	gates2, _ := svc.GateSnapshot()
	if got := gates2["quota_paused"]; !got.Set {
		t.Fatalf("quota_paused = %+v, want the caller's mutation of the returned map to not affect the cache", got)
	}
}

// TestStatusListeners_RoleBindsEnabledExcluded is Task 4.1 Step 1's
// red-first test: composeStatusReply's listeners[] carries the FULL
// declared role set (Options.DeclaredRoles), with `enabled` reflecting the
// CONFIG-level flag and `excluded` computed independently from
// Options.ExcludedRoles (Binding Decision 4) — a config-disabled role
// (enabled=false, never selector-excluded) and a selector-excluded role
// (enabled=true, excluded=true) must render distinctly.
func TestStatusListeners_RoleBindsEnabledExcluded(t *testing.T) {
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
		declaredRoles: []roles.Role{
			{Name: "review", Binds: []string{"review-requested"}, Enabled: true},
			{Name: "worker", Binds: []string{"work-ready"}, Enabled: true},
			{Name: "feedback", Binds: []string{"feedback-ready"}, Enabled: false},
		},
		excludedRoles: []string{"worker"},
	}
	reply := svc.composeStatusReply(0)
	listeners, ok := reply["listeners"].([]map[string]any)
	if !ok || len(listeners) != 3 {
		t.Fatalf("listeners = %v, want 3 entries (the full declared set)", reply["listeners"])
	}
	byRole := make(map[string]map[string]any, len(listeners))
	for _, l := range listeners {
		byRole[l["role"].(string)] = l
	}
	if got := byRole["review"]; got["enabled"] != true || got["excluded"] != false {
		t.Fatalf("review = %+v, want enabled=true excluded=false", got)
	}
	if got := byRole["worker"]; got["enabled"] != true || got["excluded"] != true {
		t.Fatalf("worker = %+v, want enabled=true excluded=true (selector-excluded)", got)
	}
	if got := byRole["feedback"]; got["enabled"] != false || got["excluded"] != false {
		t.Fatalf("feedback = %+v, want enabled=false excluded=false (config-disabled, never selector-excluded)", got)
	}
	if got, ok := byRole["review"]["binds"].([]string); !ok || !reflect.DeepEqual(got, []string{"review-requested"}) {
		t.Fatalf("review.binds = %v, want [review-requested]", byRole["review"]["binds"])
	}
	if byRole["review"]["backoff"] != nil {
		t.Fatalf("review.backoff = %v, want null (no live roleListener reference reaches core)", byRole["review"]["backoff"])
	}
	if byRole["review"]["delivered"] != int64(0) || byRole["review"]["declined"] != int64(0) {
		t.Fatalf("review delivered/declined = %v/%v, want 0/0 (no ListenerCounts wired)", byRole["review"]["delivered"], byRole["review"]["declined"])
	}
}

// TestStatusSources_TypeModeLastTickFailure is Task 4.1 Step 7's red-first
// test: composeStatusReply's sources[] carries type/mode ("pull", always —
// no push query type exists), lastTick (present only for a source THIS
// pass's TickSnapshot.Sources actually fired), and failure (present only
// for a source that failed this pass) — over the full configured set,
// UNION Options.ExcludedSources for a selector-excluded source that has
// already vanished from TickSnapshot.Sources entirely.
func TestStatusSources_TypeModeLastTickFailure(t *testing.T) {
	svc := &Service{
		q:               newQueue(t),
		bindings:        testBindings(),
		reg:             NewRegistry(nil),
		excludedSources: []string{"disabled-src"},
	}
	now := time.Date(2026, 9, 1, 0, 5, 0, 0, time.UTC)
	svc.PublishTick(TickSnapshot{
		Sources: []SourceReport{
			{Name: "active-src", Type: "pull", LastTick: now},
			{Name: "failing-src", Type: "pull", Failure: &FailureInfo{Count: 2, NextEligible: now}},
		},
	})
	reply := svc.composeStatusReply(0)
	sources, ok := reply["sources"].([]map[string]any)
	if !ok || len(sources) != 3 {
		t.Fatalf("sources = %v, want 3 entries (2 active + 1 selector-excluded)", reply["sources"])
	}
	byName := make(map[string]map[string]any, len(sources))
	for _, s := range sources {
		byName[s["name"].(string)] = s
	}

	active := byName["active-src"]
	if active["type"] != "pull" || active["mode"] != "pull" || active["enabled"] != true || active["excluded"] != false {
		t.Fatalf("active-src = %+v, want type/mode=pull enabled=true excluded=false", active)
	}
	if want := now.UTC().Format(time.RFC3339Nano); active["lastTick"] != want {
		t.Fatalf("active-src.lastTick = %v, want %v", active["lastTick"], want)
	}
	if active["failure"] != nil {
		t.Fatalf("active-src.failure = %v, want nil (no failure this pass)", active["failure"])
	}

	failing := byName["failing-src"]
	failureMap, ok := failing["failure"].(map[string]any)
	if !ok || failureMap["count"] != 2 {
		t.Fatalf("failing-src.failure = %v, want {count:2, nextEligible:...}", failing["failure"])
	}
	if _, present := failing["lastTick"]; present {
		t.Fatalf("failing-src.lastTick = %v, want omitted (never fired this pass)", failing["lastTick"])
	}

	excluded := byName["disabled-src"]
	if excluded["enabled"] != true || excluded["excluded"] != true {
		t.Fatalf("disabled-src = %+v, want enabled=true excluded=true", excluded)
	}
	if _, present := excluded["lastTick"]; present {
		t.Fatalf("disabled-src.lastTick = %v, want omitted (selector-excluded; never fired)", excluded["lastTick"])
	}
	if excluded["failure"] != nil {
		t.Fatalf("disabled-src.failure = %v, want nil", excluded["failure"])
	}
}

// TestStatusResolvedConfig_PerParticipantPresentButEmpty is Task 4.1 Step
// 11's red-first test (operator-widened scope, Binding Decision 7):
// statusResolvedConfig's output always carries a perParticipant key
// holding map[string]any{} — present and empty, never omitted or nil.
func TestStatusResolvedConfig_PerParticipantPresentButEmpty(t *testing.T) {
	got := statusResolvedConfig(ResolvedConfig{RepoRoot: "/repo", ActiveRoles: 1, ActiveQueries: 1})
	pp, ok := got["perParticipant"]
	if !ok {
		t.Fatal("perParticipant key absent, want always present")
	}
	m, ok := pp.(map[string]any)
	if !ok || len(m) != 0 {
		t.Fatalf("perParticipant = %#v, want an empty, non-nil map[string]any{}", pp)
	}
}

// TestStatusCounters_MirrorsMetricCatalogVerbatim is Task 4.1 Step 13's
// red-first test (Binding Decision 7): counters reads back VERBATIM from
// the already-landed internal/metrics.Emitter/MetricsReader mechanism —
// the exact values mon.read would independently report for
// pr_pool.unconsumed_expired/unknown_type_rejected/deduped — proving
// "never a second, divergently-counted set" by construction.
func TestStatusCounters_MirrorsMetricCatalogVerbatim(t *testing.T) {
	mp, reader := metrics.NewReadableProvider()
	emitter, err := metrics.New(mp, func() map[string]int { return nil })
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	emitter.OnUnconsumedExpired("t1")
	emitter.OnUnknownTypeRejected("t2")
	emitter.OnDeduped("t3")

	svc := &Service{
		q:             newQueue(t),
		bindings:      testBindings(),
		reg:           NewRegistry(nil),
		metricsReader: reader,
	}
	reply := svc.composeStatusReply(0)
	counters, ok := reply["counters"].(map[string]any)
	if !ok {
		t.Fatalf("counters absent = %v, want present when MetricsReader != nil", reply["counters"])
	}
	want := map[string]any{
		"unconsumedExpired":   map[string]int64{"t1": 1},
		"unknownTypeRejected": map[string]int64{"t2": 1},
		"deduped":             map[string]int64{"t3": 1},
	}
	if !reflect.DeepEqual(counters, want) {
		t.Fatalf("counters = %+v, want %+v", counters, want)
	}
}

// TestStatusCounters_NilMetricsReaderOmitsKey is Task 4.1 Step 14's
// red-first test: a Service built with MetricsReader left nil never sets
// the `counters` key at all — not nil, not {}, absent — matching
// resolvedConfig's own tick-nil omission idiom.
func TestStatusCounters_NilMetricsReaderOmitsKey(t *testing.T) {
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
	}
	reply := svc.composeStatusReply(0)
	if _, present := reply["counters"]; present {
		t.Fatalf("counters = %v, want omitted entirely when MetricsReader is nil", reply["counters"])
	}
}
