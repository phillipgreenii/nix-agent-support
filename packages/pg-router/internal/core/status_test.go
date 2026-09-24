package core

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/conformance"
	"github.com/phillipgreenii/pg-router/internal/metrics"
	"github.com/phillipgreenii/pg-router/internal/roles"
)

// TestListenerCounts_BumpDeclinedReasonConcurrentSameReason proves
// BumpDeclinedReason (bead pg2-j4uwg) is safe for concurrent callers
// incrementing the SAME reason's counter — the race LoadOrStore's
// "insert-at-most-once, then atomic.Int64.Add" design exists to avoid (two
// goroutines racing the FIRST bump of a brand-new reason key).
func TestListenerCounts_BumpDeclinedReasonConcurrentSameReason(t *testing.T) {
	c := &ListenerCounts{}
	var wg sync.WaitGroup
	const n = 100
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.BumpDeclinedReason("at-capacity")
		}()
	}
	wg.Wait()
	got := c.DeclinedByReasonSnapshot()
	if got["at-capacity"] != int64(n) {
		t.Fatalf("declinedByReason[at-capacity] = %d, want %d", got["at-capacity"], n)
	}
}

// TestListenerCounts_DeclinedByReasonSnapshotIsIndependentCopy proves
// DeclinedByReasonSnapshot returns a plain map a caller may hold onto
// without racing further Bump calls into the same reason key.
func TestListenerCounts_DeclinedByReasonSnapshotIsIndependentCopy(t *testing.T) {
	c := &ListenerCounts{}
	c.BumpDeclinedReason("at-capacity")
	snap := c.DeclinedByReasonSnapshot()
	c.BumpDeclinedReason("at-capacity")
	if snap["at-capacity"] != 1 {
		t.Fatalf("snapshot[at-capacity] = %d, want 1 (a later Bump must not retroactively change an already-taken snapshot)", snap["at-capacity"])
	}
	if got := c.DeclinedByReasonSnapshot()["at-capacity"]; got != 2 {
		t.Fatalf("a FRESH snapshot[at-capacity] = %d, want 2", got)
	}
}

// TestGateStateNewerObservationWins is the red-first test for Task 3.5 Step 1's
// compare rule: a concurrent tick-stat write with an OLDER observation MUST
// NOT overwrite a socket verb's newer one.
func TestGateStateNewerObservationWins(t *testing.T) {
	var svc Service
	newer := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)

	svc.ObserveGateFromSocketVerb(newer, "operator_paused", GateInfo{Set: true, Owner: "operator"})
	svc.ObserveGateFromTick(older, map[string]GateInfo{"operator_paused": {Set: false}})

	gates, observedAt := svc.GateSnapshot()
	if !observedAt.Equal(newer) {
		t.Fatalf("gatesObservedAt = %v, want unchanged at the socket verb's %v", observedAt, newer)
	}
	if got := gates["operator_paused"]; !got.Set {
		t.Fatalf("operator_paused = %+v, want the socket verb's Set=true to survive the older drive-loop write", got)
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
	svc.ObserveGateFromTick(priorTick, map[string]GateInfo{"operator_paused": {Set: false}})

	// A file-direct pause happens here, out-of-band — nothing calls into svc.

	// An immediate status read still sees the PRIOR (unset) state, stamped
	// with the stale priorTick observation time.
	gates, observedAt := svc.GateSnapshot()
	if got := gates["operator_paused"]; got.Set {
		t.Fatalf("operator_paused = %+v, want the prior unset state until the next tick observes the file", got)
	}
	if !observedAt.Equal(priorTick) {
		t.Fatalf("gatesObservedAt = %v, want the stale %v (unrefreshed until the next tick)", observedAt, priorTick)
	}

	// The next drive-loop tick reads the gate file and observes it set.
	nextTick := priorTick.Add(10 * time.Second)
	svc.ObserveGateFromTick(nextTick, map[string]GateInfo{"operator_paused": {Set: true, Mtime: nextTick.Add(-5 * time.Second)}})

	gates, observedAt = svc.GateSnapshot()
	if got := gates["operator_paused"]; !got.Set {
		t.Fatalf("operator_paused = %+v, want it to flip to set at the next tick", got)
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
	svc.ObserveGateFromSocketVerb(now, "operator_paused", GateInfo{Set: true})

	gates, _ := svc.GateSnapshot()
	gates["operator_paused"] = GateInfo{Set: false}

	gates2, _ := svc.GateSnapshot()
	if got := gates2["operator_paused"]; !got.Set {
		t.Fatalf("operator_paused = %+v, want the caller's mutation of the returned map to not affect the cache", got)
	}
}

// TestListenerCounts_LastDeliveredAtNanos_ZeroUntilSet proves the zero
// value of ListenerCounts' new LastDeliveredAtNanos field (Task 2) is 0 --
// "never delivered" -- until listenerCountObserver.OnAccept sets it.
func TestListenerCounts_LastDeliveredAtNanos_ZeroUntilSet(t *testing.T) {
	c := &ListenerCounts{}
	if got := c.LastDeliveredAtNanos.Load(); got != 0 {
		t.Fatalf("zero value LastDeliveredAtNanos = %d, want 0", got)
	}
}

// TestStatusListeners_SelfReportStateJoinsByRoleName proves statusListeners'
// new selfReportState key (Task 2, folding in the retired Registry pane's
// one useful signal) joins a Registration onto its matching declared role
// by Registration.ID == role name.
func TestStatusListeners_SelfReportStateJoinsByRoleName(t *testing.T) {
	declared := []roles.Role{{Name: "df-feedback", Enabled: true, Binds: []string{"pr.changed"}}}
	counts := map[string]*ListenerCounts{"df-feedback": {}}
	// Registration.State is conformance.Lifecycle (an int-based Stringer,
	// internal/conformance's top-level conformance package,
	// conformance/transport.go:14), not a plain string -- conformance.
	// Started is the constant whose .String() is "started" (matching the
	// existing pattern at statusRegistrations' own `"state": r.State.
	// String()`).
	regs := []Registration{{ID: "df-feedback", Kind: "handler", State: conformance.Started}}

	rows := statusListeners(declared, nil, counts, regs)
	if got := rows[0]["selfReportState"]; got != "started" {
		t.Fatalf("selfReportState = %v, want %q", got, "started")
	}
}

// TestStatusListeners_SelfReportStateEmptyWhenNeverRegistered proves a
// declared role with no matching Registration reports the empty string,
// never a missing key.
func TestStatusListeners_SelfReportStateEmptyWhenNeverRegistered(t *testing.T) {
	declared := []roles.Role{{Name: "df-feedback", Enabled: true, Binds: []string{"pr.changed"}}}
	counts := map[string]*ListenerCounts{"df-feedback": {}}

	rows := statusListeners(declared, nil, counts, nil)
	if got := rows[0]["selfReportState"]; got != "" {
		t.Fatalf("selfReportState = %v, want empty string", got)
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
	reviewCounts := &ListenerCounts{}
	reviewCounts.Delivered.Store(5)
	reviewCounts.Declined.Store(2)
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
		declaredRoles: []roles.Role{
			{Name: "review", Binds: []string{"review-requested"}, Enabled: true},
			{Name: "worker", Binds: []string{"work-ready"}, Enabled: true},
			{Name: "feedback", Binds: []string{"feedback-ready"}, Enabled: false},
		},
		excludedRoles:  []string{"worker"},
		listenerCounts: map[string]*ListenerCounts{"review": reviewCounts},
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
	if byRole["review"]["delivered"] != int64(5) || byRole["review"]["declined"] != int64(2) {
		t.Fatalf("review delivered/declined = %v/%v, want 5/2 (from ListenerCounts)", byRole["review"]["delivered"], byRole["review"]["declined"])
	}
	if byRole["worker"]["delivered"] != int64(0) || byRole["worker"]["declined"] != int64(0) {
		t.Fatalf("worker delivered/declined = %v/%v, want 0/0 (no ListenerCounts entry for it)", byRole["worker"]["delivered"], byRole["worker"]["declined"])
	}
}

// TestStatusListeners_DeclinedByReasonBreaksDownDeclined proves
// composeStatusReply's listeners[] carries the OPTIONAL declinedByReason
// breakdown (bead pg2-j4uwg) alongside the pre-existing flat declined total
// — additive, never a replacement, and empty (not absent) for a role with
// no ListenerCounts entry at all.
func TestStatusListeners_DeclinedByReasonBreaksDownDeclined(t *testing.T) {
	reviewCounts := &ListenerCounts{}
	reviewCounts.Delivered.Store(5)
	reviewCounts.Declined.Store(3)
	reviewCounts.BumpDeclinedReason("at-capacity")
	reviewCounts.BumpDeclinedReason("at-capacity")
	reviewCounts.BumpDeclinedReason("capacity-unknown")
	svc := &Service{
		q:        newQueue(t),
		bindings: testBindings(),
		reg:      NewRegistry(nil),
		declaredRoles: []roles.Role{
			{Name: "review", Binds: []string{"review-requested"}, Enabled: true},
			{Name: "worker", Binds: []string{"work-ready"}, Enabled: true},
		},
		listenerCounts: map[string]*ListenerCounts{"review": reviewCounts},
	}
	reply := svc.composeStatusReply(0)
	listeners, ok := reply["listeners"].([]map[string]any)
	if !ok || len(listeners) != 2 {
		t.Fatalf("listeners = %v, want 2 entries", reply["listeners"])
	}
	byRole := make(map[string]map[string]any, len(listeners))
	for _, l := range listeners {
		byRole[l["role"].(string)] = l
	}
	reviewByReason, ok := byRole["review"]["declinedByReason"].(map[string]int64)
	if !ok {
		t.Fatalf("review.declinedByReason = %v (%T), want map[string]int64", byRole["review"]["declinedByReason"], byRole["review"]["declinedByReason"])
	}
	if reviewByReason["at-capacity"] != 2 || reviewByReason["capacity-unknown"] != 1 {
		t.Fatalf("review.declinedByReason = %v, want at-capacity=2 capacity-unknown=1", reviewByReason)
	}
	var sum int64
	for _, v := range reviewByReason {
		sum += v
	}
	if sum != byRole["review"]["declined"].(int64) {
		t.Fatalf("declinedByReason sums to %d, want it to equal the flat declined total %v", sum, byRole["review"]["declined"])
	}
	workerByReason, ok := byRole["worker"]["declinedByReason"].(map[string]int64)
	if !ok || len(workerByReason) != 0 {
		t.Fatalf("worker.declinedByReason = %v, want an EMPTY (not absent/nil) map — no ListenerCounts entry for it", byRole["worker"]["declinedByReason"])
	}
}

// TestStatusListeners_SortedByRoleName is bead pg2-d1sem's regression test:
// statusListeners must sort its output by Role.Name, never leave it in the
// config's own declaration order. declared is deliberately given "zeta"
// before "alpha" (and a middle "mid") to prove the fix, not just an
// already-sorted input.
func TestStatusListeners_SortedByRoleName(t *testing.T) {
	declared := []roles.Role{
		{Name: "zeta", Enabled: true},
		{Name: "alpha", Enabled: true},
		{Name: "mid", Enabled: true},
	}

	rows := statusListeners(declared, nil, nil, nil)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	got := []string{rows[0]["role"].(string), rows[1]["role"].(string), rows[2]["role"].(string)}
	want := []string{"alpha", "mid", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("role order = %v, want alphabetical %v", got, want)
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

// TestStatusSources_MergedAndSortedByName is bead pg2-d1sem's regression
// test: statusSources must merge active and excluded sources into ONE
// alphabetical-by-name list, never two sequential unsorted groups (active
// then excluded). The excluded source "alpha-excluded" is deliberately
// alphabetically BEFORE the active "zeta-active" (and vice versa for
// "beta-excluded" after "yankee-active"), so a naive active-then-excluded
// concatenation would fail this ordering check.
func TestStatusSources_MergedAndSortedByName(t *testing.T) {
	active := []SourceReport{
		{Name: "zeta-active", Type: "pull"},
		{Name: "yankee-active", Type: "pull"},
	}
	excludedSources := []string{"alpha-excluded", "beta-excluded"}

	rows := statusSources(active, excludedSources, nil)
	if len(rows) != 4 {
		t.Fatalf("len(rows) = %d, want 4", len(rows))
	}
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = r["name"].(string)
	}
	want := []string{"alpha-excluded", "beta-excluded", "yankee-active", "zeta-active"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("name order = %v, want alphabetical (active+excluded merged) %v", got, want)
	}
	// The excluded/active flags travel with the right row through the sort.
	byName := make(map[string]map[string]any, len(rows))
	for _, r := range rows {
		byName[r["name"].(string)] = r
	}
	if byName["alpha-excluded"]["excluded"] != true {
		t.Fatalf("alpha-excluded.excluded = %v, want true", byName["alpha-excluded"]["excluded"])
	}
	if byName["zeta-active"]["excluded"] != false {
		t.Fatalf("zeta-active.excluded = %v, want false", byName["zeta-active"]["excluded"])
	}
}

// TestStatusQueues_SortedByType is bead pg2-d1sem's regression-only test:
// statusQueues already sorts its output by type via sort.Strings — this
// pins that behavior so a future change can't silently regress it (no
// production code change made for this function).
func TestStatusQueues_SortedByType(t *testing.T) {
	depth := map[string]int{
		"zeta-type":  3,
		"alpha-type": 1,
		"mid-type":   2,
	}

	rows := statusQueues(depth)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	got := []string{rows[0]["type"].(string), rows[1]["type"].(string), rows[2]["type"].(string)}
	want := []string{"alpha-type", "mid-type", "zeta-type"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("type order = %v, want alphabetical %v", got, want)
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
	if _, present := got["pollIntervalMs"]; present {
		t.Fatalf("pollIntervalMs = %v, want omitted when PollInterval is nil", got["pollIntervalMs"])
	}

	// A non-nil PollInterval is echoed back as pollIntervalMs (Task 3.5 Step
	// 7's pre-existing branch, unchanged by this widening) — carries perParticipant too.
	pi := 10 * time.Second
	got2 := statusResolvedConfig(ResolvedConfig{RepoRoot: "/repo", PollInterval: &pi})
	if got2["pollIntervalMs"] != int64(10000) {
		t.Fatalf("pollIntervalMs = %v, want 10000", got2["pollIntervalMs"])
	}
	if _, ok := got2["perParticipant"].(map[string]any); !ok {
		t.Fatalf("perParticipant = %#v, want present alongside pollIntervalMs too", got2["perParticipant"])
	}
}

// TestStatusCounters_MirrorsMetricCatalogVerbatim is Task 4.1 Step 13's
// red-first test (Binding Decision 7): counters reads back VERBATIM from
// the already-landed internal/metrics.Emitter/MetricsReader mechanism —
// the exact values mon.read would independently report for
// pg_router_unconsumed_expired/unknown_type_rejected/deduped — proving
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

// TestStatusGates_DisabledOmittedWhenFalse locks bead pg2-efbb0's wire
// convention for GateInfo.Disabled: omitted entirely (never `"disabled":
// false`) for a gate that does not carry the external kill switch, so an
// existing reply consumer sees byte-identical output — the SAME
// omit-when-absent convention mtime/owner already use.
func TestStatusGates_DisabledOmittedWhenFalse(t *testing.T) {
	out := statusGates(map[string]GateInfo{"operator_paused": {Set: true}})
	if len(out) != 1 {
		t.Fatalf("statusGates returned %d entries, want 1", len(out))
	}
	if _, present := out[0]["disabled"]; present {
		t.Errorf("entry = %+v, want no \"disabled\" key when Disabled is false", out[0])
	}
}

// TestStatusGates_DisabledPresentWhenTrue is the positive counterpart: a
// gate with Disabled: true must carry `"disabled": true` on the wire,
// independent of Set.
func TestStatusGates_DisabledPresentWhenTrue(t *testing.T) {
	out := statusGates(map[string]GateInfo{"operator_paused": {Set: true, Disabled: true}})
	if len(out) != 1 {
		t.Fatalf("statusGates returned %d entries, want 1", len(out))
	}
	if got, ok := out[0]["disabled"].(bool); !ok || !got {
		t.Errorf("entry = %+v, want \"disabled\": true", out[0])
	}
	if got, ok := out[0]["set"].(bool); !ok || !got {
		t.Errorf("entry = %+v, want \"set\": true unaffected by Disabled", out[0])
	}
}
